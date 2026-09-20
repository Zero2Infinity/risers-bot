// Package wa is the WhatsApp transport for risers-bot: it listens for group
// or chat messages starting with "!risers ", runs the remainder through the
// agent ReAct loop, and replies in place.
//
// DESIGN (dependency order db ← history ← agent ← wa; wa → agent → tools):
//
//   - One agent session per sender: sessionID is the sender JID, so each
//     user gets their own persisted history via the existing db layer.
//     No new storage; wa only maps JID → sessionID.
//   - The ReAct loop is transport-agnostic: wa builds the same
//     agent.New(store, hist, provider, executor) stack as cmd and calls
//     Loop.Run(ctx, sessionID, userText, toolDefs). Only the I/O edge
//     (whatsmeow event ↔ reply send) lives here.
//   - Command gate: only messages with the "!risers " prefix are handled;
//     everything else is ignored so the bot stays silent in groups.
//
// PSEUDOCODE for one incoming message:
//
//	func (b *Bot) HandleMessage(ctx, senderJID, text) (reply string, err error)
//	  prompt, ok := ParseCommand(text)   // strip "!risers ", trim; !ok → "" (ignore)
//	  if !ok → return "", nil            // not for us
//	  if prompt == "" → return help text // bare "!risers"
//	  reply := b.loop.Run(ctx, senderJID, prompt, b.toolDefs)
//	  wrap err as "agent: %w"; caller sends reply back to the chat
//
// NEXT STEPS (each its own commit):
//
//  1. `go get go.mau.fi/whatsmeow` (+ protobuf store deps) and wire the
//     whatsmeow client: QR/pairing login, event handler → HandleMessage,
//     send reply. (Needs network + device pairing; do it with the user.)
//  2. Extract the shared stack (db/store/hist/provider/executor/toolDefs)
//     from cmd/risers-bot/main.go so cmd and wa build it once.
//  3. cmd flag to run in WhatsApp mode vs one-shot CLI mode.
//
// whatsmeow is NOT in go.mod yet — step 1 adds it. This file compiles
// dependency-free so the contract is reviewable before fetching the dep.
package wa

import (
	"context"
	"fmt"
	"strings"
)

// CommandPrefix gates which messages the bot answers. Trailing space is
// part of the prefix so "!risersfoo" does not trigger.
const CommandPrefix = "!risers "

// Bot holds the transport-agnostic pieces needed to answer one message.
// The whatsmeow client itself arrives in step 1; Loop and ToolDefs are
// interfaces here so this package never imports heavy deps directly.
type Bot struct {
	// RunTurn executes one ReAct turn: sessionID isolates per-sender
	// history (use the sender JID). Mirrors agent Loop.Run.
	RunTurn func(ctx context.Context, sessionID, userText string) (string, error)
}

// ParseCommand strips the "!risers " prefix. ok=false means the message
// is not for the bot (caller ignores it). An empty prompt with ok=true
// means a bare "!risers" — caller replies with help text.
func ParseCommand(text string) (prompt string, ok bool) {
	if !strings.HasPrefix(text, CommandPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(text, CommandPrefix)), true
}

// HelpText answers a bare "!risers" with usage.
const HelpText = `Risers bot — DCL team 88 pundit.
!risers <question> — e.g. "!risers summarize the match against Daring Bering"` + "\n"

// HandleMessage answers one incoming chat message. Returns "" with nil
// error when the message is not for the bot (caller sends nothing).
func (b *Bot) HandleMessage(ctx context.Context, senderJID, text string) (string, error) {
	prompt, ok := ParseCommand(text)
	if !ok {
		return "", nil
	}
	if prompt == "" {
		return HelpText, nil
	}
	if b.RunTurn == nil {
		return "", fmt.Errorf("wa: RunTurn not wired")
	}
	reply, err := b.RunTurn(ctx, senderJID, prompt)
	if err != nil {
		return "", fmt.Errorf("wa: agent: %w", err)
	}
	return reply, nil
}
