// Package window implements the SLIDING-WINDOW part of context budgeting.
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
//
// PSEUDOCODE:
//   if len(full) <= keepRecent → return full (and false, nothing dropped)
//   return full[len(full)-keepRecent:] (and true, we dropped the head)
func KeepRecent(full []db.Message, keepRecent int) (tail []db.Message, dropped bool) {
	// TODO(impl): slice based on len(full) vs keepRecent.
	panic("unimplemented")
}

// ApproxTokens is a rough token estimate for cheap budget guidance before a real
// tokenizer (tiktoken) is added. Rule: ~4 chars per token is a safe over-estimate
// for english prose; conservatively used to decide "too big to forward verbatim".
//
// PSEUDOCODE:
//   total = sum over messages of len(content) + len(thinking)
//   return total / 4   (integer division, ceiling added in caller if desired)
func ApproxTokens(msgs []db.Message) int {
	// TODO(impl): sum content+thinking lengths, divide by 4.
	panic("unimplemented")
}
