// This file owns the get_schedule tool: fixtures/results for one team,
// optionally narrowed to one tournament. Also owns resolveTournamentID,
// shared with standings.go.
//
// Uses the team-scoped endpoint GET /api/schedules/{team_id} (verified:
// path segment scopes the team; ?teamId= overrides it; ?query= ignored).
// Returns slim per-match rows for the model to interpret. Team defaults to
// the configured team (88 = Risers) — never hardcoded in queries.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"risers-bot/internal/llm"
)

// ScheduleTool is the registry entry for get_schedule. Register it in cmd
// via reg.Register(ScheduleTool).
var ScheduleTool = ToolDef{
	Tool: llm.Tool{
		Name:        "get_schedule",
		Description: "Get match fixtures and results for a DCL team (defaults to your configured team). Optionally narrow to one tournament. Present them as a short list to the fan.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"team_id": map[string]any{
					"description": "Team id number. Omit for the configured team",
				},
				"tournament_id": map[string]any{
					"description": "Tournament id number, or \"current\" for latest active. Omit for all tournaments",
				},
			},
		},
	},
	Execute: executeGetSchedule,
}

// executeGetSchedule resolves the tournament id, fetches ALL fixture pages,
// and returns slim per-match rows as a JSON string. Slim (not pass-through):
// full fixtures carry roster JSON-strings (~2KB each); 30+ fixtures would
// flood the window and repeat the tournaments ramble. Row shape mirrors the
// proven scripts/test-prompts.sh jq slim.
func executeGetSchedule(ctx context.Context, client *DCLClient, args map[string]any) (string, error) {
	team := client.TeamID()
	if raw, ok := args["team_id"]; ok && raw != nil {
		var err error
		team, err = toMatchID(raw)
		if err != nil {
			return "", fmt.Errorf("get_schedule: %w", err)
		}
	}

	// teamId query param is mandatory — without it the API returns an
	// error envelope with no teamShedules key (verified live).
	path := fmt.Sprintf("/api/schedules/%d?teamId=%d", team, team)
	pages, err := client.GetPaginated(ctx, path, "teamShedules")
	if err != nil {
		return "", fmt.Errorf("get_schedule: %w", err)
	}

	var all []fixtureRow
	for i, p := range pages {
		var env fixtureListResponse
		if err := json.Unmarshal(p, &env); err != nil {
			return "", fmt.Errorf("get_schedule page %d: %w", i, err)
		}
		all = append(all, env.MatchFixtures...)
	}

	if raw, ok := args["tournament_id"]; ok && raw != nil {
		tid, err := resolveTournamentID(ctx, client, raw)
		if err != nil {
			return "", fmt.Errorf("get_schedule: %w", err)
		}
		kept := all[:0]
		for _, m := range all {
			if m.MstTournamentID == tid {
				kept = append(kept, m)
			}
		}
		all = kept
	}

	slim := make([]fixtureSummary, 0, len(all))
	for _, m := range all {
		slim = append(slim, fixtureSummary{
			ID:      m.ID,
			Date:    m.Date,
			Team1ID: m.Team1ID,
			Team1:   m.Team1Name,
			Team2ID: m.Team2ID,
			Team2:   m.Team2Name,
			Venue:   m.GroundName,
			Group:   m.GroupName,
			Ended:   m.IsMatchEnded,
			Result:  fixtureResult(m.ScoreDetails),
		})
	}

	out, err := json.Marshal(slim)
	if err != nil {
		return "", fmt.Errorf("get_schedule marshal: %w", err)
	}
	return string(out), nil
}

// resolveTournamentID turns the model's tournament_id arg into an int.
// Accepts float64 (JSON numbers), int, json.Number, numeric strings, or
// the string "current" (case-insensitive) for the latest active tournament.
// Shared with standings.go — keep it here, not duplicated.
func resolveTournamentID(ctx context.Context, client *DCLClient, raw any) (int, error) {
	switch v := raw.(type) {
	case nil:
		return 0, fmt.Errorf("tournament_id missing (want number or \"current\")")
	case string:
		s := strings.TrimSpace(v)
		if strings.EqualFold(s, "current") {
			return latestTournamentID(ctx, client)
		}
		id, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("tournament_id parse %q: %w", s, err)
		}
		if id <= 0 {
			return 0, fmt.Errorf("tournament_id not positive: %d", id)
		}
		return id, nil
	case int:
		if v <= 0 {
			return 0, fmt.Errorf("tournament_id int value not positive: %d", v)
		}
		return v, nil
	case float64:
		id := int(v)
		if v != float64(id) || id <= 0 {
			return 0, fmt.Errorf("tournament_id not a positive whole number: %v", v)
		}
		return id, nil
	default:
		return 0, fmt.Errorf("tournament_id: unexpected type :%T", raw)
	}
}

// tournamentListResponse mirrors the verified GET /api/gettournamentlist
// envelope: {"status", "success", "tournamentList": [...]}.
type tournamentListResponse struct {
	TournamentList []tournamentEntry `json:"tournamentList"`
}

// tournamentEntry carries the fields the resolver and the tournaments slim
// need. Full rows have ~25 keys (fees, roster limits, points config).
type tournamentEntry struct {
	ID          int    `json:"id"`
	Name        string `json:"tournament_name"`
	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
	Overs       string `json:"tournament_overs"`
	IsPublished int    `json:"is_published"`
}

// fixtureListResponse mirrors GET /api/schedules/{team}: {"status",
// "success", "teamShedules": [...]} (10/page, paginated). Same fixture
// shape as /api/getmatchlist/{id} (which uses the "matchFixtures" key).
type fixtureListResponse struct {
	MatchFixtures []fixtureRow `json:"teamShedules"`
}

// fixtureRow carries the fields the schedule slim needs. Full fixtures have
// ~55 keys including roster JSON-strings (team1Players/team2Players).
type fixtureRow struct {
	ID              int    `json:"id"`
	Date            string `json:"date"`
	StartTime       string `json:"start_time"`
	MstTournamentID int    `json:"mst_tournament_id"`
	Team1ID         int    `json:"team1_id"`
	Team1Name       string `json:"team1Name"`
	Team2ID         int    `json:"team2_id"`
	Team2Name       string `json:"team2Name"`
	GroundName      string `json:"ground_name"`
	GroupName       string `json:"group_name"`
	IsMatchEnded    int    `json:"is_match_ended"`
	ScoreDetails    string `json:"score_details"`
}

// fixtureSummary is the slim row the model sees. Team IDs stay in so the
// model can filter team 88 exactly instead of substring-matching names.
type fixtureSummary struct {
	ID      int    `json:"id"`
	Date    string `json:"date"`
	Team1ID int    `json:"team1_id"`
	Team1   string `json:"team1"`
	Team2ID int    `json:"team2_id"`
	Team2   string `json:"team2"`
	Venue   string `json:"venue"`
	Group   string `json:"group"`
	Ended   int    `json:"ended"`
	Result  string `json:"result"`
}

// fixtureResult extracts the result line ("X won by N runs") from a
// fixture's double-encoded score_details string. Returns "" when the match
// hasn't been scored yet or the payload doesn't parse — never an error,
// since one bad row must not fail the whole schedule.
func fixtureResult(scoreDetails string) string {
	var outer struct {
		ExtraDetails struct {
			Message string `json:"message"`
		} `json:"extraDetails"`
	}
	s := strings.TrimSpace(scoreDetails)
	if s == "" {
		return ""
	}
	if err := json.Unmarshal([]byte(s), &outer); err != nil {
		return ""
	}
	return outer.ExtraDetails.Message
}

// latestTournamentID fetches /api/gettournamentlist and picks the latest
// published tournament (max id among is_published == 1; falls back to max
// id overall when no flag matches).
func latestTournamentID(ctx context.Context, client *DCLClient) (int, error) {
	var payload tournamentListResponse
	if err := client.GetJSON(ctx, "/api/gettournamentlist", &payload); err != nil {
		return 0, fmt.Errorf("latest tournament: %w", err)
	}

	if len(payload.TournamentList) == 0 {
		return 0, fmt.Errorf("no tournaments found")
	}

	latest := 0
	for _, t := range payload.TournamentList {
		if t.ID > latest && t.IsPublished == 1 {
			latest = t.ID
		}
	}
	if latest == 0 {
		for _, t := range payload.TournamentList {
			if t.ID > latest {
				latest = t.ID
			}
		}
	}

	if latest == 0 {
		return 0, fmt.Errorf("no usable tournament id")
	}

	return latest, nil
}
