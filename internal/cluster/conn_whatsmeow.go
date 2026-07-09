package cluster

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/acme/wadist/internal/dispatch"
	"github.com/acme/wadist/internal/store"
	"go.mau.fi/whatsmeow"
	waproto "go.mau.fi/whatsmeow/store"
	waLog "go.mau.fi/whatsmeow/util/log"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// ReceiptFunc receives a translated delivery/read receipt for an outgoing
// message. kind is "delivered" or "read". The cluster layer does nothing but
// translate whatsmeow's events.Receipt into these primitives and forward them;
// all persistence lives in internal/receipt (injected from cmd/wadist).
type ReceiptFunc func(messageIDs []string, kind string, at time.Time)

// waConn is the real whatsmeow-backed Conn. It is the ONLY file in this package
// that imports whatsmeow; everything else is fake-testable. No live-connection
// unit test exists (requires a real WhatsApp device + 4G proxy).
type waConn struct {
	client    *whatsmeow.Client
	proxy     *store.ProxyBinding
	permanent atomic.Bool // set on LoggedOut / StreamReplaced
}

// NewWAConn builds a client over a registered device store. Proxy is applied at
// Connect time (per-account dynamic 4G proxy) before dialing. When onReceipt is
// non-nil, a delivery/read receipt event handler is registered before connect —
// a thin translation that forwards events.Receipt to onReceipt and nothing else.
// whatsmeow's built-in auto-reconnect is always disabled and a terminal-signal
// event handler is always installed (see below); the ghost reaper owns
// reconnect/reclaim lifecycle instead.
func NewWAConn(device *waproto.Device, logger waLog.Logger, proxy *store.ProxyBinding, onReceipt ReceiptFunc) *waConn {
	client := whatsmeow.NewClient(device, logger)
	c := &waConn{client: client, proxy: proxy}
	if onReceipt != nil {
		client.AddEventHandler(func(evt any) {
			r, ok := evt.(*events.Receipt)
			if !ok || len(r.MessageIDs) == 0 {
				return
			}
			// Wire values are version-stable: ""→delivered, read/read-self→read.
			// Other receipt types (sender/played/…) are ignored.
			var kind string
			switch r.Type {
			case "":
				kind = "delivered"
			case "read", "read-self":
				kind = "read"
			default:
				return
			}
			ids := make([]string, len(r.MessageIDs))
			for i, m := range r.MessageIDs {
				ids[i] = string(m)
			}
			onReceipt(ids, kind, r.Timestamp)
		})
	}
	// Take back lifecycle control: stop whatsmeow's invisible auto-reconnect so
	// a half-dead socket surfaces as a ghost instead of self-healing while
	// leaking FDs/goroutines/proxy sockets. Terminal signals set Permanent so
	// the reaper marks the account logged-out rather than re-warming it.
	client.EnableAutoReconnect = false
	client.AddEventHandler(func(evt any) {
		switch evt.(type) {
		case *events.LoggedOut, *events.StreamReplaced:
			c.permanent.Store(true)
		}
	})
	return c
}

var _ Conn = (*waConn)(nil)

// Connect dials the WhatsApp websocket. ctx is intentionally unused because
// whatsmeow's client.Connect() takes no context; the proxy is applied before dialling.
func (c *waConn) Connect(_ context.Context) error {
	if c.proxy != nil {
		if err := store.ApplyProxy(c.client, c.proxy); err != nil {
			return err
		}
	}
	return c.client.Connect()
}

func (c *waConn) Disconnect() { c.client.Disconnect() }

var _ LivenessConn = (*waConn)(nil)

// Liveness reports the whatsmeow socket state: Alive iff connected AND logged-in
// right now; Permanent iff a terminal LoggedOut/StreamReplaced was observed.
func (c *waConn) Liveness() Liveness {
	return Liveness{
		Alive:     c.client.IsConnected() && c.client.IsLoggedIn(),
		Permanent: c.permanent.Load(),
	}
}

var _ PresenceConn = (*waConn)(nil)

// SetPresence signals the account's global online/offline state to WhatsApp.
// Call with available=true before a sending burst and available=false after.
func (c *waConn) SetPresence(ctx context.Context, available bool) error {
	st := types.PresenceUnavailable
	if available {
		st = types.PresenceAvailable
	}
	return c.client.SendPresence(ctx, st)
}

// SendTyping signals composing/paused state in a specific chat. chatPhone is
// the bare E.164-style number (no @s.whatsapp.net suffix needed).
func (c *waConn) SendTyping(ctx context.Context, chatPhone string, composing bool) error {
	jid := types.NewJID(chatPhone, types.DefaultUserServer)
	st := types.ChatPresencePaused
	if composing {
		st = types.ChatPresenceComposing
	}
	return c.client.SendChatPresence(ctx, jid, st, "")
}

var _ SessionSender = (*waConn)(nil)

// Send delivers a text message to `phone` via the live whatsmeow client and
// returns the server message id. Media is not yet supported (Module A is
// text-first); a non-nil media handle is rejected before any client use, so
// this method is safe to call with a nil client in tests.
func (c *waConn) Send(ctx context.Context, phone, body string, media *dispatch.MediaHandle) (string, error) {
	if media != nil {
		return "", errors.New("cluster: media send not supported yet")
	}
	jid := types.NewJID(phone, types.DefaultUserServer)
	msg := &waE2E.Message{Conversation: proto.String(body)}
	resp, err := c.client.SendMessage(ctx, jid, msg)
	if err != nil {
		return "", fmt.Errorf("whatsmeow send: %w", err)
	}
	return string(resp.ID), nil
}
