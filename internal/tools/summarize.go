// This file owns the summarize_match tool: a Risers-centric pundit summary
// for one match (complete slim scorecard + computed insights). The model
// writes the creative response; the code only fetches, completes the card,
// and precomputes talking points.
//
// It fetches the same /api/getmatchdata/{id} endpoint as get_match_scorecard
// and reuses its decode/slim types (scorecardResponse, scorecardSummary,
// inningSummary, batterOut, tossText, toMatchID) plus the shared completeCard
// reconstruction — this file adds the analysis layer, not another decoder.
//
// Risers identification is by team ID (Config.TeamID, default 88), matched
// against the fixture's team1_id/team2_id — never by name substring, since
// "Risers" collides with "Dallas Risers Cricket Club".
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"risers-bot/internal/llm"
)

// SummarizeMatchTool is the registry entry for summarize_match. Register it
// in cmd via reg.Register(SummarizeMatchTool).
var SummarizeMatchTool = ToolDef{
	Tool: llm.Tool{
		Name:        "summarize_match",
		Description: "Summarize a DCL match as a cricket pundit would, from Risers (team 88) perspective: match overview, Risers batting/bowling highlights, key moments, and Risers areas to improve. Returns the complete slim scorecard plus precomputed insights — present them in the pundit's voice, do not invent figures. Pass match_id as a number, or the string \"last\" for the team's most recent completed game.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"match_id": map[string]any{
					"description": "Match id number (preferred when known from find_opponent), \"last\" for the most recent completed game, or an opponent team name (e.g. \"Phoenix\") — if several teams match, the tool lists them so you can ask the user which one",
				},
			},
			"required": []string{"match_id"},
		},
	},
	Execute: executeSummarizeMatch,
}

// matchInsights carries precomputed pundit talking points, all from Risers'
// perspective. The model presents them; it does not compute them.
type matchInsights struct {
	RisersBatSummary  string   `json:"risers_bat_summary"`
	RisersBatted      string   `json:"risers_batted"` // "first" or "second" — removes chase ambiguity
	ImpactKnock       string   `json:"impact_knock"`
	TopScorer         string   `json:"top_scorer"`
	AnchorNote        string   `json:"anchor_note,omitempty"`
	Collapse          string   `json:"collapse,omitempty"`
	PartnershipNote   string   `json:"partnership_note,omitempty"`
	RisersBowlSummary string   `json:"risers_bowl_summary"`
	BestBowler        string   `json:"best_bowler"`
	TightSpell        string   `json:"tight_spell,omitempty"`
	ExpensiveNote     string   `json:"expensive_note,omitempty"`
	ResultSummary     string   `json:"result_summary"`
	ChaseNote         string   `json:"chase_note,omitempty"`
	DefendNote        string   `json:"defend_note,omitempty"`
	ExtrasNote        string   `json:"extras_note,omitempty"`
	Improve           []string `json:"improve"`
}

// executeSummarizeMatch fetches /api/getmatchdata/{id}, completes both cards
// via the shared slim path, identifies the Risers innings by team ID, and
// computes pundit insights. An unscored match returns meta with no innings
// or insights.
func executeSummarizeMatch(ctx context.Context, client *DCLClient, args map[string]any) (string, error) {
	id, ambiguous, err := resolveMatchID(ctx, client, args["match_id"])
	if err != nil {
		return "", fmt.Errorf("summarize_match: %w", err)
	}
	if ambiguous != "" {
		return ambiguous, nil
	}

	var payload scorecardResponse
	path := fmt.Sprintf("/api/getmatchdata/%d", id)
	if err := client.GetJSON(ctx, path, &payload); err != nil {
		return "", fmt.Errorf("summarize_match: %w", err)
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
			return "", fmt.Errorf("summarize_match details: %w", err)
		}
		slim.Result = strings.TrimSpace(sd.ExtraDetails.Message)
		inn1 := slimInning(sd.Inning1)
		completeCard(ctx, client, &inn1, sd.Inning1)
		inn2 := slimInning(sd.Inning2)
		completeCard(ctx, client, &inn2, sd.Inning2)
		slim.Innings = []inningSummary{inn1, inn2}
		tagBowlingSides(&slim)

		risersID := client.TeamID()
		risers, opp, risersFirst := inn1, inn2, true
		switch {
		case inningTeamID(sd.Inning1.TeamName, md) == risersID && risersID != 0:
			risers, opp, risersFirst = inn1, inn2, true
		case inningTeamID(sd.Inning2.TeamName, md) == risersID && risersID != 0:
			risers, opp, risersFirst = inn2, inn1, false
		default:
			// Risers not in this match — analyze first innings side so the
			// tool still returns something useful.
			risers, opp, risersFirst = inn1, inn2, true
		}
		ins := computeInsights(risers, opp, risersFirst, slim.Result)
		slim.Insights = &ins
	}

	out, err := json.Marshal(slim)
	if err != nil {
		return "", fmt.Errorf("summarize_match marshal: %w", err)
	}
	return string(out), nil
}

// inningTeamID maps an innings to its fixture team ID by exact name match
// against the two fixture sides. Falls back to 0 (unknown) rather than
// guessing — the caller treats 0 as "not Risers".
func inningTeamID(innTeamName string, md matchDetails) int {
	switch innTeamName {
	case md.Team1Name:
		return md.Team1ID
	case md.Team2Name:
		return md.Team2ID
	default:
		return 0
	}
}

// computeInsights derives pundit talking points from the two slim innings.
// risersFirst is true when Risers batted first. All strings are present-tense
// fragments the model weaves into its report; empty means "nothing notable".
func computeInsights(risers, opp inningSummary, risersFirst bool, result string) matchInsights {
	ins := matchInsights{Improve: []string{}}
	ins.ResultSummary = result
	if risersFirst {
		ins.RisersBatted = "first"
	} else {
		ins.RisersBatted = "second"
	}

	// Risers batting.
	ins.RisersBatSummary = fmt.Sprintf("Risers scored %d/%d in %.1f overs", risers.Runs, risers.Wickets, risers.Overs)
	ins.TopScorer, ins.ImpactKnock = topBatInsights(risers.Batters)
	if note := anchorNote(risers, result); note != "" {
		ins.AnchorNote = note
	}
	if note := collapseNote(risers.Batters); note != "" {
		ins.Collapse = note
		ins.Improve = append(ins.Improve, "middle-order fragility: "+note)
	}
	if note := partnershipNote(risers); note != "" {
		ins.PartnershipNote = note
	}

	// Risers bowling. Note: an innings' Bowlers are the side that bowled AT
	// the batting team, so Risers bowling figures live in opp.Bowlers.
	ins.RisersBowlSummary = fmt.Sprintf("Risers held %s to %d/%d in %.1f overs", opp.Team, opp.Runs, opp.Wickets, opp.Overs)
	ins.BestBowler = bestBowlerNote(opp.Bowlers)
	for _, b := range opp.Bowlers {
		if b.Wickets >= 2 && b.Economy < 4.0 {
			ins.TightSpell = fmt.Sprintf("%s %d/%d (econ %.2f) — strangled the scoring", b.Name, b.Wickets, b.Runs, b.Economy)
		}
		if b.Overs >= 2 && b.Economy > 9.0 {
			ins.ExpensiveNote = fmt.Sprintf("%s went for %d runs in %.1f overs (economy rate %.2f)", b.Name, b.Runs, b.Overs, b.Economy)
			ins.Improve = append(ins.Improve, "bowling economy: "+ins.ExpensiveNote)
		}
	}

	// Match flow from Risers' view.
	won := strings.Contains(strings.ToLower(result), "risers")
	if !risersFirst && won {
		inHand := 10 - risers.Wickets
		ins.ChaseNote = fmt.Sprintf("Risers chased %d down with %d wickets in hand", opp.Runs+1, inHand)
	}
	if risersFirst && won {
		ins.DefendNote = fmt.Sprintf("Risers defended %d by %d runs", risers.Runs, risers.Runs-opp.Runs)
	}

	// Discipline: extras conceded by Risers bowlers (extras in opp innings).
	// Names Risers explicitly — the model swapped sides on match 5783,
	// crediting our 18 conceded extras to Phoenix.
	if opp.Runs > 0 && float64(opp.Extras)/float64(opp.Runs) > 0.10 {
		ins.ExtrasNote = fmt.Sprintf("Risers conceded %d extras — over 10%% of %s's total", opp.Extras, opp.Team)
		ins.Improve = append(ins.Improve, "extras discipline: "+ins.ExtrasNote)
	}
	// Top-order intent in a chase: two or more set batters striking under
	// 120 suggests the chase could have been more assertive.
	if !risersFirst {
		slow := 0
		for _, b := range risers.Batters {
			if b.Balls >= 10 && b.StrikeRate < 120 {
				slow++
			}
		}
		if slow >= 2 {
			ins.Improve = append(ins.Improve, "top-order intent: multiple set batters struck at a strike rate under 120 in the chase")
		}
	}
	return ins
}

// topBatInsights returns the top-scorer line and the impact-knock line
// (highest SR among 10+ run batters). Empty strings when no batters.
func topBatInsights(batters []batterSummary) (top, impact string) {
	if len(batters) == 0 {
		return "", ""
	}
	best := batters[0]
	for _, b := range batters[1:] {
		if b.Runs > best.Runs {
			best = b
		}
	}
	top = fmt.Sprintf("%s top-scored with %d off %d", best.Name, best.Runs, best.Balls)
	imp := batterSummary{}
	for _, b := range batters {
		if b.Runs >= 10 && (imp.Name == "" || b.StrikeRate > imp.StrikeRate) {
			imp = b
		}
	}
	if imp.Name != "" {
		impact = fmt.Sprintf("%s %d off %d (SR %.1f) — the innings' spark", imp.Name, imp.Runs, imp.Balls, imp.StrikeRate)
	}
	return top, impact
}

// anchorNote credits a not-out batter guiding a successful chase.
func anchorNote(risers inningSummary, result string) string {
	if !strings.Contains(strings.ToLower(result), "risers") {
		return ""
	}
	for _, b := range risers.Batters {
		if b.Out == "not out" && b.Runs >= 15 {
			return fmt.Sprintf("%s %d* off %d anchored the chase", b.Name, b.Runs, b.Balls)
		}
	}
	return ""
}

// collapseNote flags 3+ consecutive single-digit dismissals in batting
// order as a middle-order wobble proxy (no fall-of-wicket data in the API).
func collapseNote(batters []batterSummary) string {
	run := 0
	total := 0
	for _, b := range batters {
		if b.Out != "not out" && b.Runs < 10 {
			run++
			total += b.Runs
		} else {
			run = 0
			total = 0
		}
		if run >= 3 {
			return fmt.Sprintf("%d wickets fell for %d runs among the top order", run, total)
		}
	}
	return ""
}

// partnershipNote credits the top two when they carried 40%+ of the total.
func partnershipNote(risers inningSummary) string {
	if len(risers.Batters) < 2 || risers.Runs == 0 {
		return ""
	}
	top := append([]batterSummary{}, risers.Batters...)
	sort.Slice(top, func(i, j int) bool { return top[i].Runs > top[j].Runs })
	combined := top[0].Runs + top[1].Runs
	if float64(combined)/float64(risers.Runs) >= 0.40 {
		return fmt.Sprintf("%s and %s combined for %d of the team's %d", top[0].Name, top[1].Name, combined, risers.Runs)
	}
	return ""
}

// bestBowlerNote credits the most wickets, tiebreak by economy.
func bestBowlerNote(bowlers []bowlerSummary) string {
	if len(bowlers) == 0 {
		return ""
	}
	best := bowlers[0]
	for _, b := range bowlers[1:] {
		if b.Wickets > best.Wickets || (b.Wickets == best.Wickets && b.Economy < best.Economy) {
			best = b
		}
	}
	if best.Wickets == 0 {
		return "no Risers bowler took a wicket"
	}
	return fmt.Sprintf("%s led with %d/%d (econ %.2f)", best.Name, best.Wickets, best.Runs, best.Economy)
}
