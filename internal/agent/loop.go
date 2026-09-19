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
)

// ── Agent behavior ─────────────────────────────────────────────────────────

// SystemPrompt is the short persona injected as the first message on every turn.
// Keep it short (tool-use discipline + answer contract); it lives here — not in
// internal/llm — because behavior is an orchestrator concern, not a transport
// concern. See internal/llm/provider.go RoleSystem for the role value.
// Tightened 2026-09-19: model answered stats questions with frontend tutorials
// instead of cricket answers, so the prompt now pins the audience (a fan, not
// a developer) and bans code/tutorials unless asked.
const SystemPrompt = `You are Risers bot, cricket pundit for DCL team 88 (Risers). You talk to a cricket fan, not a developer. Reply in English. Use tools for DCL games/players/stats. Answer ONLY the user's question from the tool results, concisely: a short list or 2-3 sentences. Never write code, tutorials, or app-building advice unless asked. If unsure, say you do not know. Do not hallucinate scores or players.`

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

	window, err := l.history.ContextFor(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("agent: history context: %w", err)
	}
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
		res, err := l.provider.Chat(ctx, msgs, tools)
		if err != nil {
			return "", fmt.Errorf("agent: provider chat iter %d: %w", iter, err)
		}
		if err := l.store.SaveMessage(sessionID, "assistant", res.Content, res.Thinking, ""); err != nil {
			return "", fmt.Errorf("agent: save assistant iter %d: %w", iter, err)
		}
		// Thinking persisted but NEVER appended — matches qwen message.thinking contract.
		// Append assistant turn (Content only) so next Chat sees tool_calls context.
		msgs = append(msgs, llm.ChatMessage{Role: llm.RoleAssistant, Content: res.Content})
		if len(res.ToolCalls) == 0 {
			return res.Content, nil
		}
		if l.executor == nil {
			return "", fmt.Errorf("agent: tool calls but no executor")
		}
		for _, tc := range res.ToolCalls {
			obs, err := l.executor(ctx, tc)
			if err != nil {
				return "", fmt.Errorf("agent: tool %q: %w", tc.Name, err)
			}
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

	return "", fmt.Errorf("agent: max iterations %d exceeded", MaxIterations)
}
