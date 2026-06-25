package cluster

import (
	"context"
	"fmt"

	"github.com/acme/wadist/internal/dispatch"
)

// SessionSender is one account session's outbound capability.
type SessionSender interface {
	Send(ctx context.Context, phone, body string, media *dispatch.MediaHandle) (string, error)
}

// NewSessionWithSender builds a Session with an outbound sender wired (the real
// factory passes the whatsmeow conn as both Conn and SessionSender).
func NewSessionWithSender(jid string, conn Conn, lock DeviceLockHandle, sender SessionSender) *Session {
	return newSessionWithSender(jid, conn, lock, sender)
}

// newSessionWithSender creates a Session with an optional send capability.
// Production NewSession leaves sender nil.
func newSessionWithSender(jid string, conn Conn, lock DeviceLockHandle, sender SessionSender) *Session {
	s := NewSession(jid, conn, lock)
	s.sender = sender
	return s
}

// RoutingSender implements dispatch.Sender by routing each send to the live
// session that owns the destination JID. A missing/incapable session returns a
// clear error so the send pipeline requeues/refunds rather than silently
// succeeding.
type RoutingSender struct{ reg *Registry }

func NewRoutingSender(reg *Registry) *RoutingSender { return &RoutingSender{reg: reg} }

var _ dispatch.Sender = (*RoutingSender)(nil)

func (rs *RoutingSender) Send(ctx context.Context, jid, phone, body string, media *dispatch.MediaHandle) (string, error) {
	sess, ok := rs.reg.Get(jid)
	if !ok {
		return "", fmt.Errorf("cluster: no active session for routing")
	}
	if sess.sender == nil {
		return "", fmt.Errorf("cluster: session has no send capability")
	}
	return sess.sender.Send(ctx, phone, body, media)
}
