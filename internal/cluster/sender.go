package cluster

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/acme/wadist/internal/dispatch"
)

// ErrLostOwnership is returned when fenceOnSend is enabled and the session's
// advisory lock is no longer healthy at send time. The send is aborted without
// touching the underlying sender so the worker may requeue/refund safely.
var ErrLostOwnership = errors.New("cluster: lost ownership before send")

// TypingPolicy controls the composing-indicator delay injected around each
// send. When Min==Max==0 the feature is a no-op (zero allocation, no sleep).
type TypingPolicy struct{ Min, Max time.Duration }

// package-level seeded RNG — not the global rand to avoid lock contention with
// other callers. Protected by rngMu.
var (
	rngMu sync.Mutex
	rng   = rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec
)

func randInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	rngMu.Lock()
	v := rng.Int63n(n)
	rngMu.Unlock()
	return v
}

// sleepJitter sleeps for a random duration in [min, max). When min==max==0 it
// is a no-op. The sleep is ctx-interruptible.
func sleepJitter(ctx context.Context, min, max time.Duration) {
	d := min
	if max > min {
		d = min + time.Duration(randInt63n(int64(max-min)))
	}
	if d <= 0 {
		return
	}
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

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
type RoutingSender struct {
	reg         *Registry
	fenceOnSend bool
	typing      TypingPolicy
}

// NewRoutingSender returns a RoutingSender with default behaviour: no fence
// check, no typing indicators. Existing callers and tests are unaffected.
func NewRoutingSender(reg *Registry) *RoutingSender { return &RoutingSender{reg: reg} }

// NewRoutingSenderWithPolicy returns a RoutingSender with optional fence-on-send
// and typing-sequence behaviour.
func NewRoutingSenderWithPolicy(reg *Registry, fenceOnSend bool, typing TypingPolicy) *RoutingSender {
	return &RoutingSender{reg: reg, fenceOnSend: fenceOnSend, typing: typing}
}

var _ dispatch.Sender = (*RoutingSender)(nil)

func (rs *RoutingSender) Send(ctx context.Context, jid, phone, body string, media *dispatch.MediaHandle) (string, error) {
	sess, ok := rs.reg.Get(jid)
	if !ok {
		return "", fmt.Errorf("cluster: no active session for routing")
	}
	if sess.sender == nil {
		return "", fmt.Errorf("cluster: session has no send capability")
	}

	// (A) Fence-on-send: abort if ownership lock is no longer healthy.
	if rs.fenceOnSend && !sess.Healthy(ctx) {
		return "", ErrLostOwnership
	}

	// (B) Typing anthropomorphism: composing → jitter → send → paused (best-effort).
	pc, hasPresence := sess.conn.(PresenceConn)
	if hasPresence {
		_ = pc.SendTyping(ctx, phone, true)
		sleepJitter(ctx, rs.typing.Min, rs.typing.Max)
	}
	id, err := sess.sender.Send(ctx, phone, body, media)
	if hasPresence {
		_ = pc.SendTyping(ctx, phone, false)
	}
	return id, err
}
