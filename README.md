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

# Or build every package (validates internal/db too)
CGO_ENABLED=1 go build ./...

# Run tests (once they exist)
go test ./...

# Static checks
go vet ./...
```

### Why `CGO_ENABLED=1`

`internal/db` depends on `mattn/go-sqlite3`, a CGO driver (wraps libsqlite3 via
cgo). On this project the cgo build is intentional — it avoids pure-Go driver
overhead and is the pragmatic choice here. Run all builds/tests with
`CGO_ENABLED=1`.

## Layout

```
risers-bot/
├── go.mod               # module risers-bot, go 1.26.4
├── go.sum
├── .gitignore           # ignores binary, *.db, /data/, IDE files
├── cmd/
│   └── risers-bot/
│       └── main.go      # entrypoint (package main) — what `go build ./cmd/risers-bot` builds
└── internal/
    ├── db/
    │   ├── db.go        # Store: Open / GetOrCreateSession / SaveMessage / ListMessages / Close
    │   └── schema.sql   # embedded via //go:embed — sessions + messages tables
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

## Roadmap (parked)

- CLI `!risers` subcommands: `ping`, `stats`, `player`, `top`, `next`, `digest`
- Ollama `Provider` interface + tool calling, with per-session capture
- whatsmeow WhatsApp transport in `internal/wa`
