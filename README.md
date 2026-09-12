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

## Layout (implemented)

```
risers-bot/
├── AGENTS.md            # pair-programmer + Go-expert workflow (Track A dev / Track B tests)
├── go.mod               # module risers-bot, go 1.26.4
├── go.sum
├── .gitignore           # ignores binary, *.db, /data/, IDE files
├── cmd/
│   └── risers-bot/
│       └── main.go      # entrypoint (package main) — still stub (next: wire Store→History→ollama→Loop)
└── internal/
    ├── db/
    │   ├── db.go        # Store: Open / GetOrCreateSession / SaveMessage / ListMessages / Close
    │   └── schema.sql   # embedded via //go:embed — sessions + messages tables (id, session_id FK, role, content, thinking, tool_name)
    ├── llm/
    │   ├── provider.go  # Provider{Chat}, ChatMessage/Tool/ToolCall/Result, Role* consts (thinking never fed back)
    │   └── ollama/
    │       ├── client.go                # POST /api/chat, DefaultURL/DefaultContextWindow=4096, thinking, tool_calls (object+string fallback)
    │       └── client_integration_test.go # //go:build integration, loggingRoundTripper wire dumps, real Ollama (Plain/ToolCalling/MaxTokens)
    ├── history/
    │   ├── history.go   # History{store,compactor,keepRecent} — ContextFor: ListMessages→KeepRecent→Summarize→[system]+tail
    │   ├── window.go    # KeepRecent (≤0→empty guard) / ApproxTokens (≈ len/4) — pure Go, no LLM
    │   └── compact.go   # Compactor interface + Summarize(ctx,store,compactor,sessionID,head) → role='system' persisted (cli kind)
    ├── agent/
    │   └── loop.go      # ReAct outer loop: SystemPrompt (cricket pandit), MaxIterations=3, ToolExecutor, Run(store+history+provider) → user→assistant(thinking)→tool→final
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

## Features

Track implementation progress with t-shirt sizes — `XS <50L` no DAG · `S ~100L 1 pkg` · `M ~150L cross-pkg or LLM/net+httptest` · `L wiring 2-3 pkgs + state` · `XL 3+ files external dep`. Done → Next (DAG order) → Parked.

| Feature | Size | Status | Files |
|---|---|---|---|
| `db` persistence — `Open/GetOrCreateSession/SaveMessage/ListMessages/Close`, `schema.sql //go:embed`, FK `_foreign_keys=on` | **S** | Done | `internal/db/db.go`, `internal/db/schema.sql` |
| `history` windowing — `KeepRecent` (≤0→empty guard), `ApproxTokens` (len/4), `History{store,compactor,keepRecent}`, `ContextFor` window-only | **S** | Done | `internal/history/window.go`, `internal/history/history.go` |
| `history` compaction — `Compactor` interface, `Summarize(ctx,store,compactor,sessionID,head)` → `role='system'` persisted (cli kind, full head retained) | **M** | Done | `internal/history/compact.go` |
| `llm` abstraction — `Provider`, `ChatMessage/Tool/ToolCall/Result`, `Role*` consts, thinking never fed back | **XS** | Done | `internal/llm/provider.go` |
| `ollama` non-streaming — `NewClient/WithURL/WithHTTPClient/WithContextWindow`, `POST /api/chat`, `options.num_ctx=4096`, `normalizeToolCalls` object→string fallback | **M** | Done | `internal/llm/ollama/client.go`, `client_integration_test.go` |
| `agent` ReAct outer loop — `SystemPrompt` cricket pandit, `MaxIterations=3`, `ToolExecutor`, `Loop.Run(store+history+provider)` outer save→Chat→save assistant(thinking)→append assistant Content-only→tools→save tool→append tool | **M** | Done | `internal/agent/loop.go` |
| **Compactor adapter** `llm→history` — bind `ollama.Client` to `history.Compactor` (5-bullet prompt) without cycle | **S** | Next | `internal/agent/compactor.go` (new leaf) |
| **History budget + DB-as-Cache** — `maxTokens/DefaultMaxTokens=4096`, `ApproxTokens` gate, dedup `resummarize` (ID-range `head[0].ID:head[last].ID:len` over `ListMessages`) | **M** | Next | `internal/history/history.go`, `window.go` |
| **CLI wiring** `cmd/risers-bot/main.go` — `db.Open` → `History.New` → `ollama.NewClient(WithContextWindow aligned)` → `agent.New`, `!risers` `ping/stats/player/top/next/digest` | **L** | Next | `cmd/risers-bot/main.go` |
| **ToolExecutor registry** mock DCL `fetch_stats/get_player` (auth-gated `dallascricket.org:3000/api/*` → mock first) | **M** | Next | `internal/tools/*.go` (new) |
| **whatsmeow transport** `internal/wa` | **XL** | Parked | `internal/wa/*` (new) |
| **Track B tests** — `db` reopen proof `t.TempDir`, `history` table-driven windowing, `agent` fake Provider+executor, `ollama` `httptest` | **L** | Parked | `*_test.go` across `db/history/llm/ollama/agent` |
| **Streaming** `stream:true` NDJSON + `arguments` string fallback (prepared `normalizeToolCalls:243`) | **L** | Parked | `internal/llm/ollama/client.go` |
| **Vector/retrieval** over past sessions | **XL** | Parked | `internal/vector/*` (new) |
| **DCL live API** auth-gated | **L** | Parked | `internal/tools/dcl.go` (new) |

> **Layout ↔ Features:** Layout shows what’s implemented on disk today; Features tracks what’s done/next/parked for you and the agent. Next = DAG order `db ← history ← agent ← cmd/wa` (`AGENTS.md`).

## Roadmap (parked — see Features)

- whatsmeow WhatsApp transport in `internal/wa` (now tracked as XL Parked above)
- Vector/retrieval over past sessions (XL Parked)
- Streaming `stream:true` (L Parked) — interface `llm.Provider.Chat` stays unchanged either way

## References

- Ollama Chat API — https://docs.ollama.com/api/chat
  (ChatRequest/ChatResponse — the REST shape `client.go` bridges to `llm.Provider`)
- qwen3.5:9b (9.65B, 6.6GB Q4_K_M) — https://ollama.com/library/qwen3.5:9b
  (`ollama run qwen3.5:9b`; multimodal Qwen3.5. Deeper LLM info:
  https://huggingface.co/Qwen/Qwen3.5-9B · https://github.com/QwenLM/Qwen3.5)
