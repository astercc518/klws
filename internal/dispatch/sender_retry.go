package dispatch

import (
	"context"
	"time"
)

// retrySender wraps a Sender with exponential backoff. E2 keeps it minimal:
// no jitter, no status classification, no governor coupling — those belong to
// E4 (backpressure/anti-ban). A permanent-error predicate (injected) short-
// circuits retries; the default retries every error up to maxAttempts.
type retrySender struct {
	inner       Sender
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
	permanent   func(error) bool
	sleep       func(time.Duration)
}

func NewRetrySender(inner Sender, maxAttempts int, baseDelay, maxDelay time.Duration) *retrySender {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	return &retrySender{
		inner:       inner,
		maxAttempts: maxAttempts,
		baseDelay:   baseDelay,
		maxDelay:    maxDelay,
		sleep:       time.Sleep,
	}
}

// WithPermanent injects the predicate that classifies an error as permanent
// (true = do not retry). E4 supplies the real classifier (loggedOut, 400
// not-connected, …).
func (r *retrySender) WithPermanent(fn func(error) bool) *retrySender {
	r.permanent = fn
	return r
}

// withSleep overrides the sleep function (tests inject a no-op recorder).
func (r *retrySender) withSleep(fn func(time.Duration)) *retrySender {
	r.sleep = fn
	return r
}

var _ Sender = (*retrySender)(nil)

func (r *retrySender) Send(ctx context.Context, jid, phone, body string, media *MediaHandle) (string, error) {
	delay := r.baseDelay
	var lastErr error
	for attempt := 0; attempt < r.maxAttempts; attempt++ {
		id, err := r.inner.Send(ctx, jid, phone, body, media)
		if err == nil {
			return id, nil
		}
		lastErr = err
		if r.permanent != nil && r.permanent(err) {
			return "", err
		}
		if attempt == r.maxAttempts-1 || ctx.Err() != nil {
			break
		}
		r.sleep(delay)
		if delay = delay * 2; delay > r.maxDelay {
			delay = r.maxDelay
		}
	}
	return "", lastErr
}
