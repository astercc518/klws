// Package cluster manages active account sessions and orchestrates graceful
// shutdown. The live whatsmeow connection is abstracted behind Conn and the
// advisory device lock behind DeviceLockHandle so session lifecycle, the
// Registry, and the Supervisor are all unit-testable with fakes. The only file
// that touches a real whatsmeow client is conn_whatsmeow.go.
package cluster

import (
	"context"
	"sync"
)

// Conn is one account's live transport (real impl wraps a whatsmeow client).
type Conn interface {
	Connect(ctx context.Context) error
	Disconnect()
}

// DeviceLockHandle is the cross-process ownership lock for an account.
// *store.DeviceLock satisfies this structurally.
type DeviceLockHandle interface {
	Healthy(ctx context.Context) bool
	Release(ctx context.Context)
}

// Session is one active account: a live Conn guarded by an advisory lock.
type Session struct {
	jid    string
	conn   Conn
	lock   DeviceLockHandle
	sender SessionSender
	mu     sync.Mutex
	closed bool
}

func NewSession(jid string, conn Conn, lock DeviceLockHandle) *Session {
	return &Session{jid: jid, conn: conn, lock: lock}
}

func (s *Session) JID() string { return s.jid }

func (s *Session) Healthy(ctx context.Context) bool {
	return s.lock != nil && s.lock.Healthy(ctx)
}

// Close detaches the session: disconnect the live transport FIRST (stop
// sending/receiving), THEN release the advisory lock (so another node may take
// over only after we have fully detached). Idempotent.
func (s *Session) Close(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.conn != nil {
		s.conn.Disconnect()
	}
	if s.lock != nil {
		s.lock.Release(ctx)
	}
}
