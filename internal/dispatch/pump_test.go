package dispatch

import (
	"context"
	"errors"
	"testing"
)

func TestPumpEnqueuer_FullReturnsErr(t *testing.T) {
	p := NewPumpEnqueuer(1)
	ctx := context.Background()
	if err := p.EnqueueSend(ctx, SendPayload{RecipientID: 1}, 0); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	// buffer=1 now full → non-blocking, returns ErrPumpFull (does NOT block).
	if err := p.EnqueueSend(ctx, SendPayload{RecipientID: 2}, 0); !errors.Is(err, ErrPumpFull) {
		t.Fatalf("second enqueue err = %v; want ErrPumpFull", err)
	}
	got := <-p.C()
	if got.RecipientID != 1 {
		t.Fatalf("drained %d; want 1", got.RecipientID)
	}
}
