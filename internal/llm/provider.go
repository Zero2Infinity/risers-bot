// Package llm defines the provider abstraction for the conversational loop,
// and the data model that both the DB-backed chat log and the Ollama HTTP API
// bridge over. It is the seam that lets the loop (cmd/risers-bot or internal/wa)
// stay provider-agnostic: it calls Provider.Chat and gets back either a textual
// reply or a set of tool calls, WITHOUT knowing whether the backend is Ollama,
// a mock, or a future OpenAI-compatible endpoint.
//
// Relationship to other packages:
//   - db owns persistence (role/content/thinking/tool_name rows).
//   - history owns the context-window policy (what subset to send).
//   - llm owns ONLY "talk to a model": it maps a []ChatMessage (built from db
//     rows via history) into a request, and normalizes the response.
//
// Deliberately kept free of any net/http import at the interface level so it can
// be tested with an in-memory stub.
package llm

import "context"

// Role values supported by both the chat log (db.Message.Role) and Ollama.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
	RoleSystem    = "system"
)

// ChatMessage is one turn in the conversation sent to the model. It is produced
// from db.Message by the caller (the loop): Thinking is intentionally NOT sent
// to the model — qwen puts private reasoning in message.thinking, which must not
// be fed back.
type ChatMessage struct {
	Role    string // RoleUser | RoleAssistant | RoleTool | RoleSystem
	Content string
	// ToolCallID ties a RoleTool message back to the ToolCall it answers.
	ToolCallID string
	// ToolName is set when Role == RoleTool to help the loop persist it.
	ToolName string
}

// Tool defines a callable tool the model may invoke (e.g. fetch_stats, get_player).
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON-schema-ish parameter object
}

// ToolCall is what the model requests the loop to execute.
type ToolCall struct {
	// ID is the opaque identifier echoed back in the RoleTool result message.
	ID string
	// Name is the tool to run (must match a Tool provided to Chat).
	Name string
	// Arguments is the raw JSON object the model supplied for the call
	// (non-streaming Ollama returns a JSON object, not a string).
	Arguments map[string]any
}

// Result is the outcome of a single Chat invocation.
type Result struct {
	// Content is the plain-text reply; empty when the model only made tool calls.
	Content string
	// Thinking is the model's private reasoning (stored, never fed back).
	Thinking string
	// ToolCalls is non-empty when the model has requested tool execution.
	ToolCalls []ToolCall
}

// Provider is the interface the conversational loop depends on. Implementations
// translate ChatMessage+T ools into a backend request and normalize the response
// into a Result.
type Provider interface {
	// Chat runs one turn of the conversation. msgs is the history window produced
	// by history.ContextFor plus the latest user turn. tools lists callables the
	// model may use; pass nil for a plain (non-tool) chat.
	//
	// PSEUDOCODE (implementations):
	//   req := translate(msgs, tools)                    // shape per backend
	//   resp, err := backendChat(ctx, req)               // POST /api/chat
	//   return normalize(resp)                           // content, thinking, tool_calls
	Chat(ctx context.Context, msgs []ChatMessage, tools []Tool) (*Result, error)
}
