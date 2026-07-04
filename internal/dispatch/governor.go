package dispatch

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// GovParams are the AIMD knobs. MaxRate is the SendRate ceiling.
type GovParams struct {
	SLO, Step, Factor, MinRate, MaxRate float64
}

// nextRate applies AIMD: at/over the SLO the rate is multiplicatively decreased
// (floored at MinRate); below the SLO it is additively increased (capped at
// MaxRate). Pure function — the whole control law, unit-tested in isolation.
func nextRate(current, banRate float64, p GovParams) float64 {
	if banRate >= p.SLO {
		next := current * p.Factor
		if next < p.MinRate {
			next = p.MinRate
		}
		return next
	}
	next := current + p.Step
	if next > p.MaxRate {
		next = p.MaxRate
	}
	return next
}

// FleetBanRate computes the fleet-wide definition-A ban rate over the last
// windowSec: banFailures/attempted where attempted = recent sent+failed and
// banFailures = recent failed whose last_error carries a ban signal. Mirrors
// riskbreaker.banRate without the campaign filter. Returns (0,0,nil) when idle.
func FleetBanRate(ctx context.Context, pool *pgxpool.Pool, windowSec int) (float64, int, error) {
	window := fmt.Sprintf("%d seconds", windowSec)
	var attempted, banFailures int
	err := pool.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE state IN ('sent','failed') AND updated_at >= now() - $1::interval),
  count(*) FILTER (WHERE state = 'failed' AND updated_at >= now() - $1::interval AND (
        last_error ILIKE '%wa_warning%' OR last_error ILIKE '%banned%' OR last_error ILIKE '%403%'))
  FROM campaign_recipients`, window).Scan(&attempted, &banFailures)
	if err != nil {
		return 0, 0, fmt.Errorf("fleet ban rate: %w", err)
	}
	if attempted == 0 {
		return 0, 0, nil
	}
	return float64(banFailures) / float64(attempted), attempted, nil
}

// Governor runs the AIMD control loop: on each tick it reads the fleet ban
// rate and, when there is enough signal, nudges the pump send-rate τ via
// setRate. It holds the current τ across ticks so each step is relative to
// the last applied value (not the original starting rate).
type Governor struct {
	pool      *pgxpool.Pool
	setRate   func(float64)
	params    GovParams
	windowSec int
	minSample int
	current   float64
}

// NewGovernor builds a Governor. current is the initial τ (= SendRate);
// setRate is typically pump.SetRate.
func NewGovernor(pool *pgxpool.Pool, setRate func(float64), params GovParams, windowSec, minSample int, current float64) *Governor {
	return &Governor{pool: pool, setRate: setRate, params: params, windowSec: windowSec, minSample: minSample, current: current}
}

// EvaluateOnce reads the fleet ban rate and, if there is enough signal
// (attempted >= minSample), applies one AIMD step to the pump rate. Returns the
// new/current rate, whether it was applied, and any error. Exported for tests.
func (g *Governor) EvaluateOnce(ctx context.Context) (float64, bool, error) {
	banRate, attempted, err := FleetBanRate(ctx, g.pool, g.windowSec)
	if err != nil {
		return g.current, false, err
	}
	if attempted < g.minSample {
		return g.current, false, nil // not enough signal; leave τ unchanged
	}
	g.current = nextRate(g.current, banRate, g.params)
	g.setRate(g.current)
	return g.current, true, nil
}

// Run drives EvaluateOnce every interval until ctx is cancelled. Per-tick
// errors are logged and do not stop the loop (a transient DB error recovers
// on the next tick).
func (g *Governor) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, applied, err := g.EvaluateOnce(ctx); err != nil {
				log.Printf("risk governor: evaluate: %v", err)
			} else if applied {
				log.Printf("risk governor: tau -> %.1f/s", g.current)
			}
		}
	}
}
