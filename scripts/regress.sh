#!/bin/bash
# regress.sh — regression prompt runner for risers-bot.
# Calls the compiled bot binary with a preset or ad-hoc question.
# Fresh SQLite DB per run so stale history never interferes.
#
# Usage:
#   ./scripts/regress.sh                                        # preset menu
#   ./scripts/regress.sh "Who won the last Risers game?"        # direct question
#
# Deps: go, sqlite3 CLI not required (bot manages its own DB).
# Env:  RISERS_DB override respected; RISERS_MODEL override respected.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="/tmp/risers-bot"
DB="/tmp/regress.db"
TIMEOUT_SECS=300

PROMPTS=(
  "List all Risers games played in DCL Fall 2026"
  "Who scored the most runs in the last Risers game?"
  "List Rahul Amratlal Patel runs scored per game in DCL Fall 2026"
  "List Rahul Amratlal Patel runs scored per game in DCL Summer 2026. One line per game: runs off balls."
  "Summarize match 5954: who played, toss, result, and top 3 batters. Keep it short."
  "Summarize last game for me."
  "What is the team id for the Royals?"
  "Summarize the match against Daring team in DCL Fall 2026."
  "Is there DCL Fall 2026 schedule uploaded?"
  "Is there DCL Spring 2027 schedule uploaded?"
  # BLOCKED on 5692 string-typed IDs (see scorecard.go NOTEs) — enable after
  # the int-or-string revisit:
  # "Summarize the match against Royals team in DCL Fall 2026."
)

build_if_needed() {
  # Rebuild when the binary is missing or any Go source is newer.
  if [[ ! -x "$BIN" ]] || [[ -n "$(find "$REPO_ROOT/cmd" "$REPO_ROOT/internal" -name '*.go' -newer "$BIN" 2>/dev/null)" ]]; then
    echo "(building risers-bot...)" >&2
    (cd "$REPO_ROOT" && CGO_ENABLED=1 go build -o "$BIN" ./cmd/risers-bot)
  fi
}

run_question() {
  local question="$1"
  rm -f "$DB"
  echo "Q: $question" >&2
  echo "---" >&2
  local start end
  start=$(date +%s)
  local rc
  if command -v timeout >/dev/null 2>&1; then
    RISERS_DB="$DB" timeout "$TIMEOUT_SECS" "$BIN" "$question"
    rc=$?
  else
    # macOS has no `timeout`; the bot's own HTTP timeouts still bound API calls.
    RISERS_DB="$DB" "$BIN" "$question"
    rc=$?
  fi
  end=$(date +%s)
  echo "---" >&2
  echo "($((end - start))s, exit=$rc)" >&2
  return $rc
}

pick_from_menu() {
  echo "Regression prompts:" >&2
  local i
  for i in "${!PROMPTS[@]}"; do
    printf '  %d  %s\n' "$((i + 1))" "${PROMPTS[$i]}" >&2
  done
  printf 'Enter number (1-%d) or q to quit: ' "${#PROMPTS[@]}" >&2
  local choice
  read -r choice
  [[ "$choice" == "q" ]] && exit 0
  if ! [[ "$choice" =~ ^[0-9]+$ ]] || ((choice < 1 || choice > ${#PROMPTS[@]})); then
    echo "invalid choice" >&2
    exit 1
  fi
  run_question "${PROMPTS[$((choice - 1))]}"
}

build_if_needed

if [[ $# -eq 0 ]]; then
  pick_from_menu
else
  run_question "$*"
fi
