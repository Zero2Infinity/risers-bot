// This file owns the get_points_table tool: standings for one tournament.
//
// Verified live: GET /api/tournamentpointstable/34 → 200,
// {"status", "success", "pointsTable": {"Div A": {"League": [...]}, ...}}
// (~64KB). Returns the full payload for the model to interpret (trim later
// for the 4K window). Reuses resolveTournamentID from schedule.go — do not
// duplicate the "current" logic here.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"risers-bot/internal/llm"
)

// PointsTableTool is the registry entry for get_points_table. Register it
// in cmd via reg.Register(PointsTableTool).
var PointsTableTool = ToolDef{
	Tool: llm.Tool{
		Name:        "get_points_table",
		Description: "Get the points table (standings) for a DCL tournament: wins, losses, points, net run rate per team per division. Pass tournament_id as a number, or the string \"current\" for the latest active tournament.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tournament_id": map[string]any{
					"description": "Tournament id number, or \"current\" for latest active",
				},
			},
			"required": []string{"tournament_id"},
		},
	},
	Execute: executeGetPointsTable,
}

// executeGetPointsTable resolves the tournament id, fetches the points
// table, and returns it as a JSON string. v1 passes through untouched.
func executeGetPointsTable(ctx context.Context, client *DCLClient, args map[string]any) (string, error) {
	id, err := resolveTournamentID(ctx, client, args["tournament_id"])
	if err != nil {
		return "", fmt.Errorf("get_points_table: %w", err)
	}

	var raw any
	path := fmt.Sprintf("/api/tournamentpointstable/%d", id)
	if err := client.GetJSON(ctx, path, &raw); err != nil {
		return "", fmt.Errorf("get_points_table: %w", err)
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("get_points_table marshal: %w", err)
	}

	return string(out), nil
}
