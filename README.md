# Risers Bot

A WhatsApp cricket bot for the **Risers** team (DCL team 88), built in Go.

Per-session user↔LLM chat (including tool calls and reasoning) is persisted to
SQLite, with the whatsmeow transport for WhatsApp and Ollama (qwen) as the model
backend.

## Requirements

- **Go 1.26+** (project targets `1.26.4`; see `go.mod`)
- **CGO enabled** — the SQLite driver is `github.com/mattn/go-sqlite3`, which
  requires a C compiler. Build with `CGO_ENABLED=1`.
- **Ollama** (optional, only for the LLM path) running at `http://localhost:11434`
  with a tool-calling model such as `qwen3.5:9b`.
- A C toolchain (`clang`/`gcc`) available on `$PATH`.

## Setup

```bash
# 1. Clone / enter the repo
cd risers-bot

# 2. Fetch dependencies (go-sqlite3 is the only dependency for now)
go mod tidy

# 3. Sanity build — verifies DB package + CLI compile with CGO on
CGO_ENABLED=1 go build ./...
```

> If `go mod tidy` reports `go-sqlite3` still marked `// indirect`, that just
> means no `.go` file calls the driver name yet — it becomes direct once the
> code declares `"/github.com/mattn/go-sqlite3"` in an import.

## Build & run

```bash
# Build the CLI binary into ./risers-bot
CGO_ENABLED=1 go build -o risers-bot ./cmd/risers-bot

# Or build every package (validates internal/*)
CGO_ENABLED=1 go build ./...

# Run tests (unit)
go test ./...

# Run integration tests (hits live Ollama on http://localhost:11434)
go test -tags=integration ./internal/llm/ollama -run TestChat_RealOllama -count=1 -v

# Static checks
go vet ./...
go vet -tags=integration ./internal/llm/...
```

### Why `CGO_ENABLED=1`

`internal/db` depends on `mattn/go-sqlite3`, a CGO driver (wraps libsqlite3 via
cgo). On this project the cgo build is intentional — it avoids pure-Go driver
overhead and is the pragmatic choice here. Run all builds/tests with
`CGO_ENABLED=1`.

## Layout

```
risers-bot/
├── AGENTS.md            # pair-programmer + Go-expert workflow (Track A dev / Track B tests)
├── go.mod               # module risers-bot, go 1.26.4
├── go.sum
├── .gitignore           # ignores binary, *.db, /data/, IDE files
├── cmd/
│   └── risers-bot/
│       └── main.go      # entrypoint (package main) — stub, wires Loop → llm → db when built
└── internal/
    ├── db/
    │   ├── db.go        # Store: Open / GetOrCreateSession / SaveMessage / ListMessages / Close
    │   └── schema.sql   # embedded via //go:embed — sessions + messages tables
    ├── llm/
    │   ├── provider.go  # Provider{Chat}, ChatMessage/Tool/ToolCall/Result, Role* consts
    │   └── ollama/
    │       ├── client.go                # POST /api/chat, DefaultURL/DefaultContextWindow=4096, thinking, tool_calls
    │       └── client_integration_test.go # //go:build integration, wire logging (chatReq/chatResp), real Ollama
    ├── history/
    │   ├── history.go   # History{store,compactor,keepRecent,maxTokens}, ContextFor scaffold
    │   ├── window.go    # KeepRecent/ApproxTokens scaffold (≈ len/4)
    │   └── compact.go   # Compactor + Summarize → role='system' row scaffold
    ├── agent/
    │   └── loop.go      # ReAct loop: SystemPrompt, MaxIterations=3, ToolExecutor, Run scaffold
    └── wa/              # (empty) future whatsmeow WhatsApp transport
```

### Go lifecycle notes

- **Executables live in `cmd/<name>/`** as `package main`; library code lives in
  packages under `internal/` and `/`. Bare `go build` in the repo root only works
  if there are `.go` files there — here you must target a package
  (`go build ./cmd/risers-bot`) or use `go build ./...` from the root.
- **Schema is embedded and idempotent.** `schema.sql` uses `CREATE TABLE IF NOT
  EXISTS` and is executed on every `Open`, so an app restart on an existing DB
  file is harmless.
- **Foreign keys are enforced.** `Open` opens SQLite with
  `?_foreign_keys=on`, so a `SaveMessage` for a missing `session_id` fails with
  a FK constraint error — always call `GetOrCreateSession` before inserting
  messages.
- **Runtime data is ignored.** `*.db` and `/data/` are git-ignored; never commit
  databases.

### Ollama Chat wire (OpenAI-compatible REST)

Ollama’s `POST /api/chat` is intentionally compatible with OpenAI’s
`POST /v1/chat/completions` for `model`/`messages`/`tools`/`tool_calls` — the
subset used here (`internal/llm/ollama/client.go`). See `client.go` package
doc `References` and the verbose wire dumps in
`internal/llm/ollama/client_integration_test.go:16` (`loggingRoundTripper`
→ `--- REQUEST wire JSON (chatReq) ---` / `--- RESPONSE wire JSON (chatResp) ---` with
`go test -tags=integration -v`).

### Observability (learning)

Integration tests log the full wire JSON req/resp for every real Ollama call via
`loggingRoundTripper` (`client_integration_test.go:16`). Run with `-v` to see
`chatReq{Model, Messages, Tools, Options{NumCtx}, Stream}` → `chatResp{Content,
Thinking, ToolCalls, done_reason, prompt_eval_count}` and max-token behavior
(`MaxTokens_Survives` / `ServerRespectsNumCtx` with `DefaultContextWindow=4096`).

## Roadmap (parked)

- `history.ContextFor` windowing + `agent.Loop.Run` ReAct (reason/act/observe)
- CLI `!risers` subcommands: `ping`, `stats`, `player`, `top`, `next`, `digest`
- whatsmeow WhatsApp transport in `internal/wa`

## References

- Ollama Chat API — https://docs.ollama.com/api/chat
  (ChatRequest/ChatResponse — the REST shape `client.go` bridges to `llm.Provider`)
- qwen3.5:9b (9.65B, 6.6GB Q4_K_M) — https://ollama.com/library/qwen3.5:9b
  (`ollama run qwen3.5:9b`; multimodal Qwen3.5. Deeper LLM info:
  https://huggingface.co/Qwen/Qwen3.5-9B · https://github.com/QwenLM/Qwen3.5)
