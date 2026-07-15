package warmup

import (
	"context"
	"log"
	"math/rand"
	"time"
)

// TickOnce runs one warmup round: evaluate+promote every WARMING profile,
// then PairAndWarm the pool. Failures are logged and never fatal — a broken
// warmup tick must never take down the worker process, mirroring
// metrics.Sampler.RunLoop's philosophy (it only touches warmup_* tables,
// never the send/dispatch engine).
func (s *Service) TickOnce(ctx context.Context, rng *rand.Rand, sleep Sleeper) error {
	warming, err := s.store.ListByStage(ctx, StageWarming, 500)
	if err != nil {
		return err
	}
	for _, p := range warming {
		if _, err := s.EvaluateAndPromote(ctx, p.AccountJID); err != nil {
			log.Printf("warmup: promote %s: %v", p.AccountJID, err)
		}
	}
	pairs, msgs, err := s.PairAndWarm(ctx, 200, rng, sleep)
	if err != nil {
		return err
	}
	if pairs > 0 {
		log.Printf("warmup: paired %d, sent %d warmup messages", pairs, msgs)
	}
	return nil
}

// RunLoop ticks TickOnce every interval until ctx is cancelled. interval<=0
// blocks on ctx (loop disabled, matching the msEnv "0 = off" convention used
// elsewhere in this codebase). Tick failures are logged and swallowed —
// same never-fatal reasoning as TickOnce/metrics.Sampler.RunLoop.
func (s *Service) RunLoop(ctx context.Context, interval time.Duration, rng *rand.Rand) error {
	if interval <= 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := s.TickOnce(ctx, rng, time.Sleep); err != nil {
				log.Printf("warmup tick: %v", err)
			}
		}
	}
}
