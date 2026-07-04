package dispatch

import (
	"context"
	"fmt"

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
