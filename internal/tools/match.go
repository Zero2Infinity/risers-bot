// This file owns the get_match_scorecard tool: slim scorecard for one match
// (meta + toss + result + both innings with batters and bowlers).
//
// Verified live: GET /api/getmatchdata/5954 → 200,
// {"status", "success", "scorecard": {...}} (~70KB raw). Rosters, playing
// XIs, logos, and scoring metadata stay out — the slim observation (~3KB)
// keeps the model narrating instead of schema-explaining.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"risers-bot/internal/llm"
)

// flexFloat decodes a JSON string or number as float64. The DCL API mixes
// "50.00", 0, and 0.0 for rate fields across rows of the same payload;
// all normalize to a number (0 → 0.0 naturally).
type flexFloat float64

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexFloat) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = 0
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexFloat(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	s = strings.TrimSpace(s)
	if s == "" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = flexFloat(n)
	return nil
}

// ScorecardTool is the registry entry for get_match_scorecard. Register it
// in cmd via reg.Register(ScorecardTool).
var ScorecardTool = ToolDef{
	Tool: llm.Tool{
		Name:        "get_match_scorecard",
		Description: "Get the scorecard for a DCL match: teams, toss, result, and both innings with batting and bowling figures. Returns slim rows. Present them as a short cricket summary to the fan. Get the match_id from get_schedule first.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"match_id": map[string]any{
					"description": "Match id number from get_schedule",
				},
			},
			"required": []string{"match_id"},
		},
	},
	Execute: executeGetMatchScorecard,
}

// scorecardResponse mirrors GET /api/getmatchdata/{id}: {"status",
// "success", "scorecard": {"matchDetails": {...}, rosters, playing XIs}}.
// Only matchDetails is decoded; the rest is skipped.
type scorecardResponse struct {
	Scorecard struct {
		MatchDetails matchDetails `json:"matchDetails"`
	} `json:"scorecard"`
}

// matchDetails carries the fields the scorecard slim needs. score_details
// is a double-encoded JSON string holding both innings.
type matchDetails struct {
	ID            int    `json:"id"`
	Date          string `json:"date"`
	GroundName    string `json:"ground_name"`
	Team1ID       int    `json:"team1_id"`
	Team1Name     string `json:"team1Name"`
	Team2ID       int    `json:"team2_id"`
	Team2Name     string `json:"team2Name"`
	Tournament    string `json:"tournament_name"`
	TossWinTeamID int    `json:"toss_win_team_id"`
	Decision      string `json:"decision"`
	ScoreDetails  string `json:"score_details"`
}

// scoreDetails is the decoded inner scorecard: both innings plus the
// result message.
type scoreDetails struct {
	Inning1      inning `json:"inning1"`
	Inning2      inning `json:"inning2"`
	ExtraDetails struct {
		Message string `json:"message"`
	} `json:"extraDetails"`
}

// inning is one decoded innings. Overs arrive as int or float (e.g. 18,
// 15.1); float64 decodes both and marshals back cleanly.
type inning struct {
	TeamName string      `json:"teamName"`
	Runs     int         `json:"runs"`
	Wickets  int         `json:"wickets"`
	Overs    float64     `json:"overs"`
	RunRate  flexFloat   `json:"runRate"`
	Extras   extrasRow   `json:"extra"`
	Batters  []batterRow `json:"batters"`
	Bowlers  []bowlerRow `json:"bowlers"`
}

type extrasRow struct {
	Total int `json:"total"`
}

type batterRow struct {
	Name          string    `json:"name"`
	Run           int       `json:"run"`
	Ball          int       `json:"ball"`
	Four          int       `json:"four"`
	Six           int       `json:"six"`
	StrikeRate    flexFloat `json:"strikeRate"`
	BattingStatus string    `json:"battingStatus"`
	OutType       string    `json:"outType"`
}

type bowlerRow struct {
	Name    string    `json:"name"`
	Over    float64   `json:"over"`
	Run     int       `json:"run"`
	Wicket  int       `json:"wicket"`
	Economy flexFloat `json:"economy"`
}

// scorecardSummary is the slim observation the model sees.
type scorecardSummary struct {
	MatchID    int             `json:"match_id"`
	Date       string          `json:"date"`
	Venue      string          `json:"venue"`
	Tournament string          `json:"tournament"`
	Team1      string          `json:"team1"`
	Team2      string          `json:"team2"`
	Toss       string          `json:"toss"`
	Result     string          `json:"result"`
	Innings    []inningSummary `json:"innings"`
}

type inningSummary struct {
	Team    string          `json:"team"`
	Runs    int             `json:"runs"`
	Wickets int             `json:"wickets"`
	Overs   float64         `json:"overs"`
	RunRate float64         `json:"run_rate"`
	Extras  int             `json:"extras"`
	Batters []batterSummary `json:"batters"`
	Bowlers []bowlerSummary `json:"bowlers"`
}

type batterSummary struct {
	Name       string  `json:"name"`
	Runs       int     `json:"runs"`
	Balls      int     `json:"balls"`
	Fours      int     `json:"fours"`
	Sixes      int     `json:"sixes"`
	StrikeRate float64 `json:"strike_rate"`
	Out        string  `json:"out"`
}

type bowlerSummary struct {
	Name    string  `json:"name"`
	Overs   float64 `json:"overs"`
	Runs    int     `json:"runs"`
	Wickets int     `json:"wickets"`
	Economy float64 `json:"economy"`
}

// executeGetMatchScorecard fetches /api/getmatchdata/{id} and returns a slim
// scorecard as a JSON string. An unscored match (empty score_details)
// returns meta with no innings so the model can say "not played yet".
func executeGetMatchScorecard(ctx context.Context, client *DCLClient, args map[string]any) (string, error) {
	id, err := toMatchID(args["match_id"])
	if err != nil {
		return "", fmt.Errorf("get_match_scorecard: %w", err)
	}

	var payload scorecardResponse
	path := fmt.Sprintf("/api/getmatchdata/%d", id)
	if err := client.GetJSON(ctx, path, &payload); err != nil {
		return "", fmt.Errorf("get_match_scorecard: %w", err)
	}
	md := payload.Scorecard.MatchDetails

	slim := scorecardSummary{
		MatchID:    md.ID,
		Date:       md.Date,
		Venue:      md.GroundName,
		Tournament: md.Tournament,
		Team1:      md.Team1Name,
		Team2:      md.Team2Name,
		Toss:       tossText(md),
	}

	if s := strings.TrimSpace(md.ScoreDetails); s != "" {
		var sd scoreDetails
		if err := json.Unmarshal([]byte(s), &sd); err != nil {
			return "", fmt.Errorf("get_match_scorecard details: %w", err)
		}
		slim.Result = strings.TrimSpace(sd.ExtraDetails.Message)
		for _, inn := range []inning{sd.Inning1, sd.Inning2} {
			slim.Innings = append(slim.Innings, slimInning(inn))
		}
	}

	out, err := json.Marshal(slim)
	if err != nil {
		return "", fmt.Errorf("get_match_scorecard marshal: %w", err)
	}
	return string(out), nil
}

// slimInning maps one decoded innings to its summary row.
func slimInning(inn inning) inningSummary {
	s := inningSummary{
		Team:    inn.TeamName,
		Runs:    inn.Runs,
		Wickets: inn.Wickets,
		Overs:   inn.Overs,
		RunRate: float64(inn.RunRate),
		Extras:  inn.Extras.Total,
	}
	for _, b := range inn.Batters {
		s.Batters = append(s.Batters, batterSummary{
			Name:       b.Name,
			Runs:       b.Run,
			Balls:      b.Ball,
			Fours:      b.Four,
			Sixes:      b.Six,
			StrikeRate: float64(b.StrikeRate),
			Out:        batterOut(b.BattingStatus, b.OutType),
		})
	}
	for _, b := range inn.Bowlers {
		s.Bowlers = append(s.Bowlers, bowlerSummary{
			Name:    b.Name,
			Overs:   b.Over,
			Runs:    b.Run,
			Wickets: b.Wicket,
			Economy: float64(b.Economy),
		})
	}
	return s
}

// batterOut renders how a batter got out. "Out" maps to the dismissal type;
// anything else surfaces as-is ("retired hurt") or "not out". Unknown
// statuses surface verbatim (fail-visible) rather than guessing.
func batterOut(status, outType string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "out":
		if s := strings.TrimSpace(outType); s != "" {
			return s
		}
		return "out"
	case "", "notout", "not out", "not_out", "dnb", "did not bat":
		return "not out"
	default:
		return strings.TrimSpace(status)
	}
}

// tossText renders "X won the toss and chose to bat/bowl". Empty when the
// API carries no toss info.
func tossText(md matchDetails) string {
	if md.TossWinTeamID == 0 {
		return ""
	}
	name := md.Team1Name
	if md.TossWinTeamID == md.Team2ID {
		name = md.Team2Name
	}
	verb := strings.ToLower(strings.TrimSpace(md.Decision))
	switch verb {
	case "batting":
		verb = "bat"
	case "bowling":
		verb = "bowl"
	}
	if verb == "" {
		return fmt.Sprintf("%s won the toss", name)
	}
	return fmt.Sprintf("%s won the toss and chose to %s", name, verb)
}

// toMatchID turns the model's match_id arg into a positive int. Accepts
// float64 (JSON numbers), int, or numeric strings. Same whole-positive-number
// switch as resolveTournamentID's numeric branches, minus "current".
// Keep it unexported in this file.
func toMatchID(raw any) (int, error) {
	switch v := raw.(type) {
	case nil:
		return 0, fmt.Errorf("match_id missing (want number)")
	case string:
		s := strings.TrimSpace(v)
		id, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("match_id parse %q: %w", s, err)
		}
		if id <= 0 {
			return 0, fmt.Errorf("match_id not positive: %d", id)
		}
		return id, nil
	case int:
		if v <= 0 {
			return 0, fmt.Errorf("match_id not positive: %d", v)
		}
		return v, nil
	case float64:
		id := int(v)
		if v != float64(id) || id <= 0 {
			return 0, fmt.Errorf("match_id not a positive whole number: %v", v)
		}
		return id, nil
	default:
		return 0, fmt.Errorf("match_id: unexpected type %T", raw)
	}
}
