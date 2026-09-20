// This file owns match-call reconciliation: a guard between the model and
// the scorecard/summary tools that keeps an explicit team name in the user
// text from being dropped between hops.
//
// FAILURE MODE (seen 4+ times): user names an opponent ("Royals"), the
// model calls find_opponent (correct: 5692), then calls summarize_match
// with "last" or an unrelated number (5954) instead of the name. The tool
// layer cannot tell "last" is wrong — only the user text knows.
//
// RULE: if the user text names exactly one opponent and the call targets
// summarize_match/get_match_scorecard with "last" or a number that is not
// that opponent's latest match, rewrite match_id to the opponent's full
// name (names route deterministically via resolveMatchID; numbers proved
// substitutable). Name args, no-name prompts, correct numbers, and
// multi-team mentions pass through untouched.
package tools

import (
	"context"
	"strconv"
	"strings"
	"unicode"

	"risers-bot/internal/llm"
)

// fillerWords are team-name tokens too generic to identify an opponent.
// "game" is the dangerous one: "summarize last game" must not match
// "The Game Changers".
var fillerWords = map[string]bool{
	"the": true, "a": true, "an": true,
	"game": true, "games": true, "match": true,
	"team": true, "teams": true, "club": true, "cc": true, "xi": true,
	"cricket": true,
}

// opponentMentioned reports whether userText contains a distinctive token
// of oppName as a whole word (case-insensitive, min 4 letters, filler
// words skipped). Whole-word, not substring: "royals" must not match
// "The Royal Renegades".
func opponentMentioned(userText, oppName string) bool {
	split := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
	}
	inText := map[string]bool{}
	for _, w := range split(userText) {
		inText[w] = true
	}
	for _, w := range split(oppName) {
		if len(w) < 4 || fillerWords[w] {
			continue
		}
		if inText[w] {
			return true
		}
	}
	return false
}

// ReconcileMatchCall rewrites a summarize/scorecard call's match_id to the
// opponent named in userText when the model's selector disagrees. Returns
// the call unchanged when no rewrite applies. One fixtures fetch, only
// when the call carries "last" or a number.
func ReconcileMatchCall(ctx context.Context, client *DCLClient, userText string, call llm.ToolCall) llm.ToolCall {
	if call.Name != SummarizeMatchTool.Tool.Name && call.Name != ScorecardTool.Tool.Name {
		return call
	}
	raw, ok := call.Arguments["match_id"]
	if !ok || raw == nil {
		return call
	}
	needCheck := false
	var numID int
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if strings.EqualFold(s, "last") {
			needCheck = true
		} else if n, err := strconv.Atoi(s); err == nil && n > 0 {
			needCheck, numID = true, n // numeric string routes like a number
		} else {
			return call // a team name routes deterministically
		}
	case float64:
		needCheck, numID = true, int(v)
	case int:
		needCheck, numID = true, v
	default:
		return call
	}
	if !needCheck {
		return call
	}
	opps, err := AllOpponents(ctx, client)
	if err != nil {
		return call // reconciler never fails the turn; fail open
	}
	var hit *Opponent
	for i := range opps {
		if opponentMentioned(userText, opps[i].TeamName) {
			if hit != nil {
				return call // two teams named — ambiguous, leave to the model
			}
			hit = &opps[i]
		}
	}
	if hit == nil || (numID != 0 && numID == hit.MatchID) {
		return call // no team named, or the number already agrees
	}
	fixed := call
	fixed.Arguments = make(map[string]any, len(call.Arguments))
	for k, v := range call.Arguments {
		fixed.Arguments[k] = v
	}
	fixed.Arguments["match_id"] = hit.TeamName
	return fixed
}
