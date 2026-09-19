// This file owns the get_points_table tool: standings for one tournament.
//
// Verified live: GET /api/tournamentpointstable/34 → 200,
// {"status", "success", "pointsTable": {"Div A": {"League": [...]}, ...}}
// (~64KB raw → ~9KB slim). Reuses resolveTournamentID from schedule.go — do
// not duplicate the "current" logic here.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"risers-bot/internal/llm"
)

// PointsTableTool is the registry entry for get_points_table. Register it
// in cmd via reg.Register(PointsTableTool).
var PointsTableTool = ToolDef{
	Tool: llm.Tool{
		Name:        "get_points_table",
		Description: "Get the points table (standings) for a DCL tournament: wins, losses, points, net run rate per team per division. Risers are team_id 88 — do not confuse with Dallas Risers Cricket Club (a different team). When asked about one team, pass its team_id to get just its rows; omit team_id for the full table. Answer ONLY the asked team's row. Pass tournament_id as a number, or the string \"current\" for the latest active tournament.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tournament_id": map[string]any{
					"description": "Tournament id number, or \"current\" for latest active",
				},
				"team_id": map[string]any{
					"description": "Optional team id number to return only that team's rows (Risers are 88)",
				},
			},
			"required": []string{"tournament_id"},
		},
	},
	Execute: executeGetPointsTable,
}

// standingsResponse mirrors GET /api/tournamentpointstable/{id}:
// {"status", "success", "pointsTable": {"Div A": {"League": [...]}, ...}}.
type standingsResponse struct {
	PointsTable json.RawMessage `json:"pointsTable"`
}

// standingsRowRaw carries the fields the slim needs. Full rows have ~17
// keys (profile paths, internal ids, raw match logs). team_id stays: it
// disambiguates Risers (88) from Dallas Risers Cricket Club.
type standingsRowRaw struct {
	TeamID        int       `json:"team_id"`
	TeamName      string    `json:"team_name"`
	MatchesPlayed int       `json:"matches_played"`
	MatchesWon    int       `json:"matches_won"`
	MatchesLost   int       `json:"matches_lost"`
	MatchesTied   int       `json:"matches_tied"`
	Points        string    `json:"points"`
	NetRunRate    flexFloat `json:"net_run_rate"`
}

// standingsSummary is the slim observation the model sees.
type standingsSummary struct {
	Divisions []standingsDivision `json:"divisions"`
}

type standingsDivision struct {
	Name   string           `json:"name"`
	Rounds []standingsRound `json:"rounds"`
}

type standingsRound struct {
	Stage string         `json:"stage"`
	Teams []standingsRow `json:"teams"`
}

type standingsRow struct {
	TeamID int     `json:"team_id"`
	Team   string  `json:"team"`
	Played int     `json:"played"`
	Won    int     `json:"won"`
	Lost   int     `json:"lost"`
	Tied   int     `json:"tied"`
	Points string  `json:"points"`
	NRR    float64 `json:"nrr"`
}

// stageOrder fixes round order (League before Semi Final before Final);
// unknown stages sort after, alphabetically.
var stageOrder = map[string]int{"League": 0, "Semi Final": 1, "Final": 2}

// executeGetPointsTable resolves the tournament id, fetches the points
// table, and returns slim division/round/team rows as a JSON string. An
// optional team_id arg narrows the table to one team's rows.
func executeGetPointsTable(ctx context.Context, client *DCLClient, args map[string]any) (string, error) {
	id, err := resolveTournamentID(ctx, client, args["tournament_id"])
	if err != nil {
		return "", fmt.Errorf("get_points_table: %w", err)
	}
	teamID := 0
	if v, ok := args["team_id"]; ok && v != nil {
		teamID, err = toMatchID(v)
		if err != nil {
			return "", fmt.Errorf("get_points_table: %w", err)
		}
	}

	var payload standingsResponse
	path := fmt.Sprintf("/api/tournamentpointstable/%d", id)
	if err := client.GetJSON(ctx, path, &payload); err != nil {
		return "", fmt.Errorf("get_points_table: %w", err)
	}

	slim := standingsSummary{Divisions: []standingsDivision{}}
	var table map[string]map[string][]standingsRowRaw
	if err := json.Unmarshal(payload.PointsTable, &table); err == nil {
		slim.Divisions = slimStandings(table, teamID)
	}

	out, err := json.Marshal(slim)
	if err != nil {
		return "", fmt.Errorf("get_points_table marshal: %w", err)
	}

	return string(out), nil
}

// slimStandings flattens the {division: {stage: [rows]}} map into summary
// divisions, sorted by name with stages in play order. A nonzero teamID
// keeps only that team's rows (empty divisions/rounds dropped). An empty
// or unparseable table yields no divisions — never an error.
func slimStandings(table map[string]map[string][]standingsRowRaw, teamID int) []standingsDivision {
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	sort.Strings(names)

	out := []standingsDivision{}
	for _, name := range names {
		rounds := table[name]
		stages := make([]string, 0, len(rounds))
		for stage := range rounds {
			stages = append(stages, stage)
		}
		sort.Slice(stages, func(i, j int) bool {
			oi, oki := stageOrder[stages[i]]
			oj, okj := stageOrder[stages[j]]
			if oki != okj {
				return oki
			}
			if oki && okj && oi != oj {
				return oi < oj
			}
			return stages[i] < stages[j]
		})

		div := standingsDivision{Name: name, Rounds: []standingsRound{}}
		for _, stage := range stages {
			round := standingsRound{Stage: stage, Teams: []standingsRow{}}
			for _, r := range rounds[stage] {
				if teamID != 0 && r.TeamID != teamID {
					continue
				}
				round.Teams = append(round.Teams, standingsRow{
					TeamID: r.TeamID,
					Team:   r.TeamName,
					Played: r.MatchesPlayed,
					Won:    r.MatchesWon,
					Lost:   r.MatchesLost,
					Tied:   r.MatchesTied,
					Points: r.Points,
					NRR:    float64(r.NetRunRate),
				})
			}
			if len(round.Teams) > 0 {
				div.Rounds = append(div.Rounds, round)
			}
		}
		if len(div.Rounds) > 0 {
			out = append(out, div)
		}
	}
	return out
}
