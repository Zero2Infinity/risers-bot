// Command risers-bot is the CLI entry for the Risers (team 88) cricket
// pundit bot. It wires persistence → history → model → agent → DCL tools,
// runs one user turn through the ReAct loop, and prints the reply.
//
// Two modes: one-shot CLI (default) and WhatsApp listener (-wa).
//
// WIRING (dependency order db ← history ← agent ← cmd, agent → tools → DCL):
//
//	cfg := tools.LoadConfig()            // RISERS_TEAM_ID=88 default
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
// session "cli"). WhatsApp mode: risers-bot -wa (needs RISERS_WA_PHONE on first
// run for pairing-code login; session per sender JID).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"risers-bot/internal/agent"
	"risers-bot/internal/db"
	"risers-bot/internal/history"
	"risers-bot/internal/llm"
	"risers-bot/internal/llm/ollama"
	"risers-bot/internal/log"
	"risers-bot/internal/tools"
	"risers-bot/internal/wa"
)

func main() {
	// NOTE: stdlib "log" is intentionally NOT imported here — risers-bot/internal/log
	// owns the process logger. -log defaults to $RISERS_LOG_LEVEL (via FromEnv),
	// so the flag wins when present and the env covers flag-less (e.g. -wa) runs.
	logLevel := flag.String("log", log.FromEnv(), "log level: info|debug")
	waMode := flag.Bool("wa", false, "run as WhatsApp listener for !risers commands")
	flag.Parse()
	log.Setup(*logLevel)
	var err error
	if *waMode {
		err = runWA(context.Background())
	} else {
		err = run(context.Background(), flag.Args())
	}
	if err != nil {
		log.L().Error("risers-bot: fatal", "err", err)
		os.Exit(1)
	}
}

// run executes one CLI turn. args is everything after the program name,
// joined as the user message. sessionID "cli", DB path from RISERS_DB or
// ./risers.db, model from RISERS_MODEL (legacy OLLAMA_MODEL still honored)
// or "qwen3.5:9b".
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

	reply, err := stack.loopFor(userText).Run(ctx, "cli", userText, stack.toolDefs)
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
			return stack.loopFor(userText).Run(ctx, sessionID, userText, stack.toolDefs)
		},
	}
	client, err := wa.Connect(ctx, bot)
	if err != nil {
		return err
	}
	log.L().Info("risers-bot: listening for !risers commands (Ctrl-C to stop)")
	client.Wait(ctx)
	return nil
}

// stack is the shared agent stack built once for either mode.
type stack struct {
	store    *db.Store
	hist     *history.History
	provider *ollama.Client
	reg      *tools.Registry
	dcl      *tools.DCLClient
	toolDefs []llm.Tool
}

// loopFor builds a ReAct loop whose executor reconciles match calls
// against userText before dispatch (see tools.ReconcileMatchCall).
// Cheap per turn; history/store/provider/registry are shared.
func (s *stack) loopFor(userText string) *agent.Loop {
	base := s.reg.BuildExecutor(s.dcl)
	exec := func(ctx context.Context, call llm.ToolCall) (string, error) {
		return base(ctx, tools.ReconcileMatchCall(ctx, s.dcl, userText, call))
	}
	return agent.New(s.store, s.hist, s.provider, exec)
}

// envFirst returns the first non-blank env value among keys, or "" when
// none is set. Canonical RISERS_* name first, legacy name second.
func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
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

	model := envFirst("RISERS_MODEL", "OLLAMA_MODEL")
	if model == "" {
		model = "qwen3.5:9b"
	}
	// 6144: 4K truncates 5783-class cards mid-table even with fresh
	// sessions (no history); 6K is the floor for full cards. 16K measured
	// at 5-8x latency.
	provider := ollama.NewClient(model, ollama.WithContextWindow(6144))

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

	return &stack{store: store, hist: hist, provider: provider, reg: reg, dcl: dcl, toolDefs: reg.ToolDefs()}, nil
}
