// This file owns the get_tournaments tool: list all DCL tournaments.
//
// No parameters. Paginates through ALL pages via GetPaginated (the API
// returns 20/page; 36 total — a single fetch misses 16) and returns slim
// {id, name, dates, overs, published} rows, newest first (page order).
// Slim observations keep the model on the question and fit the 4K window.
//
// Future optimization (verified, not wired): the server accepts ?query=
// name-substring filter on this endpoint. Useful if rows ever grow past
// window comfort; client-side is fine at 36 rows.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"risers-bot/internal/llm"
)

// TournamentTool is the registry entry for get_tournaments. Register it
// in cmd via reg.Register(TournamentTool).
var TournamentTool = ToolDef{
	Tool: llm.Tool{
		Name:        "get_tournaments",
		Description: "List all DCL tournaments. Returns slim rows: id, name, start/end dates, overs, published flag. Present them as a short list to the fan. Call this first when you need a tournament_id for schedule or standings.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	Execute: executeGetTournaments,
}

// tournamentSummary is the slim row the model sees. Full DCL rows carry
// ~25 keys (fees, roster limits, points config); none of that helps answer
// fan questions, so it stays out of the observation.
type tournamentSummary struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Overs     string `json:"overs"`
	Published int    `json:"published"`
}

// executeGetTournaments fetches ALL pages and returns slim rows as a JSON
// string. Concatenated page order is newest-first (page 0 holds the latest
// ids), so no sorting needed.
func executeGetTournaments(ctx context.Context, client *DCLClient, args map[string]any) (string, error) {
	pages, err := client.GetPaginated(ctx, "/api/gettournamentlist", "tournamentList")
	if err != nil {
		return "", fmt.Errorf("get_tournaments: %w", err)
	}

	var all []tournamentEntry
	for i, p := range pages {
		var env tournamentListResponse
		if err := json.Unmarshal(p, &env); err != nil {
			return "", fmt.Errorf("get_tournaments page %d: %w", i, err)
		}
		all = append(all, env.TournamentList...)
	}
	if len(all) == 0 {
		return "", fmt.Errorf("get_tournaments: no tournaments found")
	}

	slim := make([]tournamentSummary, 0, len(all))
	for _, t := range all {
		slim = append(slim, tournamentSummary{
			ID:        t.ID,
			Name:      t.Name,
			StartDate: t.StartDate,
			EndDate:   t.EndDate,
			Overs:     t.Overs,
			Published: t.IsPublished,
		})
	}

	out, err := json.Marshal(slim)
	if err != nil {
		return "", fmt.Errorf("get_tournaments marshal: %w", err)
	}
	return string(out), nil
}
