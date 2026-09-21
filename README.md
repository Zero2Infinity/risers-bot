# Risers Bot

Your WhatsApp cricket pundit for **Risers (DCL team 88)** — local LLM grounded in real DCL data for accurate answers.

## Design

```mermaid
flowchart LR
    fan(["Fan<br/>WhatsApp / CLI"]) --> wa
    subgraph bot["risers-bot"]
        direction TB
        wa["WA<br/>!risers gate, session per sender"]
        react["ReAct agent<br/>reason-act-observe, 10 steps max"]
        llmc["LLM<br/>chat, thinking, tool calls"]
        hist["History<br/>recent window + summary"]
        dbc["DB<br/>SQLite sessions + messages"]
        tools["Tools<br/>9 DCL helpers, team 88"]
        logc["Log<br/>shared stderr logger"]
        wa --> react
        react --> hist
        hist --> dbc
        react --> llmc
        react --> tools
        react -.-> logc
        llmc -.-> logc
    end
    llmc --> ollama[("Ollama<br/>local qwen3.5:9b")]
    tools --> dcl[("DCL API<br/>schedule, scorecards, standings, players")]
    classDef person fill:#08427b,color:#fff,stroke:#052e56;
    classDef ext fill:#999,color:#fff,stroke:#6b6b6b;
    class fan person;
    class ollama,dcl ext;
```

Dependency order: `db ← history ← agent ← cmd/wa`, `agent → tools → DCL API`.

## UserPrompt - try out!

CLI: `risers-bot "<question>"` — WhatsApp: `!risers <question>`

- `List all Risers games played in DCL Fall 2026`
- `Is there DCL Fall 2026 schedule uploaded?`
- `Summarize last game for me.`
- `Summarize the match against Daring team in DCL Fall 2026.`
- `Summarize match 5954: who played, toss, result, and top 3 batters. Keep it short.`
- `Who scored the most runs in the last Risers game?`
- `What is the team id for the Royals?`
- `List Rahul Amratlal Patel runs scored per game in DCL Fall 2026`
- `List Rahul Amratlal Patel runs scored per game in DCL Summer 2026. One line per game: runs off balls.`
- `Is there DCL Spring 2027 schedule uploaded?`

More in `scripts/regress.sh` — each run uses a fresh DB so tries never pollute each other.

## Features

- [x] Answer Risers questions on WhatsApp via `!risers`
- [x] Answer one-off questions from the command line
- [x] Keep a separate chat history per user in SQLite
- [x] Trim history to fit the model's context window
- [x] Summarize very old chats instead of dropping them
- [x] Chat via local Ollama model
- [x] Run a ReAct loop that calls tools then answers
- [x] Look up DCL data with tools:
  - `get_tournaments` — list all tournaments
  - `get_schedule` — fixtures and results for a team
  - `get_match_scorecard` — full scorecard with every batter and bowler
  - `get_points_table` — standings with wins, points, and net run rate
  - `search_players` — find a player id by name
  - `get_player_stats` — career batting and bowling per game
  - `get_player_stats_filtered` — one player's games and totals for one tournament
  - `summarize_match` — pundit summary from the Risers perspective
  - `find_opponent` — find a team id by partial name
- [ ] Auto-summarize old history on every turn

Base URL: `https://dallascricket.org:3000` (public `GET`, no auth).
Default team: Risers, `DCL_TEAM_ID=88`.

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
