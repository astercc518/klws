package dispatch

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

var ErrPumpFull = errors.New("dispatch: pump buffer full")

// PumpEnqueuer is an in-memory Enqueuer: dispatchBatch pushes SendPayloads into a
// bounded channel instead of asynq. It is non-blocking — a full buffer returns
// ErrPumpFull so the caller's transaction rolls back that assignment (the
// recipient stays pending and is retried on the next fill), providing natural
// backpressure without holding a DB tx open.
type PumpEnqueuer struct {
	ch   chan SendPayload
	once sync.Once
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

// Close closes the channel so pump workers drain and exit. Idempotent: a
// second (or concurrent) call is a safe no-op rather than a panic, as
// belt-and-suspenders for the shutdown path (production wiring guarantees a
// single caller, but Close must never be the thing that crashes shutdown).
func (p *PumpEnqueuer) Close() { p.once.Do(func() { close(p.ch) }) }

// Pump is the rolling-wave send pump: a bounded pool of workers drains
// PumpEnqueuer's channel, each acquiring a token from a shared rate.Limiter
// (smooth τ pacing — no batch pulse) before loading the send body and running
// the existing SendWorker.ProcessSend pipeline verbatim.
type Pump struct {
	pe      *PumpEnqueuer
	w       *SendWorker
	resolve SendBodyResolver
	lim     *rate.Limiter
	workers int
}

// NewPump wires a Pump. burst == workers so all workers can grab a token at
// startup (avoiding an artificial cold-start stall), while the sustained rate
// is capped at ratePerSec.
func NewPump(pe *PumpEnqueuer, w *SendWorker, resolve SendBodyResolver, ratePerSec float64, workers int) *Pump {
	if workers < 1 {
		workers = 1
	}
	// ratePerSec<=0 means "unlimited": rate.Limit(0) would instead block every
	// send after the initial burst is drained, so use rate.Inf.
	lim := rate.NewLimiter(rate.Inf, workers)
	if ratePerSec > 0 {
		lim = rate.NewLimiter(rate.Limit(ratePerSec), workers)
	}
	return &Pump{
		pe:      pe,
		w:       w,
		resolve: resolve,
		lim:     lim,
		workers: workers,
	}
}

// Run starts the worker pool and blocks until every worker exits.
//
// Shutdown contract: the channel is closed by the OWNER (production wiring)
// after producers stop; workers keep draining already-buffered payloads even
// after ctx is cancelled — committed-but-unsent work is never dropped, it is
// only bounded by the caller's own shutdown timeout — and Run returns once
// the channel is drained and every worker has exited.
func (p *Pump) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pl := range p.pe.C() {
				sendCtx := ctx
				if err := p.lim.Wait(ctx); err != nil {
					// Wait only errors on ctx cancel/deadline or n>burst (n=1 is
					// always ≤ burst here), so ANY error means: stop pacing and
					// drain this buffered payload on a background context — never
					// dropping committed-but-unsent work (bounded by the upstream
					// ShutdownTimeout).
					//
					// This covers BOTH ctx cancellation AND the deadline-PREDICT
					// path, where Wait returns "would exceed context deadline"
					// while ctx.Err() is still nil: gating the fallback on
					// ctx.Err()!=nil would there skip the token AND hand
					// ProcessSend an about-to-expire ctx, aborting the send.
					sendCtx = context.Background()
				}
				p.process(sendCtx, pl)
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// SetRate updates the pump's token-bucket rate at runtime (used by the Risk
// Governor). r<=0 means unlimited (rate.Inf), matching NewPump. Thread-safe:
// rate.Limiter.SetLimit is mutex-guarded, safe against concurrent Wait().
func (p *Pump) SetRate(r float64) {
	if r <= 0 {
		p.lim.SetLimit(rate.Inf)
		return
	}
	p.lim.SetLimit(rate.Limit(r))
}

// process loads the send body/media for the payload's campaign and runs the
// existing ProcessSend pipeline. Per-send errors are logged and swallowed —
// the pump never crashes on a single bad send.
func (p *Pump) process(ctx context.Context, pl SendPayload) {
	body, mediaSha, mime, raw, err := p.resolve(ctx, pl.CampaignID)
	if err != nil {
		log.Printf("dispatch: pump resolve campaign %d: %v", pl.CampaignID, err)
		return
	}
	if err := p.w.ProcessSend(ctx, pl, body, mediaSha, mime, raw); err != nil {
		log.Printf("dispatch: pump ProcessSend recipient %d: %v", pl.RecipientID, err)
	}
}
