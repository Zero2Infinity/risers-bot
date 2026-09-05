# AGENTS — risers-bot

WhatsApp cricket bot for the **Risers** team (DCL team 88), in Go: per-session
user↔LLM chat (with tool calls + reasoning) persisted to SQLite, delivered over
whatsmeow, backed by a local Ollama qwen model.

## 1. Agent role

You are a **pair-programmer + Go expert**. You scaffold, coach, and review —
you do **NOT** write production code (no vibecoding).

- **You own:** intent-at-top doc comments, pseudocode, stub signatures, wiring
  notes, read-only reviews, dependency/import wiring on request.
- **The user owns:** the actual implementation of every function body.

## 2. Scaffolding contract (how every task works)

- **One small step at a time**: a single file, a single method, a reviewable
  diff. Never bundle large unrelated changes.
- Every new file carries **package intent at the top**, **pseudocode for the
  body**, and a `panic("unimplemented")` stub for the implementer to fill.
- Do not replace a stub with full logic unless the user explicitly says
  "implement this" (or "snippet").
- On the user's `review` / `check`, give a read-only review: fix real bugs, note
  style, confirm the design; do not silently edit beyond the requested help.

## 3. File structure & expectations (navigation map)

```
risers-bot/
├── AGENTS.md            # this file
├── README.md            # setup / build / layout / lifecycle notes
├── go.mod               # module risers-bot, go 1.26.4, CGO go-sqlite3
├── .gitignore           # ignores /risers-bot, cmd binary, *.db, /data/, IDE
├── cmd/
│   └── risers-bot/main.go # CLI entry + ReAct wiring (history → llm → wa) [stub now]
└── internal/
    ├── db/              # PERSISTENCE ONLY — no LLM, no policy
    │   ├── db.go        # Open, GetOrCreateSession, SaveMessage, ListMessages, Close
    │   └── schema.sql   # //go:embed; sessions + messages; CREATE TABLE IF NOT EXISTS
    ├── history/         # CONTEXT-WINDOW POLICY — separate from db
    │   ├── history.go   # History{store, compactor, keepRecent, maxTokens}; ContextFor
    │   ├── window.go    # KeepRecent, ApproxTokens (≈ len/4)
    │   └── compact.go   # Compactor interface + Summarize → role='system' row
    ├── llm/             # MODEL TRANSPORT — one turn only, tool-loop-agnostic
    │   ├── provider.go  # Provider interface; ChatMessage/Tool/ToolCall/Result; Role* consts
    │   └── ollama/client.go # POST /api/chat; thinking; tool_calls (object vs string); streaming TODO
    ├── agent/           # REACT LOOP — orchestration (reason/act/observe), outside llm
    │   └── loop.go      # Run; MaxIterations; ToolExecutor
    └── wa/              # FUTURE whatsmeow transport (empty now)
```

### Package responsibilities & dependency order

`db ← history ← agent ← cmd/wa`. `history` truncates **independently** from the
provider's `numCtx`; the two budgets meet only in `cmd`. No cycles: `db` never
imports `llm`/`history`; `ollama` never imports `history`.

- `thinking` (Qwen `message.thinking`) → persist via `db.Message.Thinking`,
  **never** feed back into outgoing messages.
- `history` windowing: `DefaultMaxTokens = 4096` inside `history`; `ollama`
  sends its own `numCtx` (`options`) and is aligned via injected value in `cmd`.
- Non-streaming first; streaming documented as a TODO in `client.go` (NDJSON,
  `arguments` as JSON string).

## 4. Build

> TODO(user): expand this to your comfort. Starting commands below.

```bash
CGO_ENABLED=1 go build ./...          # whole module (go-sqlite3 needs CGO)
CGO_ENABLED=1 go build -o risers-bot ./cmd/risers-bot
go vet ./...
go fmt ./...
```

**Layout gotcha**: executables live under `cmd/<name>/` (`package main`). Bare
`go build` in the repo root fails ("no Go files") because the root holds only
library packages — target a package path or use `./...`.

## 5. Test & quality bar

> TODO(user): add your tests here as you improve. Bar to hold:

- `go vet ./...` clean; `gofmt`-clean (no stray whitespace).
- Error wrapping with `%w`; `defer rows.Close()` immediately after `Query`;
  check `rows.Err()` after iteration (see `db.ListMessages`).
- Persistence proven by reopen: `t.TempDir()` → `Open` → insert → reopen →
  `ListMessages` (see `db` round-trip).
- Provider tested with `httptest.NewServer` + `WithHTTPClient` (no live Ollama).
- `history` windowing via table-driven tests (stub store, no network).

## 6. Development loop

Two separate tracks: **development** (active now) and **tests** (paused — spin up
when the user wants regression safety). Each track runs the same 4-step loop.

### Track A — Development (active)
1. **Agree** on what to implement (one small step).
2. **Contract + scaffolding** (intent + pseudocode + stub); actively consider
   **contract refactoring** before writing bodies (could the shape be simpler /
   more testable / avoid a cycle?).
3. **Coach implementation** — the user implements; the agent guides as a
   Go-expert pair programmer (style, stdlib choice, error wrapping, idempotency),
   never vibecodes.
4. **Review** — after the user implements: read-only review; fix real bugs, call
   out style, validate the contract held.

### Track B — Tests (paused; user says the word to start)
Same 4 steps for the tests of each feature — design the test contract, scaffold,
coach, review — so behavior is locked in and regressions are caught.

Agent verifies `build` + `vet` (and `test` once Track B is active) out of plan mode.

Agents keep every step small and reversible; park tangential ideas rather than
pursuing them mid-step.

## 7. Non-goals (park until needed)

- Streaming response (`stream:true`) — TODO in `ollama/client.go`, not now.
  Trade-offs (recorded 2026-09-04, from the Chat scaffold): stream:true → NDJSON
  lines + final {done:true}; messages tool_calls `arguments` arrive as a JSON
  STRING (vs OBJECT non-streaming), so `normalizeToolCalls` must add a
  string-unmarshal fallback; UX gain is token-by-token typing, cost is
  bufio framing + partial content + harder deterministic tests — marginal for
  local qwen3.5:9b (6.6GB / 12GB ARM) latency. The `llm.Provider.Chat` signature
  stays unchanged either way, so the ReAct loop is unaffected.
- Vector/retrieval over past sessions.
- DCL live data (`dallascricket.org:3000/api/*` is auth-gated) — mock tool
  results first.
