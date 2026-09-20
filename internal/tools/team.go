// This file owns opponent lookup by team name: FindOpponentTool is the
// LLM-callable tool, resolveByOpponentName is the internal resolver shared
// with scorecard.go/summarize.go via resolveMatchID. Both sit on
// lookupOpponent so matching logic lives in one place.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"risers-bot/internal/llm"
)

// FindOpponentTool is the registry entry for find_opponent. Register it in
// cmd via reg.Register(FindOpponentTool).
var FindOpponentTool = ToolDef{
	Tool: llm.Tool{
		Name:        "find_opponent",
		Description: "Find a DCL team by partial name (e.g. \"Royals\" matches \"Fort Worth Royals\"). Returns the team id — only needed when another tool asks for a team_id (e.g. get_points_table). For match summaries or scorecards, skip this tool and pass the team name straight to summarize_match or get_match_scorecard, which resolve it. If several teams match, ask the user which one.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"description": "Partial team name to search for (case-insensitive)",
				},
			},
			"required": []string{"query"},
		},
	},
	Execute: executeFindOpponent,
}

// executeFindOpponent searches completed fixtures for opponents matching the
// query and returns found/ambiguous JSON. One team → its id plus latest
// match; several → the list so the model can ask the user which one.
func executeFindOpponent(ctx context.Context, client *DCLClient, args map[string]any) (string, error) {
	raw, ok := args["query"]
	if !ok || raw == nil {
		return "", fmt.Errorf("find_opponent: query missing (want team name)")
	}
	s, ok := raw.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("find_opponent: query must be a non-empty string")
	}
	opps, err := lookupOpponent(ctx, client, strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("find_opponent: %w", err)
	}
	if len(opps) == 1 {
		// NOTE: no match_id here on purpose. The model substitutes wrong
		// numbers between hops (5692→5954 twice); team names route
		// deterministically through resolveMatchID, numbers do not.
		out, err := json.Marshal(map[string]any{
			"status":     "found",
			"team_id":    opps[0].TeamID,
			"name":       opps[0].TeamName,
			"last_match": opps[0].Date,
		})
		if err != nil {
			return "", fmt.Errorf("find_opponent marshal: %w", err)
		}
		return string(out), nil
	}
	choices := make([]teamChoice, 0, len(opps))
	for _, o := range opps {
		choices = append(choices, teamChoice{ID: o.TeamID, Name: o.TeamName, LastMatch: o.Date})
	}
	out, err := json.Marshal(map[string]any{"status": "ambiguous", "query": strings.TrimSpace(s), "teams": choices})
	if err != nil {
		return "", fmt.Errorf("find_opponent marshal: %w", err)
	}
	return string(out), nil
}

// oppMatch is one opponent team plus its latest completed fixture.
type oppMatch struct {
	TeamID   int
	TeamName string
	MatchID  int
	Date     string
}

// Opponent aliases oppMatch for use outside this package (the match-call
// reconciler). Fields stay exported: TeamID, TeamName, MatchID, Date.
type Opponent = oppMatch

// AllOpponents lists every opponent from completed fixtures, each with its
// latest fixture (max ISO date). Own team excluded.
func AllOpponents(ctx context.Context, client *DCLClient) ([]Opponent, error) {
	return allOpponents(ctx, client)
}

// allOpponents scans fixtures once. lookupOpponent filters its result —
// equivalent to filtering rows first, since matching depends only on the
// team name, which is fixed per team.
func allOpponents(ctx context.Context, client *DCLClient) ([]oppMatch, error) {
	rows, err := fetchFixtures(ctx, client, client.TeamID())
	if err != nil {
		return nil, fmt.Errorf("opponent lookup: %w", err)
	}
	mine := client.TeamID()
	seen := map[int]*oppMatch{}
	order := []int{}
	for _, r := range rows {
		if r.IsMatchEnded != 1 || r.Date == "" {
			continue
		}
		oppID, oppName := r.Team1ID, r.Team1Name
		if r.Team1ID == mine {
			oppID, oppName = r.Team2ID, r.Team2Name
		}
		if oppID == mine {
			continue
		}
		c, ok := seen[oppID]
		if !ok {
			c = &oppMatch{TeamID: oppID, TeamName: oppName}
			seen[oppID] = c
			order = append(order, oppID)
		}
		if r.Date > c.Date {
			c.Date, c.MatchID = r.Date, r.ID
		}
	}
	out := make([]oppMatch, 0, len(order))
	for _, oppID := range order {
		out = append(out, *seen[oppID])
	}
	return out, nil
}

// lookupOpponent lists opponent teams whose name contains query
// (case-insensitive), each with its latest completed fixture (max ISO date).
// Shared by the find_opponent tool and resolveByOpponentName.
func lookupOpponent(ctx context.Context, client *DCLClient, query string) ([]oppMatch, error) {
	all, err := allOpponents(ctx, client)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	out := make([]oppMatch, 0, len(all))
	for _, o := range all {
		if strings.Contains(strings.ToLower(o.TeamName), q) {
			out = append(out, o)
		}
	}
	return out, nil
}

// teamChoice is one candidate in an ambiguous opponent-name lookup.
type teamChoice struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	LastMatch string `json:"last_match"`
}

// resolveByOpponentName finds the configured team's most recent completed
// fixture against the opponent whose name contains query (case-insensitive).
// One match → its ID. Several teams → ambiguous JSON listing each team's
// latest match so the model can ask the user which team. None → error.
func resolveByOpponentName(ctx context.Context, client *DCLClient, query string) (int, string, error) {
	opps, err := lookupOpponent(ctx, client, query)
	if err != nil {
		return 0, "", err
	}
	switch len(opps) {
	case 0:
		return 0, "", fmt.Errorf("no completed matches against %q", query)
	case 1:
		return opps[0].MatchID, "", nil
	default:
		choices := make([]teamChoice, 0, len(opps))
		for _, o := range opps {
			choices = append(choices, teamChoice{ID: o.TeamID, Name: o.TeamName, LastMatch: o.Date})
		}
		blob, err := json.Marshal(map[string]any{"status": "ambiguous", "query": query, "teams": choices})
		if err != nil {
			return 0, "", fmt.Errorf("opponent lookup marshal: %w", err)
		}
		return 0, string(blob), nil
	}
}
