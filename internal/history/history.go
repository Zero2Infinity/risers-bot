// Package history owns the policy that turns persisted chat into an LLM context
// window. It is intentionally SEPARATE from internal/db: db is a storage
// primitive (insert/select), while history is a concern about context-window
// budgeting (windowing, token limits, summarization) that depends on an LLM
// provider, not on SQL. Keeping them apart lets db be imported by wa, cmd, and
// history without creating a dependency cycle (db never calls an LLM).
//
// The core idea:
//   - sessions -> messages grows unbounded (a WhatsApp group accumulates forever),
//     but a local Ollama model (e.g. qwen3.5:9b, 6.6GB) has a finite context
//     window. Sending the full history every turn would OOM or silently truncate.
//   - history.ContextFor(sessionID) returns the SLIDING WINDOW that SHOULD be
//     sent to the model: the most recent N messages, plus (if the older tail has
//     grown too big) a compacted summary of the old tail.
//   - The FULL history stays in db forever (never DELETE FROM messages); history
//     only decides what a given turn's LLM payload contains.
package history

import (
	"context"

	"risers-bot/internal/db"
)

// History is the compaction/windowing policy. It depends on a *db.Store for
// persistence and a Compactor for summarization; it owns neither (both are
// injected), so it is easy to test and to swap the summarizer.
type History struct {
	store     *db.Store
	compactor Compactor // nil → windowing only, no summarization yet
	keepRecent int      // number of trailing messages always forwarded verbatim
}

// New wires a History over a store. compactor may be nil to start with plain
// windowing (no LLM summarization), which is the smallest first slice.
func New(store *db.Store, compactor Compactor, keepRecent int) *History {
	return &History{store: store, compactor: compactor, keepRecent: keepRecent}
}

// ContextFor returns the payload to send to the LLM for one turn of a session.
// This is the public entrypoint the loop calls (cmd/risers-bot or wa handler),
// replacing a bare store.ListMessages(sessionID).
//
// PSEUDOCODE:
//   full := store.ListMessages(sessionID)            // ordered, oldest→newest
//   if len(full) <= keepRecent → return full          // under budget, no work
//   tail := last keepRecent messages                  // verbatim recent
//   head := full[:len(full)-keepRecent]               // older tail to compact
//   summary := resummarize(sessionID, head)           // cached or newly produced
//   return [summary] + tail
//
// The returned slice is the window to feed the model. It is NOT a replacement
// for the stored history.
func (h *History) ContextFor(ctx context.Context, sessionID string) ([]db.Message, error) {
	// TODO(impl): slice window + invoke h.compactor.Summarize when out of budget.
	panic("unimplemented")
}
