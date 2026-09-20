// Command risers-bot is the CLI entry for the Risers (team 88) cricket
// pundit bot. It wires persistence → history → model → agent → DCL tools,
// runs one user turn through the ReAct loop, and prints the reply.
//
// Two modes: one-shot CLI (default) and WhatsApp listener (-wa).
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
// session "cli"). WhatsApp mode: risers-bot -wa (needs WA_PHONE on first
// run for pairing-code login; session per sender JID).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"

	"risers-bot/internal/agent"
	"risers-bot/internal/db"
	"risers-bot/internal/history"
	"risers-bot/internal/llm"
	"risers-bot/internal/llm/ollama"
	"risers-bot/internal/tools"
	"risers-bot/internal/wa"
)

func main() {
	waMode := flag.Bool("wa", false, "run as WhatsApp listener for !risers commands")
	flag.Parse()
	var err error
	if *waMode {
		err = runWA(context.Background())
	} else {
		err = run(context.Background(), flag.Args())
	}
	if err != nil {
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

	stack, err := buildStack()
	if err != nil {
		return err
	}
	defer stack.store.Close()

	reply, err := stack.loop.Run(ctx, "cli", userText, stack.toolDefs)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	fmt.Println(reply)
	return nil
}

// runWA connects to WhatsApp and answers "!risers <prompt>" messages until
// interrupted (Ctrl-C). Each sender JID gets its own agent session.
func runWA(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	stack, err := buildStack()
	if err != nil {
		return err
	}
	defer stack.store.Close()

	bot := &wa.Bot{
		RunTurn: func(ctx context.Context, sessionID, userText string) (string, error) {
			return stack.loop.Run(ctx, sessionID, userText, stack.toolDefs)
		},
	}
	client, err := wa.Connect(ctx, bot)
	if err != nil {
		return err
	}
	log.Println("risers-bot: listening for !risers commands (Ctrl-C to stop)")
	client.Wait(ctx)
	return nil
}

// stack is the shared agent stack built once for either mode.
type stack struct {
	store    *db.Store
	loop     *agent.Loop
	toolDefs []llm.Tool
}

// buildStack wires persistence → history → model → agent → DCL tools.
// Shared by one-shot CLI and WhatsApp modes; see the package doc.
func buildStack() (*stack, error) {
	cfg := tools.LoadConfig()

	dbPath := strings.TrimSpace(os.Getenv("RISERS_DB"))
	if dbPath == "" {
		dbPath = "./risers.db"
	}
	store, err := db.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	hist := history.New(store, nil, 20)

	model := strings.TrimSpace(os.Getenv("OLLAMA_MODEL"))
	if model == "" {
		model = "qwen3.5:9b"
	}
	// 8192 (was 6144): multi-hop tool history (tournaments list + opponent
	// lookup + 6K full-card observation) left no generation room at 6K —
	// 5597/5401 summaries came back empty. 16K was measured at 5-8x
	// latency; re-measure if this grows again.
	provider := ollama.NewClient(model, ollama.WithContextWindow(8192))

	dcl := tools.NewDCLClient(cfg)
	reg := tools.NewRegistry()
	reg.Register(tools.TournamentTool)
	reg.Register(tools.ScheduleTool)
	reg.Register(tools.ScorecardTool)
	reg.Register(tools.FindOpponentTool)
	reg.Register(tools.PointsTableTool)
	reg.Register(tools.SearchPlayersTool)
	reg.Register(tools.PlayerStatsTool)
	reg.Register(tools.PlayerStatsFilteredTool)
	reg.Register(tools.SummarizeMatchTool)

	loop := agent.New(store, hist, provider, reg.BuildExecutor(dcl))
	return &stack{store: store, loop: loop, toolDefs: reg.ToolDefs()}, nil
}
