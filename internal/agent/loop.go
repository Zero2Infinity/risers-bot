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

	"risers-bot/internal/db"
	"risers-bot/internal/llm"
)

// ── Agent behavior ─────────────────────────────────────────────────────────

// SystemPrompt is the short persona injected as the first message on every turn.
// Keep it 1-2 sentences (concise, tool-use discipline); it lives here — not in
// internal/llm — because behavior is an orchestrator concern, not a transport
// concern. See internal/llm/provider.go RoleSystem for the role value.
// TODO(you): tighten wording once you observe misbehavior (e.g. hallucination).
const SystemPrompt = `You are Risers bot for DCL team 88. Be concise. Use tools for DCL stats/players; answer directly otherwise.`

// MaxIterations guards against infinite tool loops: a model could keep calling
// tools forever; this hard cap bounds one user turn to a bounded number of
// Reason->Act->Observe rounds. Configurable later (like history maxTokens).
const MaxIterations = 3

// ToolExecutor runs one tool by name and returns the observation string the
// model should see next. Implementations are registered by the caller (e.g.
// internal/wa or cmd/risers-bot) from a map[string]func(ctx, llm.ToolCall) (string, error).
type ToolExecutor func(ctx context.Context, call llm.ToolCall) (string, error)

// Loop holds the dependencies needed to run one ReAct turn. It owns none of them
// (all injected) so it is simple to test: swap a stub store, a fake provider, and
// a recording executor.
type Loop struct {
	store    *db.Store
	provider llm.Provider
	executor ToolExecutor
}

// New wires a ReAct loop. executor resolves ToolCall.Name to a function; the
// caller can use a single fn that switches on call.Name for the first slice.
func New(store *db.Store, provider llm.Provider, executor ToolExecutor) *Loop {
	return &Loop{store: store, provider: provider, executor: executor}
}

// Run processes one user turn for a session and returns the final text to reply.
//
// WIRING NOTE: build the message window as
//
//	window := history.ContextFor(session, ...)
//	msgs := append([]llm.ChatMessage{{Role: llm.RoleSystem, Content: SystemPrompt}}, window...)
//	msgs = append(msgs, llm.ChatMessage{Role: llm.RoleUser, Content: userText})
//
// The prepended SystemPrompt is the behavior contract for every Chat call.
//
// PSEUDOCODE:
//
//	msgs := history-context for session (the budgeted window)
//	msgs = append(msgs, {role:"user", content:userText})
//	store.SaveMessage(session, "user", userText, "", "")
//
//	for iter := 1; iter <= MaxIterations; iter++:
//	  res, err := provider.Chat(ctx, msgs, tools)
//	    -> persist assistant row with res.Thinking
//	  if len(res.ToolCalls) == 0:
//	    return res.Content                     // REASON produced the final answer
//	  for each tool call in res.ToolCalls:     // ACT
//	    obs, err := executor(ctx, call)        // e.g. fetchStats(call.Arguments)
//	    toolMsg := {role:"tool", content:obs, toolCallID:call.ID, toolName:call.Name}
//	    store.SaveMessage(session, "tool", obs, "", call.Name)
//	    msgs = append(msgs, toolMsg)           // OBSERVE feeds the observation back
//
//	return <exhausted-message>  // MaxIterations reached without a final answer
//
// NOTE: res.Thinking (model private reasoning) is persisted but NEVER fed back
// into msgs — matches the qwen message.thinking contract (see internal/llm).
func (l *Loop) Run(ctx context.Context, sessionID, userText string, tools []llm.Tool) (string, error) {
	panic("unimplemented")
}
