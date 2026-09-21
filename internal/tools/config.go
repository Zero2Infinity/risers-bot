// Package tools owns the LLM-callable DCL API tools for Risers (team 88).
//
// It sits between agent and the external API: agent → tools → DCL.
// Each tool maps one user question (schedule, scorecard, standings, player)
// to one public GET on https://dallascricket.org:3000, returning full JSON
// for the model to interpret. Trimming for the 4K window, caching, and
// live scores come later — see README/AGENTS non-goals.
//
// This file owns configuration only: which team and which base URL.
// Team identity is NEVER hardcoded in tool files — they read Config.TeamID.
package tools

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultTeamID is Risers in the DCL API (see scripts/risers_schedule.json:
// team2_id 88). Overridden by RISERS_TEAM_ID so other teams can reuse the tools.
const DefaultTeamID = 88

// DefaultBaseURL is the public DCL API. Overridden by RISERS_DCL_BASE_URL.
const DefaultBaseURL = "https://dallascricket.org:3000"

// Env var names. Canonical names carry the RISERS_ prefix so every knob
// matches (RISERS_DB, RISERS_LOG_LEVEL, ...). Keep these stable — README
// documents them.
const (
	EnvTeamID  = "RISERS_TEAM_ID"
	EnvBaseURL = "RISERS_DCL_BASE_URL"
)

// Legacy env var names, honored as a fallback when the canonical RISERS_*
// name is unset, so existing setups keep working.
const (
	LegacyEnvTeamID  = "DCL_TEAM_ID"
	LegacyEnvBaseURL = "DCL_BASE_URL"
)

// Config selects the team and API endpoint for all tools in this package.
type Config struct {
	// TeamID is the DCL team id (88 = Risers). Never hardcode 88 elsewhere.
	TeamID int
	// BaseURL is the DCL API root, e.g. https://dallascricket.org:3000.
	BaseURL string
}

// envFirst returns the first non-blank env value among keys, or "" when
// none is set. Callers pass the canonical RISERS_* name first and the
// legacy name second, so the new name wins and the old one still works.
func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// LoadConfig builds a Config from the environment with Risers defaults.
func LoadConfig() Config {
	team := DefaultTeamID
	if v := envFirst(EnvTeamID, LegacyEnvTeamID); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			fmt.Printf("Unable override Team id: %s", v)
		} else {
			team = n
		}
	}

	base := DefaultBaseURL
	if v := envFirst(EnvBaseURL, LegacyEnvBaseURL); v != "" {
		base = strings.TrimRight(v, "/")
	}

	return Config{TeamID: team, BaseURL: base}
}
