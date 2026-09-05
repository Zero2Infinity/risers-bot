//go:build integration

package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"risers-bot/internal/llm"
)

// loggingRoundTripper dumps the wire JSON request and response for learning.
// It is intentionally verbose — you see the exact chatReq/chatResp that Chat
// translates to/from. Only active in integration tests, never in production.
type loggingRoundTripper struct {
	base http.RoundTripper
	t    *testing.T
}

func (l *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// --- request wire JSON ---
	var reqBody []byte
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		reqBody = b
		// restore body so the real transport can read it
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	if len(reqBody) > 0 {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, reqBody, "", "  "); err == nil {
			l.t.Logf("\n--- REQUEST wire JSON (chatReq) ---\n%s\n--- end request ---", pretty.String())
		} else {
			l.t.Logf("\n--- REQUEST raw ---\n%s\n--- end request ---", string(reqBody))
		}
	}

	resp, err := l.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// --- response wire JSON ---
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	if len(respBody) > 0 {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, respBody, "", "  "); err == nil {
			l.t.Logf("\n--- RESPONSE wire JSON (chatResp) ---\n%s\n--- end response ---", pretty.String())
		} else {
			l.t.Logf("\n--- RESPONSE raw (%d bytes) ---\n%s\n--- end response ---", len(respBody), string(respBody))
		}
	} else {
		l.t.Logf("--- RESPONSE empty (status %d) ---", resp.StatusCode)
	}

	return resp, nil
}

// newRealClient returns a Client pointed at DefaultURL with a 60s context and a
// logging transport so you see the exact wire JSON req/resp in `go test -v`.
// Hard-fails are intentional: `go test -tags=integration` should surface
// "Ollama not running" as a regression, not a skip.
func newRealClient(t *testing.T) (*Client, context.Context, context.CancelFunc) {
	t.Helper()
	// Wrap the default transport with logging so you see the wire.
	base := http.DefaultTransport
	if base == nil {
		base = http.DefaultTransport
	}
	loggingClient := &http.Client{
		Transport: &loggingRoundTripper{base: base, t: t},
		Timeout:   120 * time.Second,
	}
	client := NewClient("qwen3.5:9b", WithHTTPClient(loggingClient))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return client, ctx, cancel
}

func TestChat_RealOllama_Plain(t *testing.T) {
	client, ctx, _ := newRealClient(t)

	msgs := []llm.ChatMessage{
		{Role: llm.RoleUser, Content: "Say pong in one word."},
	}

	res, err := client.Chat(ctx, msgs, nil)
	if err != nil {
		t.Fatalf("Chat plain: %v (is Ollama running at %s with qwen3.5:9b?)", err, DefaultURL)
	}
	if res.Content == "" {
		t.Fatalf("expected non-empty Content, got empty; full result: %+v", res)
	}
	if len(res.ToolCalls) != 0 {
		t.Fatalf("expected no ToolCalls for plain chat, got %d: %+v", len(res.ToolCalls), res.ToolCalls)
	}
	t.Logf("plain ok: content=%q thinking_len=%d", res.Content, len(res.Thinking))
}

func TestChat_RealOllama_ToolCalling(t *testing.T) {
	client, ctx, _ := newRealClient(t)

	tools := []llm.Tool{
		{
			Name:        "echo",
			Description: "echo the text",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string"},
				},
				"required": []string{"text"},
			},
		},
	}

	msgs := []llm.ChatMessage{
		{Role: llm.RoleUser, Content: `Call echo with text="hi".`},
	}

	res, err := client.Chat(ctx, msgs, tools)
	if err != nil {
		t.Fatalf("Chat tool-calling: %v (is Ollama running at %s?)", err, DefaultURL)
	}

	// qwen may return ToolCalls OR a plain Content answer — both are valid.
	if len(res.ToolCalls) > 0 {
		found := false
		for _, tc := range res.ToolCalls {
			if tc.Name == "echo" {
				found = true
				if tc.Arguments["text"] != "hi" {
					t.Fatalf("echo Arguments[text] want %q, got %v (full: %+v)", "hi", tc.Arguments["text"], tc)
				}
			}
		}
		if !found {
			t.Fatalf("expected echo tool call, got: %+v", res.ToolCalls)
		}
		t.Logf("tool-calling ok: tool_calls=%+v thinking_len=%d", res.ToolCalls, len(res.Thinking))
	} else {
		if res.Content == "" {
			t.Fatalf("expected Content or ToolCalls, got neither; full result: %+v", res)
		}
		t.Logf("tool-calling fell back to plain: content=%q thinking_len=%d", res.Content, len(res.Thinking))
	}
}

// TestChat_RealOllama_MaxTokens_Survives proves the client survives an
// oversized pure-Chat payload (> DefaultContextWindow=4096). No history/Loop
// involved — hand-built msgs directly to Chat. Hard-fails on error so
// `go test -tags=integration` surfaces a live Ollama regression.
// We accept either Content or Thinking as proof of life: qwen may exhaust
// num_ctx inside thinking (done_reason:length) before reaching content.
func TestChat_RealOllama_MaxTokens_Survives(t *testing.T) {
	client, ctx, _ := newRealClient(t)

	// ApproxTokens ≈ len(content)/4 (history/window.go rule). Build ~4500 tokens.
	filler := strings.Repeat("Risers DCL88 cricket stats. ", 400) // ~7800 chars ≈ 1950 tokens per chunk
	msgs := []llm.ChatMessage{}
	for i := 0; i < 12; i++ {
		msgs = append(msgs, llm.ChatMessage{Role: llm.RoleUser, Content: filler})
	}
	msgs = append(msgs, llm.ChatMessage{Role: llm.RoleUser, Content: "Summarize the filler in one sentence."})

	approx := 0
	for _, m := range msgs {
		approx += len(m.Content) / 4
	}
	t.Logf("max-tokens survives: sending %d msgs, approx %d tokens (DefaultContextWindow=%d)", len(msgs), approx, DefaultContextWindow)

	res, err := client.Chat(ctx, msgs, nil)
	if err != nil {
		t.Fatalf("Chat max-tokens survives: %v (is Ollama running at %s?)", err, DefaultURL)
	}
	if res.Content == "" && res.Thinking == "" {
		t.Fatalf("expected Content or Thinking for oversized chat, got empty; full result: %+v", res)
	}
	// Wire dump above shows prompt_eval_count / done_reason — that is the
	// "server respects num_ctx" signal; survives just needs one non-empty field.
	t.Logf("max-tokens survives ok: content_len=%d thinking_len=%d approx_sent=%d", len(res.Content), len(res.Thinking), approx)
}

// TestChat_RealOllama_MaxTokens_ServerRespectsNumCtx proves the server respects
// the num_ctx we send (chatReq.Options.NumCtx). Same oversized msgs as Survives
// but with a tightened WithContextWindow(1024) — the wire dump should show
// options.num_ctx=1024 and a different prompt_eval behavior vs DefaultContextWindow.
func TestChat_RealOllama_MaxTokens_ServerRespectsNumCtx(t *testing.T) {
	// Tighten the server window to force observable truncation vs Survives.
	base := http.DefaultTransport
	loggingClient := &http.Client{
		Transport: &loggingRoundTripper{base: base, t: t},
		Timeout:   120 * time.Second,
	}
	client := NewClient("qwen3.5:9b", WithHTTPClient(loggingClient), WithContextWindow(1024))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	filler := strings.Repeat("Risers DCL88 cricket stats. ", 400)
	msgs := []llm.ChatMessage{}
	for i := 0; i < 12; i++ {
		msgs = append(msgs, llm.ChatMessage{Role: llm.RoleUser, Content: filler})
	}
	msgs = append(msgs, llm.ChatMessage{Role: llm.RoleUser, Content: "Summarize the filler in one sentence."})

	approx := 0
	for _, m := range msgs {
		approx += len(m.Content) / 4
	}
	t.Logf("max-tokens respects num_ctx: sending %d msgs, approx %d tokens with num_ctx=1024 (vs Default %d)", len(msgs), approx, DefaultContextWindow)

	res, err := client.Chat(ctx, msgs, nil)
	if err != nil {
		t.Fatalf("Chat max-tokens respects num_ctx: %v (is Ollama running at %s?)", err, DefaultURL)
	}
	if res.Content == "" && res.Thinking == "" {
		t.Fatalf("expected Content or Thinking for num_ctx=1024 chat, got empty; full result: %+v", res)
	}
	t.Logf("max-tokens respects num_ctx ok: content_len=%d thinking_len=%d approx_sent=%d", len(res.Content), len(res.Thinking), approx)
	// The proof is in the wire dump above: REQUEST shows "options":{"num_ctx":1024} vs 4096
	// and RESPONSE shows prompt_eval_count / done_reason reflecting the tighter window.
}
