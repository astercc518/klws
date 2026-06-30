// Package cluster manages active account sessions and orchestrates graceful
// shutdown. The live whatsmeow connection is abstracted behind Conn and the
// advisory device lock behind DeviceLockHandle so session lifecycle, the
// Registry, and the Supervisor are all unit-testable with fakes. The only file
// that touches a real whatsmeow client is conn_whatsmeow.go.
package cluster

import (
	"context"
	"sync"
	"time"
)

// Conn is one account's live transport (real impl wraps a whatsmeow client).
type Conn interface {
	Connect(ctx context.Context) error
	Disconnect()
}

// PresenceConn is an optional anthropomorphic capability on top of Conn:
// online-presence signalling and per-chat typing indicators. waConn implements
// it; Conn implementations that do not (test fakes, capability-less conns) are
// safely skipped at the call site.
type PresenceConn interface {
	SetPresence(ctx context.Context, available bool) error
	SendTyping(ctx context.Context, chatPhone string, composing bool) error
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

// GracefulClose signals presence-unavailable, waits linger (if > 0), then calls
// Close (disconnect → release lock). Never calls whatsmeow Logout. linger<=0
// skips the wait. Reuses Close's disconnect→release ordering.
func (s *Session) GracefulClose(ctx context.Context, linger time.Duration) {
	if pc, ok := s.conn.(PresenceConn); ok {
		_ = pc.SetPresence(ctx, false)
	}
	if linger > 0 {
		select {
		case <-time.After(linger):
		case <-ctx.Done():
		}
	}
	s.Close(ctx)
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
