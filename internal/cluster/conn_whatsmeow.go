package cluster

import (
	"context"

	"github.com/acme/wadist/internal/store"
	"go.mau.fi/whatsmeow"
	waproto "go.mau.fi/whatsmeow/store"
	waLog "go.mau.fi/whatsmeow/util/log"
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

func (c *waConn) Connect(_ context.Context) error {
	if c.proxy != nil {
		if err := store.ApplyProxy(c.client, c.proxy); err != nil {
			return err
		}
	}
	return c.client.Connect()
}

func (c *waConn) Disconnect() { c.client.Disconnect() }
