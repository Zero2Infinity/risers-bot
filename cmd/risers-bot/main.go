// Command risers-bot is the CLI entry for the Risers (team 88) cricket
// pundit bot. It wires persistence → history → model → agent → DCL tools,
// runs one user turn through the ReAct loop, and prints the reply.
//
// WIRING (dependency order db ← history ← agent ← cmd, agent → tools → DCL):
//
//	cfg := tools.LoadConfig()            // DCL_TEAM_ID=88 default
//	store, err := db.Open(path)          // SQLite; defer store.Close()
//	hist := history.New(store, nil, keepRecent)
//	provider := ollama.NewClient(model, ollama.WithContextWindow(...))
//	dcl := tools.NewDCLClient(cfg)
//	reg := tools.NewRegistry()
//	reg.Register(tools.TournamentTool)
//	reg.Register(tools.ScheduleTool)
//	reg.Register(tools.ScorecardTool)
//	reg.Register(tools.PointsTableTool)
//	reg.Register(tools.SearchPlayersTool)
//	reg.Register(tools.PlayerStatsTool)
//	reg.Register(tools.PlayerStatsFilteredTool)
//	loop := agent.New(store, hist, provider, reg.BuildExecutor(dcl))
//	reply, err := loop.Run(ctx, sessionID, userText, reg.ToolDefs())
//
// Usage v1: risers-bot "who won the last Risers game?"  (single turn;
// session "cli"). WhatsApp transport (internal/wa) replaces this later.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"risers-bot/internal/agent"
	"risers-bot/internal/db"
	"risers-bot/internal/history"
	"risers-bot/internal/llm/ollama"
	"risers-bot/internal/tools"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

// run executes one CLI turn. args is everything after the program name,
// joined as the user message. sessionID "cli", DB path from RISERS_DB or
// ./risers.db, model from OLLAMA_MODEL or "qwen3.5:9b".
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: risers-bot \"<message>\"")
	}
	userText := strings.Join(args, " ")

	cfg := tools.LoadConfig()

	dbPath := strings.TrimSpace(os.Getenv("RISERS_DB"))
	if dbPath == "" {
		dbPath = "./risers.db"
	}
	store, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	hist := history.New(store, nil, 20)

	model := strings.TrimSpace(os.Getenv("OLLAMA_MODEL"))
	if model == "" {
		model = "qwen3.5:9b"
	}
	// 6144 (was 4096; 8192 also verified): a full two-innings card
	// observation (~4KB) plus tool definitions plus qwen thinking leaves no
	// generation room at 4K — match 5783 answers were cut mid-table twice.
	// 16K was measured at 5-8x latency; 6K is the leanest window that fits a
	// full card answer.
	provider := ollama.NewClient(model, ollama.WithContextWindow(6144))

	dcl := tools.NewDCLClient(cfg)
	reg := tools.NewRegistry()
	reg.Register(tools.TournamentTool)
	reg.Register(tools.ScheduleTool)
	reg.Register(tools.ScorecardTool)
	reg.Register(tools.PointsTableTool)
	reg.Register(tools.SearchPlayersTool)
	reg.Register(tools.PlayerStatsTool)
	reg.Register(tools.PlayerStatsFilteredTool)
	reg.Register(tools.SummarizeMatchTool)

	loop := agent.New(store, hist, provider, reg.BuildExecutor(dcl))
	reply, err := loop.Run(ctx, "cli", userText, reg.ToolDefs())
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	fmt.Println(reply)
	return nil
}
