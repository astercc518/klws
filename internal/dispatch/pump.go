package dispatch

import (
	"context"
	"errors"
	"time"
)

var ErrPumpFull = errors.New("dispatch: pump buffer full")

// PumpEnqueuer is an in-memory Enqueuer: dispatchBatch pushes SendPayloads into a
// bounded channel instead of asynq. It is non-blocking — a full buffer returns
// ErrPumpFull so the caller's transaction rolls back that assignment (the
// recipient stays pending and is retried on the next fill), providing natural
// backpressure without holding a DB tx open.
type PumpEnqueuer struct {
	ch chan SendPayload
}

func NewPumpEnqueuer(buffer int) *PumpEnqueuer {
	if buffer < 1 {
		buffer = 1
	}
	return &PumpEnqueuer{ch: make(chan SendPayload, buffer)}
}

var _ Enqueuer = (*PumpEnqueuer)(nil)

// EnqueueSend pushes non-blockingly; delay is ignored (the pump paces sends).
func (p *PumpEnqueuer) EnqueueSend(_ context.Context, pl SendPayload, _ time.Duration) error {
	select {
	case p.ch <- pl:
		return nil
	default:
		return ErrPumpFull
	}
}

// C is the read side consumed by pump workers.
func (p *PumpEnqueuer) C() <-chan SendPayload { return p.ch }

// Len reports current buffered payloads (used by the fill loop for backpressure).
func (p *PumpEnqueuer) Len() int { return len(p.ch) }

// Cap reports the buffer capacity.
func (p *PumpEnqueuer) Cap() int { return cap(p.ch) }

// Close closes the channel so pump workers drain and exit.
func (p *PumpEnqueuer) Close() { close(p.ch) }
