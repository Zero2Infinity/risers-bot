// Package ollama is intended to implement llm.Provider against a local Ollama
// server (POST http://localhost:11434/api/chat). It bridges the provider-neutral
// llm.ChatMessage/Tool/Result types to Ollama's JSON wire format for a qwen-style
// tool-calling model (e.g. qwen3.5:9b).
//
// VERIFIED WIRE FACTS (against the live server — do not reinvent these):
//   - non-streaming request body carries {"model", "messages", "tools"}.
//   - response.message.tool_calls is an array where each element is
//     {id, "function":{"index":N,"name":...,"arguments": {…object…}}}.
//     Non-streaming arguments are a JSON OBJECT (not a string).
//   - response.message.thinking holds the model's private reasoning; surface it
//     as llm.Result.Thinking and NEVER feed it back as an input message.
//
// References:
//   - Ollama Chat API — https://docs.ollama.com/api/chat
//     (ChatRequest/ChatResponse/ChatMessage/ToolDefinition/ModelOptions{num_ctx})
//   - OpenAI Chat Completions — https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create/
//     Ollama's /api/chat is intentionally compatible with OpenAI's messages/tools/
//     tool_calls/tool_call_id contract; Ollama extends it with thinking/options.
//     Any provider speaking the Chat Completions shape (OpenAI, vLLM/LiteLLM, Ollama)
//     is interchangeable for the subset used here (messages/tools, no thinking).
//
// Ollama-specific fields (not in OpenAI) — for future addition, parked now:
//   - think: request-side bool or level "low"/"medium"/"high"/"max" (enables response thinking trace)
//   - options: {num_ctx, temperature, top_p, top_k, seed, stop, num_predict, …} (Modelfile params; num_ctx is wired today)
//   - keep_alive: "5m" | 0 (how long the model stays loaded after the request)
//   - format: "json" | JSON Schema (structured outputs — see Chat request Structured outputs example)
//   - logprobs/top_logprobs (token probabilities, when enabled)
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"risers-bot/internal/llm"
)

const (
	// DefaultURL is the local Ollama endpoint.
	DefaultURL = "http://localhost:11434/api/chat"
	// DefaultContextWindow is the configured context window sent as options.num_ctx.
	// qwen3.5:9B's native window is 32768; we use 4096 for the 12GB ARM box.
	DefaultContextWindow = 4096
)

// ── Options ──────────────────────────────────────────────────────────────────

// Option is a functional option for NewClient.
type Option func(*Client)

// WithURL overrides the default endpoint URL.
func WithURL(u string) Option { return func(cl *Client) { cl.url = u } }

// WithHTTPClient overrides the http.Client (useful for the test harness).
func WithHTTPClient(c *http.Client) Option { return func(cl *Client) { cl.http = c } }

// WithContextWindow overrides the num_ctx sent to Ollama.
func WithContextWindow(n int) Option { return func(cl *Client) { cl.numCtx = n } }

// ── Client ───────────────────────────────────────────────────────────────────

// Client is an llm.Provider bound to a local Ollama instance.
type Client struct {
	url    string
	model  string
	numCtx int
	http   *http.Client
}

// NewClient builds an Ollama provider. model is e.g. "qwen3.5:9b".
func NewClient(model string, opts ...Option) *Client {
	c := &Client{
		url:    DefaultURL,
		model:  model,
		numCtx: DefaultContextWindow,
		http:   &http.Client{Timeout: 120 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ── Wire types (unexported; mirror Ollama's JSON exactly) ────────────────────

// chatReq is the POST /api/chat request body (non-streaming).
type chatReq struct {
	Model    string       `json:"model"`
	Messages []ollamaMsg  `json:"messages"`
	Tools    []ollamaTool `json:"tools,omitempty"`
	Options  *ollamaOpts  `json:"options,omitempty"` // carries num_ctx (c.numCtx)
	Stream   bool         `json:"stream"`
}

// ollamaOpts mirrors Ollama's request "options" object.
type ollamaOpts struct {
	NumCtx int `json:"num_ctx"`
}

// ollamaMsg is one element of the "messages" array. Non-streaming requests only
// need role, content, and tool_call_id (the tool_calls array is response-only,
// so it is intentionally absent here).
type ollamaMsg struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ollamaTool is one element of the "tools" array.
type ollamaTool struct {
	Type     string             `json:"type"` // always "function"
	Function ollamaToolFunction `json:"function"`
}

// ollamaToolFunction holds the callable definition for one tool.
type ollamaToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ollamaToolCall is one element of the RESPONSE's message.tool_calls.
// Arguments is a JSON OBJECT when stream=false (a JSON string when streaming).
type ollamaToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Index     int             `json:"index"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// chatResp is the non-streaming response body.
type chatResp struct {
	Message ollamaRespMsg `json:"message"`
}

// ollamaRespMsg mirrors one response message object.
type ollamaRespMsg struct {
	Content   string           `json:"content"`
	Thinking  string           `json:"thinking"`
	ToolCalls []ollamaToolCall `json:"tool_calls"`
}

// ── Chat (baseline: non-streaming) ───────────────────────────────────────────

// Chat implements llm.Provider.
//
// FLOW (one turn, non-streaming):
//
//  1. TRANSLATE msgs -> wire chatReq body
//     for each llm.ChatMessage -> ollamaMsg{Role, Content, ToolCallID}
//     for each llm.Tool         -> ollamaTool{Type:"function", Function:{Name, Description, Parameters}}
//     body := json.Marshal(chatReq{Model: c.model, Messages: ..., Tools: ..., Options: &ollamaOpts{NumCtx: c.numCtx}, Stream: false})
//
//  2. SEND via c.http (injected so tests can point at httptest.Server)
//     req := http.NewRequestWithContext(ctx, POST, c.url, bytes.NewReader(body))
//     req.Header.Set("Content-Type", "application/json")
//     resp, err := c.http.Do(req); if err -> wrap
//     defer resp.Body.Close()
//     if resp.StatusCode != 200 -> io.ReadAll(io.LimitReader(resp.Body, 4096)) + wrap "status + snippet"
//
//  3. DECODE into chatResp then llm.Result
//     var out chatResp; json.NewDecoder(resp.Body).Decode(&out)
//     return &llm.Result{
//     Content:   out.Message.Content,
//     Thinking:  out.Message.Thinking,        // store; NEVER feed back
//     ToolCalls: normalizeToolCalls(out.Message.ToolCalls),
//     }
//
// NOTE arguments: non-streaming = JSON OBJECT → map[string]any directly; string
// fallback only if streaming is added later (parked — see AGENTS.md non-goals).
func (c *Client) Chat(ctx context.Context, msgs []llm.ChatMessage, tools []llm.Tool) (result *llm.Result, err error) {
	// 1. TRANSLATE: build the wire request body.
	wireMsgs := make([]ollamaMsg, 0, len(msgs))
	for _, m := range msgs {
		wireMsgs = append(wireMsgs, ollamaMsg{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		})
	}

	wireTools := make([]ollamaTool, 0, len(tools))
	for _, t := range tools {
		wireTools = append(wireTools, ollamaTool{
			Type: "function",
			Function: ollamaToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	body, err := json.Marshal(chatReq{
		Model:    c.model,
		Messages: wireMsgs,
		Tools:    wireTools,
		Options:  &ollamaOpts{NumCtx: c.numCtx},
		Stream:   false,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama marshal: %w", err)
	}

	// 2. SEND: POST body to c.url; surface non-200 with a body snippet.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama status %d: %s", resp.StatusCode, snippet)
	}

	// 3. DECODE: body into chatResp, then shape llm.Result.
	var out chatResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama decode: %w", err)
	}

	return &llm.Result{
		Content:   out.Message.Content,
		Thinking:  out.Message.Thinking,
		ToolCalls: normalizeToolCalls(out.Message.ToolCalls),
	}, nil
}

// normalizeToolCalls converts wire tool_calls to llm.ToolCall. Handle
// arguments being a JSON OBJECT (non-streaming) OR a quoted JSON STRING
// (streaming): try object first, then fall back to unmarshal-string.
func normalizeToolCalls(wire []ollamaToolCall) []llm.ToolCall {
	// out carries the translated, provider-neutral list (same length as input).
	out := make([]llm.ToolCall, 0, len(wire))

	for _, tc := range wire {
		// Step 1: decode tc.Function.Arguments (json.RawMessage) into a map.
		args := map[string]any{}
		if len(tc.Function.Arguments) > 0 {
			// Try the OBJECT form first (non-streaming)
			if err := json.Unmarshal(tc.Function.Arguments, &args); err != nil {
				// Object form failed -> Arguments may be a QUOTED JSON STRING (streaming)
				var s string
				if err2 := json.Unmarshal(tc.Function.Arguments, &s); err2 == nil {
					_ = json.Unmarshal([]byte(s), &args)
				}
			}
		}

		// Step 2: copy the wire element's identity into the neutral type.
		out = append(out, llm.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}

	return out
}
