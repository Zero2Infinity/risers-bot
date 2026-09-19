// This file owns the player tools: search_players (name → user_id),
// get_player_stats (career batting + bowling), and
// get_player_stats_filtered (one player's games + totals for one tournament).
//
// Verified live:
//   - GET /api/getplayerlistbysearch → 200, {"playerList": [...7150...]}.
//     The server IGNORES every query param tried (q/search/name/keyword/
//     query) and always returns all players (~860KB). So search_players
//     MUST filter client-side by name substring and cap results — a full
//     pass-through would instantly blow the 4K window. This is the one
//     tool where v1 filters instead of passing through.
//   - GET /api/getplayerstatistics/7211 → 200,
//     {"status", "success", "playerStatData": {"batting": {...},
//     "bowling": {...}}} (~6KB). Passed through untouched.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"risers-bot/internal/llm"
)

// maxSearchResults caps search_players output. 20 id+name rows fit the
// window; the model asks the user to be more specific on overflow.
const maxSearchResults = 20

// playerListResponse mirrors GET /api/getplayerlistbysearch:
// {"status", "success", "playerList": [{id, full_name, ...}]}.
type playerListResponse struct {
	PlayerList []playerEntry `json:"playerList"`
}

// playerEntry carries only the fields search_players returns. Full rows
// also have profile_photo_path; add it when needed.
type playerEntry struct {
	ID       int    `json:"id"`
	FullName string `json:"full_name"`
}

// SearchPlayersTool is the registry entry for search_players. Register it
// in cmd via reg.Register(SearchPlayersTool).
var SearchPlayersTool = ToolDef{
	Tool: llm.Tool{
		Name:        "search_players",
		Description: "Search DCL players by name. Returns up to 20 matching {id, full_name} rows. Use the id as user_id in get_player_stats.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"description": "Player name or substring, e.g. \"Gautham\"",
				},
			},
			"required": []string{"query"},
		},
	},
	Execute: executeSearchPlayers,
}

// executeSearchPlayers fetches the full player list, filters by
// case-insensitive substring on full_name, and returns up to
// maxSearchResults {id, full_name} rows as JSON.
func executeSearchPlayers(ctx context.Context, client *DCLClient, args map[string]any) (string, error) {
	qRaw, ok := args["query"].(string)
	if !ok {
		return "", fmt.Errorf("search_players: query missing (want string)")
	}
	q := strings.TrimSpace(qRaw)
	if q == "" {
		return "", fmt.Errorf("search_players: query missing (want string)")
	}

	var payload playerListResponse
	// no query param - the server ignores it; filter locally
	if err := client.GetJSON(ctx, "/api/getplayerlistbysearch", &payload); err != nil {
		return "", fmt.Errorf("search_players: %w", err)
	}
	matches := []playerEntry{}
	for _, p := range payload.PlayerList {
		if strings.Contains(strings.ToLower(p.FullName), strings.ToLower(q)) {
			matches = append(matches, playerEntry{ID: p.ID, FullName: p.FullName})
		}
		if len(matches) >= maxSearchResults {
			break
		}
	}

	out, err := json.Marshal(matches)
	if err != nil {
		return "", fmt.Errorf("search_players marshal: %w", err)
	}

	return string(out), nil
}

// PlayerStatsTool is the registry entry for get_player_stats. Register it
// in cmd via reg.Register(PlayerStatsTool).
var PlayerStatsTool = ToolDef{
	Tool: llm.Tool{
		Name:        "get_player_stats",
		Description: "Get career batting and bowling statistics for a DCL player, per game grouped by format. Rows carry tournament_id numbers. When the user names a tournament, resolve its id with get_tournaments first, then list ONLY rows with that tournament_id. Get the user_id from search_players first.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user_id": map[string]any{
					"description": "Player id number from search_players",
				},
			},
			"required": []string{"user_id"},
		},
	},
	Execute: executeGetPlayerStats,
}

// playerStatsResponse mirrors GET /api/getplayerstatistics/{id}:
// {"status", "success", "playerStatData": {...}}. playerStatData is an
// object when stats exist, or [] when the player has none recorded.
type playerStatsResponse struct {
	PlayerStatData json.RawMessage `json:"playerStatData"`
}

// playerStatSections is the object form of playerStatData: per-section
// maps keyed by match_type id ("1", ...). Either section may itself be []
// when empty — hence RawMessage with tolerant decode, not typed maps.
type playerStatSections struct {
	Batting json.RawMessage `json:"batting"`
	Bowling json.RawMessage `json:"bowling"`
}

// battingRow carries the fields the stats slim needs. Full rows have ~24
// keys (timestamps, internal ids, milestone counters).
type battingRow struct {
	MatchID      int       `json:"match_id"`
	TournamentID int       `json:"tournament_id"`
	Runs         int       `json:"runs"`
	Balls        int       `json:"balls"`
	Fours        int       `json:"fours"`
	Sixes        int       `json:"sixes"`
	StrikeRate   flexFloat `json:"strike_rate"`
	NotOut       int       `json:"not_out"`
}

// bowlingRow carries the fields the stats slim needs. overs/economy are
// flexFloat: the API mixes "0.0" and 0 across rows.
type bowlingRow struct {
	MatchID      int       `json:"match_id"`
	TournamentID int       `json:"tournament_id"`
	Overs        flexFloat `json:"overs"`
	Maidens      int       `json:"maidens"`
	Runs         int       `json:"runs"`
	Wickets      int       `json:"wickets"`
	Economy      flexFloat `json:"economy"`
}

// playerStatsSummary is the slim observation the model sees.
type playerStatsSummary struct {
	Batting []battingSummary `json:"batting"`
	Bowling []bowlingSummary `json:"bowling"`
}

type battingSummary struct {
	MatchID      int     `json:"match_id"`
	TournamentID int     `json:"tournament_id"`
	Format       string  `json:"format"`
	Runs         int     `json:"runs"`
	Balls        int     `json:"balls"`
	Fours        int     `json:"fours"`
	Sixes        int     `json:"sixes"`
	StrikeRate   float64 `json:"strike_rate"`
	Out          string  `json:"out"`
}

type bowlingSummary struct {
	MatchID      int     `json:"match_id"`
	TournamentID int     `json:"tournament_id"`
	Format       string  `json:"format"`
	Overs        float64 `json:"overs"`
	Maidens      int     `json:"maidens"`
	Runs         int     `json:"runs"`
	Wickets      int     `json:"wickets"`
	Economy      float64 `json:"economy"`
}

// executeGetPlayerStats fetches /api/getplayerstatistics/{id} and returns
// slim per-game batting/bowling rows as a JSON string. A player with no
// recorded stats yields empty lists so the model can say so.
func executeGetPlayerStats(ctx context.Context, client *DCLClient, args map[string]any) (string, error) {
	id, err := toMatchID(args["user_id"])
	if err != nil {
		return "", fmt.Errorf("get_player_stats: %w", err)
	}

	var payload playerStatsResponse
	path := fmt.Sprintf("/api/getplayerstatistics/%d", id)
	if err := client.GetJSON(ctx, path, &payload); err != nil {
		return "", fmt.Errorf("get_player_stats: %w", err)
	}

	slim := playerStatsSummary{
		Batting: []battingSummary{},
		Bowling: []bowlingSummary{},
	}
	var secs playerStatSections
	if err := json.Unmarshal(payload.PlayerStatData, &secs); err == nil {
		slim.Batting = slimBatting(secs.Batting)
		slim.Bowling = slimBowling(secs.Bowling)
	}

	out, err := json.Marshal(slim)
	if err != nil {
		return "", fmt.Errorf("get_player_stats marshal: %w", err)
	}
	return string(out), nil
}

// slimBatting flattens the {"format": [rows]} map into summary rows,
// tagging each with its format key. A [] (or unparseable) section yields
// no rows — never an error, since one bad section must not fail the tool.
func slimBatting(raw json.RawMessage) []battingSummary {
	var byFormat map[string][]battingRow
	if err := json.Unmarshal(raw, &byFormat); err != nil {
		return nil
	}
	out := []battingSummary{}
	for format, rows := range byFormat {
		for _, r := range rows {
			out = append(out, battingSummary{
				MatchID:      r.MatchID,
				TournamentID: r.TournamentID,
				Format:       format,
				Runs:         r.Runs,
				Balls:        r.Balls,
				Fours:        r.Fours,
				Sixes:        r.Sixes,
				StrikeRate:   float64(r.StrikeRate),
				Out:          notOutText(r.NotOut),
			})
		}
	}
	return out
}

// slimBowling flattens the {"format": [rows]} map into summary rows.
func slimBowling(raw json.RawMessage) []bowlingSummary {
	var byFormat map[string][]bowlingRow
	if err := json.Unmarshal(raw, &byFormat); err != nil {
		return nil
	}
	out := []bowlingSummary{}
	for format, rows := range byFormat {
		for _, r := range rows {
			out = append(out, bowlingSummary{
				MatchID:      r.MatchID,
				TournamentID: r.TournamentID,
				Format:       format,
				Overs:        float64(r.Overs),
				Maidens:      r.Maidens,
				Runs:         r.Runs,
				Wickets:      r.Wickets,
				Economy:      float64(r.Economy),
			})
		}
	}
	return out
}

// notOutText renders the not_out flag (1 = not out).
func notOutText(n int) string {
	if n != 0 {
		return "not out"
	}
	return "out"
}

// PlayerStatsFilteredTool is the registry entry for
// get_player_stats_filtered. Register it in cmd via
// reg.Register(PlayerStatsFilteredTool).
var PlayerStatsFilteredTool = ToolDef{
	Tool: llm.Tool{
		Name:        "get_player_stats_filtered",
		Description: "Get one player's games and totals for ONE tournament, game by game with precomputed totals. Resolve the tournament name to its id with get_tournaments first; get the user_id from search_players first. Present the batting games exactly as listed (runs off balls), then the total.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"user_id": map[string]any{
					"description": "Player id number from search_players",
				},
				"tournament_id": map[string]any{
					"description": "Tournament id number from get_tournaments",
				},
			},
			"required": []string{"user_id", "tournament_id"},
		},
	},
	Execute: executeGetPlayerStatsFiltered,
}

// playerTournamentStats is the filtered observation the model sees: one
// player's rows for one tournament, numbered in match_id order with totals
// precomputed so the model copies instead of counting.
type playerTournamentStats struct {
	TournamentID int              `json:"tournament_id"`
	Batting      []tournamentBat  `json:"batting"`
	Bowling      []tournamentBowl `json:"bowling"`
	TotalRuns    int              `json:"total_runs"`
	TotalGames   int              `json:"total_games"`
	TotalWickets int              `json:"total_wickets"`
	TotalOvers   float64          `json:"total_overs"`
}

type tournamentBat struct {
	Game       int     `json:"game"`
	MatchID    int     `json:"match_id"`
	Runs       int     `json:"runs"`
	Balls      int     `json:"balls"`
	Fours      int     `json:"fours"`
	Sixes      int     `json:"sixes"`
	StrikeRate float64 `json:"strike_rate"`
	Out        string  `json:"out"`
}

type tournamentBowl struct {
	Game    int     `json:"game"`
	MatchID int     `json:"match_id"`
	Overs   float64 `json:"overs"`
	Maidens int     `json:"maidens"`
	Runs    int     `json:"runs"`
	Wickets int     `json:"wickets"`
	Economy float64 `json:"economy"`
}

// executeGetPlayerStatsFiltered fetches /api/getplayerstatistics/{id},
// keeps only rows for the requested tournament, numbers them in match_id
// order, and precomputes totals. A player with no games in the tournament
// yields empty lists with zero totals so the model can say so.
func executeGetPlayerStatsFiltered(ctx context.Context, client *DCLClient, args map[string]any) (string, error) {
	id, err := toMatchID(args["user_id"])
	if err != nil {
		return "", fmt.Errorf("get_player_stats_filtered: %w", err)
	}
	tournamentID, err := toMatchID(args["tournament_id"])
	if err != nil {
		return "", fmt.Errorf("get_player_stats_filtered: %w", err)
	}

	var payload playerStatsResponse
	path := fmt.Sprintf("/api/getplayerstatistics/%d", id)
	if err := client.GetJSON(ctx, path, &payload); err != nil {
		return "", fmt.Errorf("get_player_stats_filtered: %w", err)
	}

	slim := playerTournamentStats{
		TournamentID: tournamentID,
		Batting:      []tournamentBat{},
		Bowling:      []tournamentBowl{},
	}
	var secs playerStatSections
	if err := json.Unmarshal(payload.PlayerStatData, &secs); err == nil {
		slim.Batting, slim.TotalRuns, slim.TotalGames = filterBatting(secs.Batting, tournamentID)
		slim.Bowling, slim.TotalWickets, slim.TotalOvers = filterBowling(secs.Bowling, tournamentID)
	}

	out, err := json.Marshal(slim)
	if err != nil {
		return "", fmt.Errorf("get_player_stats_filtered marshal: %w", err)
	}
	return string(out), nil
}

// filterBatting keeps rows for one tournament, sorts by match_id for a
// stable game order, numbers them 1..N, and sums runs. It reuses
// battingRow/slim shape conventions from get_player_stats.
func filterBatting(raw json.RawMessage, tournamentID int) ([]tournamentBat, int, int) {
	var byFormat map[string][]battingRow
	if err := json.Unmarshal(raw, &byFormat); err != nil {
		return []tournamentBat{}, 0, 0
	}
	var rows []battingRow
	for _, rs := range byFormat {
		for _, r := range rs {
			if r.TournamentID == tournamentID {
				rows = append(rows, r)
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].MatchID < rows[j].MatchID })

	out := []tournamentBat{}
	total := 0
	for i, r := range rows {
		total += r.Runs
		out = append(out, tournamentBat{
			Game:       i + 1,
			MatchID:    r.MatchID,
			Runs:       r.Runs,
			Balls:      r.Balls,
			Fours:      r.Fours,
			Sixes:      r.Sixes,
			StrikeRate: float64(r.StrikeRate),
			Out:        notOutText(r.NotOut),
		})
	}
	return out, total, len(out)
}

// filterBowling keeps rows for one tournament, sorts by match_id, numbers
// them 1..N, and sums wickets/overs.
func filterBowling(raw json.RawMessage, tournamentID int) ([]tournamentBowl, int, float64) {
	var byFormat map[string][]bowlingRow
	if err := json.Unmarshal(raw, &byFormat); err != nil {
		return []tournamentBowl{}, 0, 0
	}
	var rows []bowlingRow
	for _, rs := range byFormat {
		for _, r := range rs {
			if r.TournamentID == tournamentID {
				rows = append(rows, r)
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].MatchID < rows[j].MatchID })

	out := []tournamentBowl{}
	wickets := 0
	overs := 0.0
	for i, r := range rows {
		wickets += r.Wickets
		overs += float64(r.Overs)
		out = append(out, tournamentBowl{
			Game:    i + 1,
			MatchID: r.MatchID,
			Overs:   float64(r.Overs),
			Maidens: r.Maidens,
			Runs:    r.Runs,
			Wickets: r.Wickets,
			Economy: float64(r.Economy),
		})
	}
	return out, wickets, overs
}
