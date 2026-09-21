// Package agent owns the REASON-ACT-OBSERVE (ReAct) loop that sits OUTSIDE the
// model provider. It is the outer orchestrator that turns a user message into a
// final reply by repeatedly calling an llm.Provider and, when the model requests
// tools, running them and feeding the observations back — until the model returns
// a plain-text answer or a max-iteration guard trips.
//
// WHY IT IS SEPARATE (and not inside internal/llm):
//   - llm.Provider.Chat is ONE turn: translate -> POST -> translate out. It is
//     deliberately tool-loop-agnostic so it can be tested with httptest +
//     WithHTTPClient and swapped for any backend (Ollama, mock, OpenAI-style).
//   - The ReAct loop is a POLICY over turns: windowing via history.ContextFor,
//     persistence via db, tool dispatch via an executor registry, and stopping
//     when the model stops requesting tools. Mixing that into the provider would
//     couple the transport to the orchestration and the conversation log.
//
// The loop reads/writes the per-session chat log (db) so every phase is persisted:
//
//	user -> assistant(thinking) -> tool(observation) -> ... -> final assistant.
//
// It does NOT know about WhatsApp; cmd/risers-bot or internal/wa feeds it text
// and routes the reply back.
package agent

import (
	"context"
	"fmt"
	"strings"

	"risers-bot/internal/db"
	"risers-bot/internal/history"
	"risers-bot/internal/llm"
	"risers-bot/internal/log"
)

// ── Agent behavior ─────────────────────────────────────────────────────────

// SystemPrompt is the short persona injected as the first message on every turn.
// Keep it short (tool-use discipline + answer contract); it lives here — not in
// internal/llm — because behavior is an orchestrator concern, not a transport
// concern. See internal/llm/provider.go RoleSystem for the role value.
// Tightened 2026-09-19: model answered stats questions with frontend tutorials
// instead of cricket answers, so the prompt now pins the audience (a fan, not
// a developer) and bans code/tutorials unless asked.
// Extended 2026-09-20: full-scorecard answers list every batter and bowler
// (the tool observation is complete — never reduce it to highlights), and the
// * mark goes only on batters the observation marks "not out".
// Relaxed 2026-09-20: dropped "concisely: 2-3 sentences" — it was suppressing
// the full pundit analysis that summarize_match asks for. Detail level now
// comes from each tool's description, not the system prompt.
// Added 2026-09-20: when a tool returns several team options for a name, ask
// the user which team, wait for the reply, then call the same tool with the
// chosen team's id number.
const SystemPrompt = `You are Risers bot, cricket pundit for DCL team 88 (Risers). You talk to a cricket fan, not a developer. Reply in English. Use tools for DCL games/players/stats. Answer ONLY the user's question from the tool results — answer in the detail the tool result asks for (e.g. a full scorecard lists every batter and bowler, a summary is a full pundit analysis with overview, batting, bowling, key moments, areas to improve). When a tool returns several team options for a name, ask the user which team, wait for their reply, then call the same tool with the chosen team's id number. Mark * only on batters the observation marks "not out". Never write code, tutorials, or app-building advice unless asked. If unsure, say you do not know. Do not hallucinate scores or players.`

// MaxIterations guards against infinite tool loops: a model could keep calling
// tools forever; this hard cap bounds one user turn to a bounded number of
// Reason->Act->Observe rounds. Raised 2026-09-19 from 3 to 10: chained tools
// (search_players → get_player_stats, tournaments → schedule → scorecard)
// need headroom; the loop still stops early once the model answers.
// Configurable later (like history maxTokens).
const MaxIterations = 10

// ToolExecutor runs one tool by name and returns the observation string the
// model should see next. Implementations are registered by the caller (e.g.
// internal/wa or cmd/risers-bot) from a map[string]func(ctx, llm.ToolCall) (string, error).
type ToolExecutor func(ctx context.Context, call llm.ToolCall) (string, error)

// Loop holds the dependencies needed to run one ReAct turn. It owns none of them
// (all injected) so it is simple to test: swap a stub store, a fake provider, and
// a recording executor.
type Loop struct {
	store    *db.Store
	history  *history.History
	provider llm.Provider
	executor ToolExecutor
}

// New wires a ReAct loop. executor resolves ToolCall.Name to a function; the
// caller can use a single fn that switches on call.Name for the first slice.
func New(store *db.Store, history *history.History, provider llm.Provider, executor ToolExecutor) *Loop {
	return &Loop{
		store:    store,
		history:  history,
		provider: provider,
		executor: executor,
	}
}

// Run processes one user turn for a session and returns the final text to reply.
//
// WIRING NOTE: build the message window as
//
//	window := history.ContextFor(session, ...)
//	msgs := append([]llm.ChatMessage{{Role: llm.RoleSystem, Content: SystemPrompt}}, window...)
//	msgs = append(msgs, llm.ChatMessage{Role: llm.RoleUser, Content: userText})
//
// NOTE: res.Thinking (model private reasoning) is persisted but NEVER fed back
// into msgs — matches the qwen message.thinking contract (see internal/llm).
func (l *Loop) Run(ctx context.Context, sessionID, userText string, tools []llm.Tool) (string, error) {

	// guards
	if l == nil || l.store == nil || l.history == nil {
		return "", fmt.Errorf("agent: nil loop/history/store")
	}
	if l.provider == nil {
		return "", fmt.Errorf("agent: nil provider")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("agent: session id empty")
	}

	if _, err := l.store.GetOrCreateSession(sessionID, "cli"); err != nil {
		return "", fmt.Errorf("agent: get or create session: %w", err)
	}
	log.L().Info("agent: turn start", "session_id", sessionID, "user", log.Preview(userText, 80))

	window, err := l.history.ContextFor(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("agent: history context: %w", err)
	}
	log.L().Debug("agent: history window", "session_id", sessionID, "messages", len(window))
	msgs := []llm.ChatMessage{{Role: llm.RoleSystem, Content: SystemPrompt}}
	for _, m := range window {
		msgs = append(msgs, llm.ChatMessage{
			Role:     m.Role,
			Content:  m.Content,
			ToolName: m.ToolName,
		})
	}

	msgs = append(msgs, llm.ChatMessage{Role: llm.RoleUser, Content: userText})
	if err := l.store.SaveMessage(sessionID, "user", userText, "", ""); err != nil {
		return "", fmt.Errorf("agent: unable to save message: %w", err)
	}

	// REACT loop
	for iter := 1; iter <= MaxIterations; iter++ {
		log.L().Info("agent: llm request", "session_id", sessionID, "iter", iter, "messages", len(msgs), "tools", len(tools))
		res, err := l.provider.Chat(ctx, msgs, tools)
		if err != nil {
			return "", fmt.Errorf("agent: provider chat iter %d: %w", iter, err)
		}
		log.L().Info("agent: llm response", "session_id", sessionID, "iter", iter,
			"content_len", len(res.Content), "thinking_len", len(res.Thinking), "tool_calls", len(res.ToolCalls))
		log.L().Debug("agent: thinking", "session_id", sessionID, "iter", iter, "preview", log.Preview(res.Thinking, 200))
		log.L().Debug("agent: content", "session_id", sessionID, "iter", iter, "preview", log.Preview(res.Content, 200))
		if err := l.store.SaveMessage(sessionID, "assistant", res.Content, res.Thinking, ""); err != nil {
			return "", fmt.Errorf("agent: save assistant iter %d: %w", iter, err)
		}
		// Thinking persisted but NEVER appended — matches qwen message.thinking contract.
		// Append assistant turn (Content only) so next Chat sees tool_calls context.
		msgs = append(msgs, llm.ChatMessage{Role: llm.RoleAssistant, Content: res.Content})
		if len(res.ToolCalls) == 0 {
			log.L().Info("agent: turn complete", "session_id", sessionID, "iterations", iter, "content_len", len(res.Content))
			return res.Content, nil
		}
		if l.executor == nil {
			return "", fmt.Errorf("agent: tool calls but no executor")
		}
		for _, tc := range res.ToolCalls {
			log.L().Info("agent: tool call", "session_id", sessionID, "iter", iter, "name", tc.Name)
			log.L().Debug("agent: tool args", "session_id", sessionID, "iter", iter, "name", tc.Name, "args", tc.Arguments)
			obs, err := l.executor(ctx, tc)
			if err != nil {
				return "", fmt.Errorf("agent: tool %q: %w", tc.Name, err)
			}
			log.L().Info("agent: tool result", "session_id", sessionID, "iter", iter, "name", tc.Name, "obs_len", len(obs))
			log.L().Debug("agent: tool observation", "session_id", sessionID, "iter", iter, "name", tc.Name, "preview", log.Preview(obs, 200))
			if err := l.store.SaveMessage(sessionID, "tool", obs, "", tc.Name); err != nil {
				return "", fmt.Errorf("agent: save tool %q: %w", tc.Name, err)
			}
			msgs = append(msgs, llm.ChatMessage{
				Role:       llm.RoleTool,
				Content:    obs,
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
			})
		}
	}

	log.L().Info("agent: max iterations exceeded", "session_id", sessionID, "max", MaxIterations)
	return "", fmt.Errorf("agent: max iterations %d exceeded", MaxIterations)
}
