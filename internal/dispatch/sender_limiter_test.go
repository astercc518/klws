package dispatch

import (
	"context"
	"testing"
	"time"
)

// gateSender blocks each Send until the test pushes to release, and announces
// entry on entered. Lets the test observe concurrency deterministically.
type gateSender struct {
	entered chan string
	release chan struct{}
}

func (g *gateSender) Send(_ context.Context, jid, _, _ string, _ *MediaHandle) (string, error) {
	g.entered <- jid
	<-g.release
	return "id", nil
}

func TestPerInstanceLimiter_SameJIDSerialized(t *testing.T) {
	g := &gateSender{entered: make(chan string, 4), release: make(chan struct{}, 4)}
	l := NewPerInstanceLimiter(g, 1)

	go func() { _, _ = l.Send(context.Background(), "j", "1", "a", nil) }() // A
	<-g.entered                                                             // A is inside inner (holds the only slot)

	done := make(chan struct{})
	go func() { _, _ = l.Send(context.Background(), "j", "1", "b", nil); close(done) }() // B

	// B must be blocked on acquire — it neither enters inner nor completes.
	select {
	case <-g.entered:
		t.Fatal("B entered inner before A released (limit not enforced)")
	case <-done:
		t.Fatal("B completed before A released")
	case <-time.After(80 * time.Millisecond):
		// expected: B blocked
	}

	g.release <- struct{}{} // release A
	<-g.entered             // now B enters
	g.release <- struct{}{} // release B
	<-done
}

func TestPerInstanceLimiter_DifferentJIDsConcurrent(t *testing.T) {
	g := &gateSender{entered: make(chan string, 4), release: make(chan struct{}, 4)}
	l := NewPerInstanceLimiter(g, 1)

	go func() { _, _ = l.Send(context.Background(), "a", "1", "x", nil) }()
	go func() { _, _ = l.Send(context.Background(), "b", "1", "y", nil) }()

	// Both distinct jids should be able to enter inner concurrently.
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case j := <-g.entered:
			got[j] = true
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("only %d/2 distinct jids entered concurrently: %v", len(got), got)
		}
	}
	if !got["a"] || !got["b"] {
		t.Fatalf("expected both a and b, got %v", got)
	}
	g.release <- struct{}{}
	g.release <- struct{}{}
}

func TestPerInstanceLimiter_CtxCancelReleases(t *testing.T) {
	g := &gateSender{entered: make(chan string, 4), release: make(chan struct{}, 4)}
	l := NewPerInstanceLimiter(g, 1)
	// Fill the slot with a blocked A.
	go func() { _, _ = l.Send(context.Background(), "j", "1", "a", nil) }()
	<-g.entered
	// B with a cancelled ctx must not block forever — returns ctx error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Send(ctx, "j", "1", "b", nil); err == nil {
		t.Fatal("expected ctx error when slot full and ctx cancelled")
	}
	g.release <- struct{}{}
}
