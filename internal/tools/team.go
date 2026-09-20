// This file owns opponent lookup by team name: resolveByOpponentName finds
// the configured team's most recent completed fixture against the opponent
// whose name contains the query. Moved out of scorecard.go so team-name
// resolution lives in one place; scorecard.go/summarize.go just call it via
// resolveMatchID.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

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
	rows, err := fetchFixtures(ctx, client, client.TeamID())
	if err != nil {
		return 0, "", fmt.Errorf("opponent lookup: %w", err)
	}
	mine := client.TeamID()
	q := strings.ToLower(query)
	type cand struct {
		name    string
		matchID int
		date    string
	}
	seen := map[int]*cand{}
	order := []int{}
	for _, r := range rows {
		if r.IsMatchEnded != 1 || r.Date == "" {
			continue
		}
		oppID, oppName := r.Team1ID, r.Team1Name
		if r.Team1ID == mine {
			oppID, oppName = r.Team2ID, r.Team2Name
		}
		if !strings.Contains(strings.ToLower(oppName), q) {
			continue
		}
		c, ok := seen[oppID]
		if !ok {
			c = &cand{name: oppName}
			seen[oppID] = c
			order = append(order, oppID)
		}
		if r.Date > c.date {
			c.date, c.matchID = r.Date, r.ID
		}
	}
	switch len(order) {
	case 0:
		return 0, "", fmt.Errorf("no completed matches against %q", query)
	case 1:
		return seen[order[0]].matchID, "", nil
	default:
		choices := make([]teamChoice, 0, len(order))
		for _, oppID := range order {
			choices = append(choices, teamChoice{ID: oppID, Name: seen[oppID].name, LastMatch: seen[oppID].date})
		}
		blob, err := json.Marshal(map[string]any{"status": "ambiguous", "query": query, "teams": choices})
		if err != nil {
			return 0, "", fmt.Errorf("opponent lookup marshal: %w", err)
		}
		return 0, string(blob), nil
	}
}
