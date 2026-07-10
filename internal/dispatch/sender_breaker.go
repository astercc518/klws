package dispatch

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned by circuitBreakerSender when an account's breaker
// is open (too many recent consecutive failures) — the send is shed without
// touching the Evolution instance.
var ErrCircuitOpen = errors.New("dispatch: circuit open for account")

type breakerState struct {
	fails     int
	openUntil time.Time
}

// circuitBreakerSender trips a per-jid breaker after `threshold` consecutive
// failures, shedding sends for `cooloff` before a half-open trial. Protects a
// sick Evolution instance (and our own throughput) from a hot failure loop.
// DORMANT: composed into the live send path only at E6 cutover.
type circuitBreakerSender struct {
	inner     Sender
	threshold int
	cooloff   time.Duration
	now       func() time.Time
	mu        sync.Mutex
	state     map[string]*breakerState
}

func NewCircuitBreakerSender(inner Sender, threshold int, cooloff time.Duration) *circuitBreakerSender {
	if threshold < 1 {
		threshold = 1
	}
	return &circuitBreakerSender{
		inner:     inner,
		threshold: threshold,
		cooloff:   cooloff,
		now:       time.Now,
		state:     map[string]*breakerState{},
	}
}

// withNow injects a clock for tests.
func (b *circuitBreakerSender) withNow(fn func() time.Time) *circuitBreakerSender {
	b.now = fn
	return b
}

var _ Sender = (*circuitBreakerSender)(nil)

func (b *circuitBreakerSender) st(jid string) *breakerState {
	s := b.state[jid]
	if s == nil {
		s = &breakerState{}
		b.state[jid] = s
	}
	return s
}

func (b *circuitBreakerSender) Send(ctx context.Context, jid, phone, body string, media *MediaHandle) (string, error) {
	b.mu.Lock()
	s := b.st(jid)
	if !s.openUntil.IsZero() && b.now().Before(s.openUntil) {
		b.mu.Unlock()
		return "", ErrCircuitOpen
	}
	b.mu.Unlock()

	id, err := b.inner.Send(ctx, jid, phone, body, media)

	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		s.fails = 0
		s.openUntil = time.Time{}
		return id, nil
	}
	s.fails++
	if s.fails >= b.threshold {
		s.openUntil = b.now().Add(b.cooloff)
	}
	return id, err
}
