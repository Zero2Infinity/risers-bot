// Package compact implements SUMMARIZATION COMPACTION for long sessions.
// When a session's older tail outgrows the window budget, this package folds
// that tail into a single short summary of key facts/bullets, so the model
// retains long-horizon context (what happened weeks ago) using only a tiny
// token cost instead of the whole history.
//
// Design constraints (per pair-programming decisions):
//   - The summary is PERSISTED back as a role='system' row in the SAME
//     messages table (via store.SaveMessage), NOT a new table. This keeps the
//     graph/schema single-table and lets ListMessages continue to order it.
//   - The FULL original rows are RETAINED in db (never deleted); only the LLM
//     payload references the summary.
//   - Compaction depends on an LLM provider (to write the summary) but does NOT
//     depend on db semantics — it just consumes []db.Message and a Compactor.
package history

import (
	"context"

	"risers-bot/internal/db"
)

// Compactor produces a short bullet summary for a chunk of history. It is the
// seam between the history policy and the Ollama provider — history only needs
// this interface, so it never imports the concrete LLM client (no cycle).
type Compactor interface {
	// Summarize condenses msgs into a few bullet points of persistent context.
	// Implementations hit POST /api/chat with a "summarize into 5 bullets"
	// system prompt for a qwen-style tool-calling model; the returned string is
	// the summary text only.
	Summarize(ctx context.Context, sessionID string, msgs []db.Message) (string, error)
}

// Summarize folds head (the older, out-of-window messages) into a summary and
// PERSISTS it as a role='system' row on the session. It returns the persisted
// summary Message so ContextFor can prepend it to the recent tail.
//
// PSEUDOCODE:
//   existingSession := store.GetOrCreateSession(sessionID, "group")   // ensure row
//   summaryText, err := compactor.Summarize(ctx, sessionID, head)
//   if err → return zero Message, wrap(err)
//   err = store.SaveMessage(sessionID, "system", summaryText, "", "")  // thinking/tool empty
//   if err → return zero Message, wrap(err)
//   // For simplicity the persisted summary is returned; a later refinement can
//   // re-read it via ListMessages to grab its id/created_at.
//   return db.Message{Role: "system", Content: summaryText, SessionID: sessionID}, nil
func Summarize(ctx context.Context, store *db.Store, compactor Compactor, sessionID string, head []db.Message) (db.Message, error) {
	// TODO(impl): orchestrates GetOrCreateSession + compactor.Summarize + SaveMessage.
	panic("unimplemented")
}
