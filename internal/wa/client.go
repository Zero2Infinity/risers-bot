// This file owns the whatsmeow mechanics: session store, pairing, event
// routing, and reply sending. Message meaning (!risers parsing, agent turn)
// lives in bot.go — this file only moves text between WhatsApp and Bot.
//
// MVP login flow (no QR rendering — pairing code instead):
//
//  1. Open/create the device store (SQLite, WA_STORE or ./wastore.db).
//  2. Connect. If the store has no ID (first run), require WA_PHONE
//     (digits only, e.g. 15551234567) and call PairPhone; the returned
//     8-char code is printed — enter it on the phone under Linked
//     Devices → Link with phone number. Poll until logged in.
//  3. Register the event handler and block. Every incoming text message
//     goes to Bot.HandleMessage; a non-empty reply is sent back to the
//     same chat (group or DM).
//
// Text extraction covers plain + extended (quoted/replied) messages.
// Own messages (IsFromMe) are skipped so the bot never answers itself.
package wa

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3" // sqlite driver for the device store
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	walog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// envOr returns the env value or def when unset/blank.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Client wraps a whatsmeow connection with a Bot for message meaning.
//
// Self-test note: when you send a command from the same phone the bot is
// linked to, the companion receives it flagged IsFromMe — indistinguishable
// from the bot's own messages except by ID. Client records every reply ID
// it sends and skips only those echoes; any other IsFromMe text came from
// the phone's keyboard and is processed normally.
type Client struct {
	cli *whatsmeow.Client
	bot *Bot

	mu      sync.Mutex
	sentIDs map[string]struct{}
}

// Connect opens the device store, connects, pairs on first run via
// WA_PHONE, and registers the message handler. It returns once logged in.
func Connect(ctx context.Context, bot *Bot) (*Client, error) {
	storePath := strings.TrimSpace(envOr("WA_STORE", "./wastore.db"))
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+storePath+"?_foreign_keys=1", walog.Noop)
	if err != nil {
		return nil, fmt.Errorf("wa: open store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("wa: get device: %w", err)
	}
	cli := whatsmeow.NewClient(device, walog.Noop)
	c := &Client{cli: cli, bot: bot, sentIDs: map[string]struct{}{}}
	cli.AddEventHandler(c.handleEvent)

	if err := cli.Connect(); err != nil {
		return nil, fmt.Errorf("wa: connect: %w", err)
	}
	if cli.Store.ID == nil {
		phone := strings.TrimSpace(envOr("WA_PHONE", ""))
		if phone == "" {
			return nil, fmt.Errorf("wa: first run needs WA_PHONE (digits only, e.g. 15551234567) for pairing-code login")
		}
		// Display name must look like "Browser (OS)" — the server
		// validates it and 400s anything else (e.g. a bot name).
		code, err := cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Mac OS)")
		if err != nil {
			return nil, fmt.Errorf("wa: pair: %w", err)
		}
		fmt.Printf("wa: enter this code on the phone (Linked Devices → Link with phone number): %s\n", code)
		for !cli.IsLoggedIn() {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	log.Printf("wa: logged in as %s", cli.Store.ID)
	return c, nil
}

// Wait blocks until ctx is done. Run it after Connect to keep the process
// alive while events arrive on whatsmeow's goroutines.
func (c *Client) Wait(ctx context.Context) {
	<-ctx.Done()
	c.cli.Disconnect()
}

// handleEvent routes incoming text messages through the Bot and sends any
// reply back to the originating chat. Non-message events are ignored.
func (c *Client) handleEvent(evt any) {
	msg, ok := evt.(*events.Message)
	if !ok {
		return
	}
	if msg.Info.IsFromMe && c.isOwnEcho(string(msg.Info.ID)) {
		return // our own reply coming back — never answer it
	}
	text := messageText(msg)
	if text == "" {
		return
	}
	log.Printf("wa: recv from %s in %s: %.80q", msg.Info.Sender, msg.Info.Chat, text)
	// Fresh session per message: no cross-prompt history, so prior tool
	// observations never eat the context window. Multi-tool chains still
	// work — they happen inside one Loop.Run, not across sessions.
	sessionID := fmt.Sprintf("wa-%d", time.Now().UnixNano())
	reply, err := c.bot.HandleMessage(context.Background(), sessionID, text)
	if err != nil {
		log.Printf("wa: turn failed for %s: %v", msg.Info.Sender, err)
		reply = "Sorry — I hit an error looking that up. Try again?"
	}
	if reply == "" {
		log.Printf("wa: ignoring non-command from %s", msg.Info.Sender)
		return
	}
	log.Printf("wa: replying to %s (%d chars)", msg.Info.Chat, len(reply))
	resp, err := c.cli.SendMessage(context.Background(), msg.Info.Chat, &waE2E.Message{
		Conversation: proto.String(reply),
	})
	if err != nil {
		log.Printf("wa: send failed to %s: %v", msg.Info.Chat, err)
		return
	}
	c.markSent(string(resp.ID))
}

// isOwnEcho reports whether id is a reply this bot sent (consumed
// one-shot: each sent ID is skipped exactly once, then forgotten).
func (c *Client) isOwnEcho(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.sentIDs[id]; !ok {
		return false
	}
	delete(c.sentIDs, id)
	return true
}

// markSent records a reply ID so its echo is skipped.
func (c *Client) markSent(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sentIDs[id] = struct{}{}
}

// messageText returns the readable text of a message, covering plain and
// extended (reply/quoted) texts. Anything else (media, protocol) is "".
func messageText(msg *events.Message) string {
	m := msg.Message
	if m == nil {
		return ""
	}
	if t := m.GetConversation(); t != "" {
		return t
	}
	return m.GetExtendedTextMessage().GetText()
}
