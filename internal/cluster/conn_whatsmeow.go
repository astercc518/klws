package cluster

import (
	"context"
	"errors"
	"fmt"

	"github.com/acme/wadist/internal/dispatch"
	"github.com/acme/wadist/internal/store"
	"go.mau.fi/whatsmeow"
	waproto "go.mau.fi/whatsmeow/store"
	waLog "go.mau.fi/whatsmeow/util/log"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// waConn is the real whatsmeow-backed Conn. It is the ONLY file in this package
// that imports whatsmeow; everything else is fake-testable. No live-connection
// unit test exists (requires a real WhatsApp device + 4G proxy).
type waConn struct {
	client *whatsmeow.Client
	proxy  *store.ProxyBinding
}

// NewWAConn builds a client over a registered device store. Proxy is applied at
// Connect time (per-account dynamic 4G proxy) before dialing.
func NewWAConn(device *waproto.Device, logger waLog.Logger, proxy *store.ProxyBinding) *waConn {
	return &waConn{client: whatsmeow.NewClient(device, logger), proxy: proxy}
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
