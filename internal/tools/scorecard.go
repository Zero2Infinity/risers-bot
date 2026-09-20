// This file owns the get_match_scorecard tool: the ENTIRE scorecard for one
// match, deterministically (meta + toss + result + both full innings — every
// batter and bowler, no top-N picks, no analysis).
//
// Verified live: GET /api/getmatchdata/5954 → 200,
// {"status", "success", "scorecard": {...}} (~70KB raw). Rosters, playing
// XIs, logos, and scoring metadata stay out — the slim observation (~3KB)
// keeps the model narrating instead of schema-explaining.
//
// Batters the API omits from the card rows are reconstructed from the
// ball-by-ball events log (completeCard); anything still unaccounted for is
// flagged per-innings via GapNote instead of silently dropped.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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

// flexInt decodes a JSON string or number as int. The DCL API mixes 7864,
// "4056", and "" for bowler/batter ID fields across matches (empty string
// means "no recorded ID" → 0, which callers treat as unknown).
type flexInt int

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexInt) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = 0
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexInt(n)
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
	n, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

// ScorecardTool is the registry entry for get_match_scorecard. Register it
// in cmd via reg.Register(ScorecardTool).
var ScorecardTool = ToolDef{
	Tool: llm.Tool{
		Name:        "get_match_scorecard",
		Description: "Get the COMPLETE scorecard for a DCL match: teams, toss, result, and both innings with every batter and bowler. List EVERY batter (runs, balls, how out) and EVERY bowler (overs, runs, wickets) — never reduce it to top scorers or best bowlers. Pass match_id as a number, or the string \"last\" for the team's most recent completed game.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"match_id": map[string]any{
					"description": "Match id number, \"last\" for the most recent completed game, or an opponent team name (e.g. \"Phoenix\") — if several teams match, the tool lists them so you can ask the user which one",
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
// 15.1); float64 decodes both and marshals back cleanly. Events holds the
// ball-by-ball log keyed by ball ("0.1", ...); summarize.go replays it to
// reconstruct batters the API omits from the batters array.
type inning struct {
	TeamID   int                    `json:"teamId"`
	TeamName string                 `json:"teamName"`
	Runs     int                    `json:"runs"`
	Wickets  int                    `json:"wickets"`
	Overs    float64                `json:"overs"`
	RunRate  flexFloat              `json:"runRate"`
	Fours    int                    `json:"four"`
	Sixes    int                    `json:"six"`
	Extras   extrasRow              `json:"extra"`
	Batters  []batterRow            `json:"batters"`
	Bowlers  []bowlerRow            `json:"bowlers"`
	Events   map[string][]ballEvent `json:"events"`
}

// ballEvent is one ball from the innings events log. run mixes ints (runs),
// "W" (wicket), "wd" (wide), "rt" (retired hurt).
type ballEvent struct {
	Striker int     `json:"striker"`
	Bowler  int     `json:"bowler"`
	Run     ballRun `json:"run"`
}

// ballRun normalizes a ball's run value: numeric runs are legal deliveries;
// "W" is a legal wicket ball; anything else (wd/rt/…) is not a legal ball.
type ballRun struct {
	Runs   int
	Legal  bool
	Wicket bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (b *ballRun) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*b = ballRun{Runs: n, Legal: true}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "W" {
		*b = ballRun{Wicket: true, Legal: true}
		return nil
	}
	*b = ballRun{}
	return nil
}

type extrasRow struct {
	Total   int `json:"total"`
	Wide    int `json:"wide"`
	NoBall  int `json:"noBall"`
	Byes    int `json:"byes"`
	LB      int `json:"lb"`
	Penalty int `json:"penalty"`
}

type batterRow struct {
	ID            int       `json:"id"`
	Name          string    `json:"name"`
	BattingOrder  int       `json:"battingOrder"`
	Run           int       `json:"run"`
	Ball          int       `json:"ball"`
	Four          int       `json:"four"`
	Six           int       `json:"six"`
	StrikeRate    flexFloat `json:"strikeRate"`
	BattingStatus string    `json:"battingStatus"`
	OutType       string    `json:"outType"`
	BowlerID      flexInt   `json:"bowler"`
}

type bowlerRow struct {
	ID      flexInt   `json:"id"`
	Name    string    `json:"name"`
	Over    float64   `json:"over"`
	Maidens int       `json:"maiden"`
	Dots    int       `json:"dot"`
	Run     int       `json:"run"`
	Wicket  int       `json:"wicket"`
	Wides   int       `json:"wide"`
	NoBalls int       `json:"noBall"`
	Economy flexFloat `json:"economy"`
}

// scorecardSummary is the slim observation the model sees. summarize_match
// reuses it and fills Insights; the scorecard tool leaves Insights empty.
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
	// Insights is set by summarize_match only; a nil pointer omits it so
	// the scorecard observation stays a pure card (omitempty ignores zero
	// structs, but not nil pointers).
	Insights *matchInsights `json:"insights,omitempty"`
}

type inningSummary struct {
	Team         string          `json:"team"`
	Runs         int             `json:"runs"`
	Wickets      int             `json:"wickets"`
	Overs        float64         `json:"overs"`
	RunRate      float64         `json:"run_rate"`
	Extras       int             `json:"extras"`
	ExtrasDetail extrasBreakdown `json:"extras_detail"`
	Batters      []batterSummary `json:"batters"`
	Bowlers      []bowlerSummary `json:"bowlers"`
	// GapNote fires when listed batter runs + extras don't reach the team
	// total even after events reconstruction — the source data is short.
	GapNote string `json:"gap_note,omitempty"`
}

// extrasBreakdown splits the innings extras total by type, as the API
// reports them (wide, noBall, byes, lb, penalty).
type extrasBreakdown struct {
	Wides   int `json:"wides"`
	NoBalls int `json:"no_balls"`
	Byes    int `json:"byes"`
	LegByes int `json:"leg_byes"`
	Penalty int `json:"penalty"`
}

type batterSummary struct {
	Name       string  `json:"name"`
	Runs       int     `json:"runs"`
	Balls      int     `json:"balls"`
	Fours      int     `json:"fours"`
	Sixes      int     `json:"sixes"`
	StrikeRate float64 `json:"strike_rate"`
	Out        string  `json:"out"`
	// Order is the batting position (0 when the source omits it);
	// OutBy names the dismissing bowler when known.
	Order int    `json:"order"`
	OutBy string `json:"out_by,omitempty"`
}

type bowlerSummary struct {
	Name    string  `json:"name"`
	Team    string  `json:"team"`
	Overs   float64 `json:"overs"`
	Maidens int     `json:"maidens"`
	Dots    int     `json:"dots"`
	Runs    int     `json:"runs"`
	Wickets int     `json:"wickets"`
	Economy float64 `json:"economy"`
}

// tagBowlingSides stamps each bowler row with its team: the bowlers in one
// innings are the fielding side, i.e. the OTHER innings' team. Without this
// the model praises opposition bowlers as ours (match 5783 credited
// Phoenix's Tamilarasan Sivakumar under Risers' bowling).
func tagBowlingSides(slim *scorecardSummary) {
	if len(slim.Innings) != 2 {
		return
	}
	for i := range slim.Innings[0].Bowlers {
		slim.Innings[0].Bowlers[i].Team = slim.Innings[1].Team
	}
	for i := range slim.Innings[1].Bowlers {
		slim.Innings[1].Bowlers[i].Team = slim.Innings[0].Team
	}
}

// executeGetMatchScorecard fetches /api/getmatchdata/{id} and returns a slim
// scorecard as a JSON string. An unscored match (empty score_details)
// returns meta with no innings so the model can say "not played yet".
func executeGetMatchScorecard(ctx context.Context, client *DCLClient, args map[string]any) (string, error) {
	id, ambiguous, err := resolveMatchID(ctx, client, args["match_id"])
	if err != nil {
		return "", fmt.Errorf("get_match_scorecard: %w", err)
	}
	if ambiguous != "" {
		return ambiguous, nil
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
			s := slimInning(inn)
			completeCard(ctx, client, &s, inn)
			slim.Innings = append(slim.Innings, s)
		}
		tagBowlingSides(&slim)
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
		ExtrasDetail: extrasBreakdown{
			Wides:   inn.Extras.Wide,
			NoBalls: inn.Extras.NoBall,
			Byes:    inn.Extras.Byes,
			LegByes: inn.Extras.LB,
			Penalty: inn.Extras.Penalty,
		},
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
			Order:      b.BattingOrder,
			OutBy:      bowlerName(inn.Bowlers, int(b.BowlerID)),
		})
	}
	for _, b := range inn.Bowlers {
		s.Bowlers = append(s.Bowlers, bowlerSummary{
			Name:    b.Name,
			Overs:   b.Over,
			Maidens: b.Maidens,
			Dots:    b.Dots,
			Runs:    b.Run,
			Wickets: b.Wicket,
			Economy: float64(b.Economy),
		})
	}
	return s
}

// bowlerName resolves a bowler ID to its name within one innings' bowlers
// list. Returns "" when unmapped — the caller omits it via omitempty.
func bowlerName(bowlers []bowlerRow, id int) string {
	if id == 0 {
		return ""
	}
	for _, b := range bowlers {
		if int(b.ID) == id {
			return b.Name
		}
	}
	return ""
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

// completeCard appends batters the API omits from the card rows (replayed
// from the ball-by-ball events log, names resolved via the player directory)
// and flags any remaining runs shortfall. Shared by get_match_scorecard and
// summarize_match so both tools hand the model a complete card.
func completeCard(ctx context.Context, client *DCLClient, s *inningSummary, inn inning) {
	extra := unlistedStrikers(inn)
	if len(extra) > 0 {
		ids := make([]int, 0, len(extra))
		for _, b := range extra {
			ids = append(ids, b.ID)
		}
		names := map[int]string{}
		if fetched, err := playerNames(ctx, client, ids); err == nil {
			names = fetched
		}
		for _, b := range extra {
			b.Row.Name = names[b.ID]
			if b.Row.Name == "" {
				b.Row.Name = fmt.Sprintf("Player %d", b.ID)
			}
			s.Batters = append(s.Batters, b.Row)
		}
	}
	listed := 0
	for _, b := range s.Batters {
		listed += b.Runs
	}
	if gap := s.Runs - listed - s.Extras; gap > 0 {
		s.GapNote = fmt.Sprintf("Scorecard lists only %d of %d runs — %d runs from unlisted batters (source data incomplete)", listed+s.Extras, s.Runs, gap)
	}
}

// unlistedBatter pairs a reconstructed row with its striker ID so the
// caller can resolve the name via the player directory.
type unlistedBatter struct {
	ID  int
	Row batterSummary
}

// unlistedStrikers replays the ball-by-ball events log and tallies every
// striker ID missing from the batters array. The DCL API omits some batters
// (match 5954 hides Rohit Gupta's 21*); their runs are recoverable from the
// events even though the card rows are not.
func unlistedStrikers(inn inning) []unlistedBatter {
	known := map[int]bool{}
	for _, b := range inn.Batters {
		known[b.ID] = true
	}
	type tally struct {
		runs, balls, fours, sixes int
		out                       bool
		firstSeen                 int
	}
	tallies := map[int]*tally{}
	seq := 0
	for _, ball := range sortedBallKeys(inn.Events) {
		for _, e := range inn.Events[ball] {
			if known[e.Striker] || e.Striker == 0 {
				continue
			}
			t, ok := tallies[e.Striker]
			if !ok {
				t = &tally{firstSeen: seq}
				seq++
				tallies[e.Striker] = t
			}
			if e.Run.Wicket {
				t.out = true
			}
			if !e.Run.Legal {
				continue
			}
			t.balls++
			t.runs += e.Run.Runs
			if e.Run.Runs == 4 {
				t.fours++
			}
			if e.Run.Runs == 6 {
				t.sixes++
			}
		}
	}
	ids := make([]int, 0, len(tallies))
	for id := range tallies {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return tallies[ids[i]].firstSeen < tallies[ids[j]].firstSeen })

	out := []unlistedBatter{}
	for _, id := range ids {
		t := tallies[id]
		sr := 0.0
		if t.balls > 0 {
			sr = float64(t.runs) / float64(t.balls) * 100
		}
		status := "not out"
		if t.out {
			status = "out"
		}
		out = append(out, unlistedBatter{
			ID: id,
			Row: batterSummary{
				Runs:       t.runs,
				Balls:      t.balls,
				Fours:      t.fours,
				Sixes:      t.sixes,
				StrikeRate: sr,
				Out:        status,
			},
		})
	}
	return out
}

// sortedBallKeys orders ball keys ("0.1", … "15.1") numerically so first-seen
// order is deterministic (Go map iteration is random).
func sortedBallKeys(events map[string][]ballEvent) []string {
	keys := make([]string, 0, len(events))
	for k := range events {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		var oi, oj float64
		fmt.Sscanf(keys[i], "%f", &oi)
		fmt.Sscanf(keys[j], "%f", &oj)
		return oi < oj
	})
	return keys
}

// playerNames maps user IDs to full names via the player directory. Called
// only when unlisted batters exist — no gap, no extra fetch.
func playerNames(ctx context.Context, client *DCLClient, ids []int) (map[int]string, error) {
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var payload playerListResponse
	if err := client.GetJSON(ctx, "/api/getplayerlistbysearch", &payload); err != nil {
		return nil, err
	}
	names := map[int]string{}
	for _, p := range payload.PlayerList {
		if want[p.ID] {
			names[p.ID] = p.FullName
		}
	}
	return names, nil
}

// resolveMatchID turns the model's match_id arg into a match ID. Accepts a
// number, the string "last" (case-insensitive) for the configured team's most
// recent completed fixture, or an opponent team name ("Phoenix"). Returns the
// ambiguous JSON payload (non-empty) when several teams match — the caller
// hands it to the model so it can ask the user which team. Shared by
// get_match_scorecard and summarize_match.
func resolveMatchID(ctx context.Context, client *DCLClient, raw any) (id int, ambiguous string, err error) {
	if s, ok := raw.(string); ok && strings.EqualFold(strings.TrimSpace(s), "last") {
		id, err := latestEndedFixtureID(ctx, client)
		return id, "", err
	}
	if s, ok := raw.(string); ok {
		if id, err := toMatchID(s); err == nil {
			return id, "", nil
		}
		return resolveByOpponentName(ctx, client, strings.TrimSpace(s))
	}
	id, err = toMatchID(raw)
	return id, "", err
}

// teamChoice and resolveByOpponentName live in team.go; resolveMatchID
// delegates opponent-name queries there.

// latestEndedFixtureID picks the configured team's most recent completed
// fixture (ended == 1, max ISO date). Fixture dates are ISO "2006-01-02"
// (verified in schedule observations), so lexicographic max is latest.
func latestEndedFixtureID(ctx context.Context, client *DCLClient) (int, error) {
	rows, err := fetchFixtures(ctx, client, client.TeamID())
	if err != nil {
		return 0, fmt.Errorf("latest fixture: %w", err)
	}
	best, bestDate := 0, ""
	for _, r := range rows {
		if r.IsMatchEnded != 1 || r.Date == "" {
			continue
		}
		if r.Date > bestDate {
			bestDate, best = r.Date, r.ID
		}
	}
	if best == 0 {
		return 0, fmt.Errorf("no completed fixtures for team %d", client.TeamID())
	}
	return best, nil
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
