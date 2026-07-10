package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

type flakySender struct {
	calls int
	err   error // when non-nil, every call fails with this
}

func (f *flakySender) Send(_ context.Context, _, _, _ string, _ *MediaHandle) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return "OK", nil
}

func TestCircuitBreaker_OpensAfterThresholdAndShortCircuits(t *testing.T) {
	inner := &flakySender{err: errors.New("boom")}
	now := time.Unix(1000, 0)
	b := NewCircuitBreakerSender(inner, 2, 30*time.Second).withNow(func() time.Time { return now })

	// 2 failures reach threshold → circuit opens.
	_, _ = b.Send(context.Background(), "j", "1", "a", nil)
	_, _ = b.Send(context.Background(), "j", "1", "a", nil)
	if inner.calls != 2 {
		t.Fatalf("inner calls=%d want 2", inner.calls)
	}
	// 3rd call while open → ErrCircuitOpen, inner NOT called.
	_, err := b.Send(context.Background(), "j", "1", "a", nil)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("want ErrCircuitOpen, got %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("inner must not be called while open: calls=%d", inner.calls)
	}
}

func TestCircuitBreaker_HalfOpenAfterCooloff(t *testing.T) {
	inner := &flakySender{err: errors.New("boom")}
	now := time.Unix(1000, 0)
	b := NewCircuitBreakerSender(inner, 1, 30*time.Second).withNow(func() time.Time { return now })

	_, _ = b.Send(context.Background(), "j", "1", "a", nil) // opens (threshold 1)
	if _, err := b.Send(context.Background(), "j", "1", "a", nil); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected open, got %v", err)
	}
	// Advance past cooloff → half-open: inner is tried again.
	now = now.Add(31 * time.Second)
	inner.err = nil // now it recovers
	id, err := b.Send(context.Background(), "j", "1", "a", nil)
	if err != nil || id != "OK" {
		t.Fatalf("half-open trial should reach inner: id=%q err=%v", id, err)
	}
	// After success, circuit closed: subsequent calls flow.
	if _, err := b.Send(context.Background(), "j", "1", "a", nil); err != nil {
		t.Fatalf("closed circuit should pass: %v", err)
	}
}

func TestCircuitBreaker_SuccessResetsFailCount(t *testing.T) {
	inner := &flakySender{err: errors.New("boom")}
	now := time.Unix(1000, 0)
	b := NewCircuitBreakerSender(inner, 2, 30*time.Second).withNow(func() time.Time { return now })
	_, _ = b.Send(context.Background(), "j", "1", "a", nil) // fail 1
	inner.err = nil
	_, _ = b.Send(context.Background(), "j", "1", "a", nil) // success → reset
	inner.err = errors.New("boom again")
	_, _ = b.Send(context.Background(), "j", "1", "a", nil) // fail 1 again (not 2)
	// Circuit should still be closed (only 1 consecutive fail since reset).
	if _, err := b.Send(context.Background(), "j", "1", "a", nil); errors.Is(err, ErrCircuitOpen) {
		t.Fatal("circuit opened too early — success did not reset fail count")
	}
}

func TestCircuitBreaker_PerJIDIsolation(t *testing.T) {
	inner := &flakySender{err: errors.New("boom")}
	now := time.Unix(1000, 0)
	b := NewCircuitBreakerSender(inner, 1, 30*time.Second).withNow(func() time.Time { return now })
	_, _ = b.Send(context.Background(), "j1", "1", "a", nil) // opens j1
	// j2 is independent — should still reach inner.
	before := inner.calls
	_, _ = b.Send(context.Background(), "j2", "1", "a", nil)
	if inner.calls != before+1 {
		t.Fatal("j2 breaker should be independent of j1")
	}
}
