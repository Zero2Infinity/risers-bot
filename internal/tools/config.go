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

// DefaultBaseURL is the public DCL API. Overridden by DCL_BASE_URL.
const DefaultBaseURL = "https://dallascricket.org:3000"

// Env var names. Team identity lives under RISERS_* (matching RISERS_DB and
// RISERS_LOG_LEVEL); the DCL API endpoint and Ollama model keep their
// established DCL_BASE_URL / OLLAMA_MODEL names. Keep these stable — README
// documents them.
const (
	EnvTeamID  = "RISERS_TEAM_ID"
	EnvBaseURL = "DCL_BASE_URL"
)

// Config selects the team and API endpoint for all tools in this package.
type Config struct {
	// TeamID is the DCL team id (88 = Risers). Never hardcode 88 elsewhere.
	TeamID int
	// BaseURL is the DCL API root, e.g. https://dallascricket.org:3000.
	BaseURL string
}

// LoadConfig builds a Config from the environment with Risers defaults.
func LoadConfig() Config {
	team := DefaultTeamID
	if v := os.Getenv(EnvTeamID); v != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n <= 0 {
			fmt.Printf("Unable override Team id: %s", v)
		} else {
			team = n
		}
	}

	base := DefaultBaseURL
	if v := strings.TrimSpace(os.Getenv(EnvBaseURL)); v != "" {
		base = strings.TrimRight(v, "/")
	}

	return Config{TeamID: team, BaseURL: base}
}
