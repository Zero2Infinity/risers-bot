# Risers Bot

WhatsApp cricket pundit bot for **Risers (DCL team 88)**.

Per-session user↔LLM chat with tool calls, persisted to SQLite, delivered over
whatsmeow, backed by a local Ollama model. LLM tools fetch schedule, scorecards,
standings, and player stats from dallascricket.org and hand the JSON to the model
for answers.

## Features

| Feature | Status | Files |
|---|---|---|
| `db` persistence — `Open`, `GetOrCreateSession`, `SaveMessage`, `ListMessages`, `Close` | Done | `internal/db/db.go`, `internal/db/schema.sql` |
| `history` windowing — `KeepRecent`, `ApproxTokens`, `History.ContextFor` | Done | `internal/history/` |
| `history` compaction — `Compactor` interface + `Summarize` | Done | `internal/history/compact.go` |
| `llm` abstraction — `Provider`, `ChatMessage`/`Tool`/`ToolCall`/`Result` | Done | `internal/llm/provider.go` |
| `ollama` client — non-streaming `POST /api/chat`, `num_ctx`, `thinking`, tool calls | Done | `internal/llm/ollama/client.go` |
| `agent` ReAct loop — `SystemPrompt`, `MaxIterations`, `ToolExecutor`, `Loop.Run` | Done | `internal/agent/loop.go` |
| `tools` DCL API — `DCLClient`, `Registry`, 9 tools (below) | Next | `internal/tools/*.go` (new) |
| CLI wiring — `db` → `history` → `ollama` → `agent` + tools | Next | `cmd/risers-bot/main.go` |
| Compactor adapter (`ollama` → `history`) | Next | `internal/agent/compactor.go` (new) |
| whatsmeow transport | Parked | `internal/wa/*` (new) |
| Streaming (`stream:true`) | Parked | `internal/llm/ollama/client.go` |
| Vector/retrieval over past sessions | Parked | `internal/vector/*` (new) |

### DCL tools (next)

Base URL: `https://dallascricket.org:3000` (public `GET`, no auth).
Default team: Risers, `DCL_TEAM_ID=88`.

| Tool | Endpoint | Notes |
|---|---|---|
| `get_tournaments` | `/api/gettournamentlist` | List tournaments |
| `get_schedule` | `/api/schedules/{teamId}` | Team-scoped fixtures |
| `get_match_scorecard` | `/api/getmatchdata/{match_id}` | Full scorecard, both innings |
| `get_points_table` | `/api/tournamentpointstable/{tournament_id}` | Standings, W/L/NRR |
| `search_players` | `/api/getplayerlistbysearch?q=` | Name → `user_id` |
| `get_player_stats` | `/api/getplayerstatistics/{user_id}` | Career batting + bowling |
| `get_player_stats_filtered` | `/api/getplayerstatistics/{user_id}` + `tournament_id` | Per-tournament stats with precomputed totals |
| `summarize_match` | `/api/getmatchdata/{match_id}` | Risers pundit summary: complete card + insights |
| `find_opponent` | team fixtures | Partial team name → team and match ids |

Deferred: `get_live_scores` (`/api/getbannerscoreinfo`), response trimming for the
4K context window, caching. Tools return full JSON for now.

## Repository structure

```
risers-bot/
├── AGENTS.md
├── README.md
├── go.mod
├── cmd/
│   └── risers-bot/main.go   # CLI entry (stub)
└── internal/
    ├── db/                  # SQLite persistence only
    ├── history/             # Context-window policy
    ├── llm/                 # Provider interface
    │   └── ollama/          # Ollama transport
    ├── agent/               # ReAct loop
    ├── tools/               # DCL API tools (new, planned)
    └── wa/                  # whatsmeow transport (empty)
```

Dependency order: `db ← history ← agent ← cmd/wa`, `agent → tools → DCL API`.
`db` never imports `llm`/`history`; `ollama` never imports `history`.

## Tech stack

- Go 1.26.4 (`go.mod`)
- SQLite via `github.com/mattn/go-sqlite3` (CGO)
- Ollama `qwen3.5:9b` (local LLM)
- whatsmeow (planned WhatsApp transport)
- DCL API `dallascricket.org:3000` (tool data source)

## Prerequisites

- Go 1.26+
- C toolchain (`clang`/`gcc`) — required for `go-sqlite3`
- Ollama at `http://localhost:11434` with `qwen3.5:9b` (LLM path only)

## Setup

```bash
cd risers-bot
go mod tidy
CGO_ENABLED=1 go build ./...
```

Build the binary:

```bash
CGO_ENABLED=1 go build -o risers-bot ./cmd/risers-bot
```

Run checks:

```bash
go vet ./...
gofmt -l .
go test ./...
```

> Binaries live under `cmd/<name>/`. Run `go build ./...` from the repo root,
> not bare `go build`.

## Configuration

| Env var | Default | Description |
|---|---|---|
| `DCL_TEAM_ID` | `88` | DCL team ID (Risers). Other teams set their own ID |
| `DCL_BASE_URL` | `https://dallascricket.org:3000` | DCL API base URL |

Runtime data (`*.db`, `/data/`) is git-ignored.

## References

- Ollama Chat API — https://docs.ollama.com/api/chat
- qwen3.5:9b — https://ollama.com/library/qwen3.5:9b
- DCL API base — https://dallascricket.org:3000/api/*
