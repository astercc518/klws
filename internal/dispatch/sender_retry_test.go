package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

type scriptedSender struct {
	calls int
	failN int // fail the first failN calls, then succeed
	err   error
	retID string
}

func (s *scriptedSender) Send(_ context.Context, _, _, _ string, _ *MediaHandle) (string, error) {
	s.calls++
	if s.calls <= s.failN {
		return "", s.err
	}
	return s.retID, nil
}

func TestRetrySender_RetriesThenSucceeds(t *testing.T) {
	inner := &scriptedSender{failN: 2, err: errors.New("transient"), retID: "OK"}
	var slept int
	r := NewRetrySender(inner, 5, time.Millisecond, 10*time.Millisecond).
		withSleep(func(time.Duration) { slept++ })
	id, err := r.Send(context.Background(), "j", "1", "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "OK" {
		t.Fatalf("id=%q", id)
	}
	if inner.calls != 3 {
		t.Fatalf("inner calls=%d want 3", inner.calls)
	}
	if slept != 2 {
		t.Fatalf("slept=%d want 2", slept)
	}
}

func TestRetrySender_PermanentNoRetry(t *testing.T) {
	perm := errors.New("loggedOut")
	inner := &scriptedSender{failN: 99, err: perm}
	r := NewRetrySender(inner, 5, time.Millisecond, time.Millisecond).
		WithPermanent(func(e error) bool { return errors.Is(e, perm) }).
		withSleep(func(time.Duration) {})
	_, err := r.Send(context.Background(), "j", "1", "b", nil)
	if !errors.Is(err, perm) {
		t.Fatalf("want perm err, got %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("permanent must not retry: calls=%d", inner.calls)
	}
}

func TestRetrySender_ExhaustsAttempts(t *testing.T) {
	inner := &scriptedSender{failN: 99, err: errors.New("transient")}
	r := NewRetrySender(inner, 3, time.Millisecond, time.Millisecond).
		withSleep(func(time.Duration) {})
	if _, err := r.Send(context.Background(), "j", "1", "b", nil); err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if inner.calls != 3 {
		t.Fatalf("calls=%d want 3", inner.calls)
	}
}

func TestRetrySender_CtxCancelStops(t *testing.T) {
	inner := &scriptedSender{failN: 99, err: errors.New("transient")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewRetrySender(inner, 5, time.Millisecond, time.Millisecond).
		withSleep(func(time.Duration) {})
	if _, err := r.Send(ctx, "j", "1", "b", nil); err == nil {
		t.Fatal("expected error under cancelled ctx")
	}
	if inner.calls != 1 {
		t.Fatalf("cancelled ctx should stop after first attempt: calls=%d", inner.calls)
	}
}
