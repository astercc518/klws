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
//
// MUST be called with a BYPASSRLS pool (e.g. store.Manager.SystemPool(), role
// app_system). campaign_recipients has FORCE ROW LEVEL SECURITY; a
// tenant-scoped (RLS) pool silently narrows this "fleet-wide" query to
// whichever single tenant is bound to that session/role, giving a wrong
// (under-counted) ban rate with no error.
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

// SegStat is a country's window send outcomes (definition A).
type SegStat struct {
	CC          string
	Attempted   int
	BanFailures int
}

// SegmentBanRates returns per-country attempted/ban-failure counts over the last
// windowSec (definition A, grouped by country_code). Mirrors FleetBanRate's
// FILTER predicates exactly, adding GROUP BY country_code. Returns one SegStat
// per country_code seen in the window; empty slice when there is no data.
//
// MUST be called with a BYPASSRLS pool (e.g. store.Manager.SystemPool(), role
// app_system). campaign_recipients has FORCE ROW LEVEL SECURITY; a
// tenant-scoped (RLS) pool silently narrows this "fleet-wide" query to
// whichever single tenant is bound to that session/role, giving wrong
// (under-counted) per-segment counts with no error.
func SegmentBanRates(ctx context.Context, pool *pgxpool.Pool, windowSec int) ([]SegStat, error) {
	window := fmt.Sprintf("%d seconds", windowSec)
	rows, err := pool.Query(ctx, `
SELECT country_code,
  count(*) FILTER (WHERE state IN ('sent','failed') AND updated_at >= now() - $1::interval),
  count(*) FILTER (WHERE state = 'failed' AND updated_at >= now() - $1::interval AND (
        last_error ILIKE '%wa_warning%' OR last_error ILIKE '%banned%' OR last_error ILIKE '%403%'))
  FROM campaign_recipients
 GROUP BY country_code`, window)
	if err != nil {
		return nil, fmt.Errorf("segment ban rates: %w", err)
	}
	defer rows.Close()
	var out []SegStat
	for rows.Next() {
		var s SegStat
		if err := rows.Scan(&s.CC, &s.Attempted, &s.BanFailures); err != nil {
			return nil, fmt.Errorf("scan segment stat: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SegParams are the L3 (per-cc) segment-governor knobs. A cc is judged
// against the rest of the fleet's ban rate scaled by Mult, floored at the
// absolute SegSLO so a healthy fleet doesn't flag noise; MinSample gates
// judgment on ccs with too little traffic to be meaningful.
type SegParams struct {
	Mult, SegSLO, SlowRate float64
	MinSample              int
}

// hotSegments returns the set of ccs whose window ban rate is elevated
// relative to the rest of the fleet. For each cc with attempted >= MinSample,
// its baseline is computed leave-one-out (the pooled rate of every OTHER
// stat, so a segment already going bad can't inflate its own threshold and
// dodge detection as its share of fleet volume grows); threshold =
// max(baseline*Mult, SegSLO); the cc is hot when its own rate clears that
// threshold.
//
// Two known limitations for future tuners (accepted, not bugs):
//   - Sub-MinSample segments are excluded from being judged, but their counts
//     STILL contribute to the leave-one-out "rest" pool sums (totAtt/totBan),
//     so a tiny noisy segment can nudge every other cc's baseline.
//   - Under multiple simultaneously-hot ccs, one severe outlier inflates the
//     rest-baseline used to judge the others, which can mask a second,
//     moderately-elevated cc until the outlier recovers.
func hotSegments(stats []SegStat, p SegParams) map[string]bool {
	var totAtt, totBan int
	for _, s := range stats {
		totAtt += s.Attempted
		totBan += s.BanFailures
	}
	hot := map[string]bool{}
	for _, s := range stats {
		if s.Attempted < p.MinSample {
			continue
		}
		restAtt := totAtt - s.Attempted
		restBan := totBan - s.BanFailures
		var baseline float64
		if restAtt > 0 {
			baseline = float64(restBan) / float64(restAtt)
		}
		threshold := baseline * p.Mult
		if p.SegSLO > threshold {
			threshold = p.SegSLO
		}
		rate := float64(s.BanFailures) / float64(s.Attempted)
		if rate >= threshold {
			hot[s.CC] = true
		}
	}
	return hot
}

// Governor runs the AIMD control loop: on each tick it reads the fleet ban
// rate and, when there is enough signal, nudges the pump send-rate τ via
// setRate. It holds the current τ across ticks so each step is relative to
// the last applied value (not the original starting rate).
//
// Optionally (via WithSegments) it also runs an L3 per-cc segment pass each
// tick: hot ccs get capped at a slow rate via setSegRate, and recovered ccs
// get uncapped. This pass is independent of the L4 fleet-wide AIMD above —
// its errors are logged and swallowed so a segment-query hiccup never blocks
// the fleet-wide rate control.
type Governor struct {
	pool      *pgxpool.Pool
	setRate   func(float64)
	params    GovParams
	windowSec int
	minSample int
	current   float64

	setSegRate func(cc string, r float64)
	segParams  SegParams
	capped     map[string]bool
}

// NewGovernor builds a Governor. current is the initial τ (= SendRate);
// setRate is typically pump.SetRate.
func NewGovernor(pool *pgxpool.Pool, setRate func(float64), params GovParams, windowSec, minSample int, current float64) *Governor {
	return &Governor{pool: pool, setRate: setRate, params: params, windowSec: windowSec, minSample: minSample, current: current}
}

// WithSegments enables the L3 segment pass: setSegRate(cc, SlowRate) caps a
// hot cc, setSegRate(cc, 0) uncaps a recovered one. Builder — returns g.
// Without this call setSegRate stays nil, evaluateSegments is a no-op, and
// EvaluateOnce behaves exactly as it did before Task 4 (zero blast radius).
func (g *Governor) WithSegments(setSegRate func(cc string, r float64), p SegParams) *Governor {
	g.setSegRate = setSegRate
	g.segParams = p
	g.capped = map[string]bool{}
	return g
}

// evaluateSegments runs one L3 pass: cap hot ccs at SlowRate, uncap ccs that
// were capped last round but are no longer hot. No-op when segments aren't
// enabled (setSegRate nil, i.e. WithSegments was never called).
func (g *Governor) evaluateSegments(ctx context.Context) error {
	if g.setSegRate == nil {
		return nil
	}
	stats, err := SegmentBanRates(ctx, g.pool, g.windowSec)
	if err != nil {
		return err
	}
	hot := hotSegments(stats, g.segParams)
	for cc := range hot {
		g.setSegRate(cc, g.segParams.SlowRate)
		g.capped[cc] = true
	}
	for cc := range g.capped {
		if !hot[cc] {
			g.setSegRate(cc, 0) // recovered → uncap
			delete(g.capped, cc)
		}
	}
	return nil
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
		if serr := g.evaluateSegments(ctx); serr != nil {
			log.Printf("dispatch: segment governor: %v", serr)
		}
		return g.current, false, nil // not enough signal; leave τ unchanged
	}
	g.current = nextRate(g.current, banRate, g.params)
	g.setRate(g.current)

	if serr := g.evaluateSegments(ctx); serr != nil {
		log.Printf("dispatch: segment governor: %v", serr)
	}
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
