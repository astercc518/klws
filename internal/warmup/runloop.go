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
	// sleeper 是 ctx-aware 的抖动等待:cancel 时立即返回而不是傻等 time.Sleep
	// 的全量 duration,这样 SIGTERM 能让一个正在进行的 tick(PairAndWarm 内部
	// 逐句间的 jitter 等待)尽快退出,而不是拖到本轮抖动全部睡完。
	sleeper := func(d time.Duration) {
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := s.TickOnce(ctx, rng, sleeper); err != nil {
				log.Printf("warmup tick: %v", err)
			}
		}
	}
}
