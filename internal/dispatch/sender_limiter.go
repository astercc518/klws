package dispatch

import (
	"context"
	"sync"
)

// perInstanceLimiter bounds concurrent sends per account (jid → one Evolution
// instance → one Baileys socket). Blasting a single socket with concurrent
// sends reorders/trips WhatsApp; this caps in-flight sends per jid at max.
// DORMANT: composed into the live send path only at E6 cutover.
type perInstanceLimiter struct {
	inner Sender
	max   int
	mu    sync.Mutex
	sems  map[string]chan struct{}
}

func NewPerInstanceLimiter(inner Sender, max int) *perInstanceLimiter {
	if max < 1 {
		max = 1
	}
	return &perInstanceLimiter{inner: inner, max: max, sems: map[string]chan struct{}{}}
}

var _ Sender = (*perInstanceLimiter)(nil)

func (l *perInstanceLimiter) sem(jid string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.sems[jid]
	if s == nil {
		s = make(chan struct{}, l.max)
		l.sems[jid] = s
	}
	return s
}

func (l *perInstanceLimiter) Send(ctx context.Context, jid, phone, body string, media *MediaHandle) (string, error) {
	s := l.sem(jid)
	select {
	case s <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s }()
	return l.inner.Send(ctx, jid, phone, body, media)
}
