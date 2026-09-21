// Package log owns the process-wide structured logger for risers-bot.
//
// WHY A SHARED PACKAGE (and not a per-file slog.New):
//
//   - One place parses the level ("info"|"debug") from the -log flag /
//     RISERS_LOG_LEVEL env, so every package (agent, ollama, wa, tools)
//     logs at the same verbosity.
//
//   - Call sites stay tiny:
//
//     log.L().Info("agent: turn start", "session_id", id)
//     log.L().Debug("agent: llm request", "iter", iter, "messages", len(msgs))
//
//   - slog is stdlib (Go 1.21+): TextHandler to stderr, zero new deps.
//     (zerolog is in go.mod only transitively via whatsmeow — not used.)
//
// LEVELS:
//   - INFO  (default): full learning payloads — turn input, history window,
//     LLM request (messages+tools) and response (content+thinking+tool_calls),
//     tool call args and full observations, each with iter/max. Verbose by
//     design for local learning; expect large stderr in WhatsApp mode.
//   - DEBUG: transport detail below the contract (ollama request/response
//     stats, per-iteration internals). Previews via Preview stay available
//     for future trimming.
//
// FORMAT ($RISERS_LOG_FORMAT, default "text"):
//   - text: slog TextHandler — scannable while tailing stderr during learning.
//   - json: slog JSONHandler — one JSON object per line (JSONL), for jq:
//     RISERS_LOG_FORMAT=json ./risers-bot "..." 2>&1 | jq -c '{msg, iter, session: .session_id}'
//     Anything else fails open to text, same as level parsing. The format
//     only changes rendering — every log.L() call site is untouched.
//
// WIRING (cmd/risers-bot/main.go, next step):
//
//	level := flag.String("log", log.FromEnv(), "log level: info|debug")
//	log.Setup(*level) // once, before buildStack()
//
// PRIVACY: INFO logs carry full thinking and full observations for learning.
// This is verbose — do not leave on in shared/long-running WhatsApp mode
// without accepting large stderr. Previews go through Preview (truncated)
// when callers want the short form.
package log

import (
	"log/slog"
	"os"
	"strings"
)

// LogLevel is the env var name ("RISERS_LOG_LEVEL") that carries the default
// log level when the -log flag is absent. cmd reads it via FromEnv so the
// level is configurable without touching the CLI invocation (handy for the
// long-running -wa mode).
const LogLevel = "RISERS_LOG_LEVEL"

// LogFormat is the env var name ("RISERS_LOG_FORMAT") that selects the
// output format: "text" (default, human-readable) or "json" (JSONL for jq).
// It is separate from -log/RISERS_LOG_LEVEL, which selects verbosity —
// level and format are orthogonal knobs.
const LogFormat = "RISERS_LOG_FORMAT"

// Setup installs the process-wide slog logger on stderr at the parsed
// level (see LogLevel) with the format from $RISERS_LOG_FORMAT ("text" by
// default, "json" for JSONL). Only "debug" (case-insensitive,
// whitespace-tolerant) enables Debug output; anything else — including
// garbage — fails open to Info so a typo can never flood WhatsApp-mode
// logs. Unknown formats fail open to text. Call once at startup, before any
// L() call site can emit. Stderr (not stdout) keeps the CLI reply on stdout
// clean for piping.
func Setup(level string) {
	lvl := slog.LevelInfo
	if strings.ToLower(strings.TrimSpace(level)) == "debug" {
		lvl = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if strings.ToLower(strings.TrimSpace(os.Getenv(LogFormat))) == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

// L returns the process-wide logger installed by Setup. It is just
// slog.Default, so it is never nil — before Setup runs it discards output,
// which keeps unit tests (that never call Setup) quiet and safe.
func L() *slog.Logger {
	return slog.Default()
}

// FromEnv returns the trimmed $RISERS_LOG_LEVEL value, or "info" when unset
// or blank. It deliberately does NOT validate: Setup owns parsing, so there
// is exactly one place that decides what a level string means.
func FromEnv() string {
	v := strings.TrimSpace(os.Getenv(LogLevel))
	if v == "" {
		return "info"
	}

	return v
}

// Preview truncates s to n bytes plus a "..." marker for log previews.
// Short strings pass through untouched. Byte- (not rune-) based: fine for
// logs, where a split multi-byte rune is a cosmetic edge. Callers must pass
// a positive n — a negative n panics at the slice (same as any s[:n]).
func Preview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
