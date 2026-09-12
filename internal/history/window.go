// Package history implements the SLIDING-WINDOW part of context budgeting.
// It is a thin, pure-Go helper (no LLM dependency) that answers: "given the full
// ordered message list for a session, which slice should actually be forwarded
// to the model on the current turn?"
//
// This is the zero-cost first mitigation for the "messages outgrow context
// window" worry: the db keeps all rows; window only truncates the payload. No
// summarization, no tokenizer, no extra LLM call — just a LIMIT-equivalent kept
// in memory.
package history

import "risers-bot/internal/db"

// KeepRecent returns the trailing keepRecent messages of full, oldest→newest.
func KeepRecent(full []db.Message, keepRecent int) (tail []db.Message, dropped bool) {
	if keepRecent <= 0 {
		return nil, len(full) > 0
	}

	if len(full) <= keepRecent {
		return full, false
	}

	return full[len(full)-keepRecent:], true
}

// ApproxTokens is a rough token estimate for cheap budget guidance before a real
// tokenizer (tiktoken) is added. Rule: ~4 chars per token is a safe over-estimate
// for english prose; conservatively used to decide "too big to forward verbatim".
func ApproxTokens(msgs []db.Message) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content) + len(m.Thinking)
	}

	return total / 4
}
