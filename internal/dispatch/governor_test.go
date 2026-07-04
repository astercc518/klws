package dispatch

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNextRate_AIMD(t *testing.T) {
	p := GovParams{SLO: 0.02, Step: 5, Factor: 0.5, MinRate: 1, MaxRate: 160}
	cases := []struct {
		name             string
		current, banRate float64
		want             float64
	}{
		{"healthy additive increase", 100, 0.00, 105},
		{"healthy capped at max", 158, 0.01, 160},
		{"over SLO multiplicative decrease", 100, 0.05, 50},
		{"decrease floored at min", 1.5, 0.9, 1}, // 1.5*0.5=0.75 → floored to 1
		{"exactly at SLO decreases", 80, 0.02, 40},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextRate(c.current, c.banRate, p); math.Abs(got-c.want) > 1e-9 {
				t.Fatalf("nextRate(%v,%v) = %v; want %v", c.current, c.banRate, got, c.want)
			}
		})
	}
}

// seedFleetOutcomes inserts nSent recipients in state='sent' and nBanFailed
// recipients in state='failed' with a last_error carrying a ban signal
// ('wa_warning'), all with updated_at=now(), so FleetBanRate/Governor can read
// them back within its window. Builds on seedCampaign/seedRecipient plus a
// targeted UPDATE (INSERT defaults state to 'pending' and leaves last_error
// NULL, so the outcome has to be applied after the fact).
func seedFleetOutcomes(t *testing.T, pool *pgxpool.Pool, nSent, nBanFailed int) {
	t.Helper()
	ctx := context.Background()
	cid := seedCampaign(t, ctx, pool, "fleet outcome seed")
	for i := 0; i < nSent; i++ {
		rid := seedRecipient(t, ctx, pool, cid, fmt.Sprintf("1555000%04d", i), "US")
		if _, err := pool.Exec(ctx,
			`UPDATE campaign_recipients SET state='sent', updated_at=now() WHERE id=$1`, rid); err != nil {
			t.Fatalf("mark sent: %v", err)
		}
	}
	for i := 0; i < nBanFailed; i++ {
		rid := seedRecipient(t, ctx, pool, cid, fmt.Sprintf("1555001%04d", i), "US")
		if _, err := pool.Exec(ctx,
			`UPDATE campaign_recipients SET state='failed', last_error='wa_warning: account banned', updated_at=now() WHERE id=$1`, rid); err != nil {
			t.Fatalf("mark ban-failed: %v", err)
		}
	}
}

// seedFleetOutcomesCC is seedFleetOutcomes but pins country_code=cc on every
// seeded recipient, so tests can assert per-country grouping (SegmentBanRates).
func seedFleetOutcomesCC(t *testing.T, pool *pgxpool.Pool, cc string, nSent, nBanFailed int) {
	t.Helper()
	ctx := context.Background()
	cid := seedCampaign(t, ctx, pool, "fleet outcome seed "+cc)
	for i := 0; i < nSent; i++ {
		rid := seedRecipient(t, ctx, pool, cid, fmt.Sprintf("1555000%s%04d", cc, i), cc)
		if _, err := pool.Exec(ctx,
			`UPDATE campaign_recipients SET state='sent', updated_at=now() WHERE id=$1`, rid); err != nil {
			t.Fatalf("mark sent: %v", err)
		}
	}
	for i := 0; i < nBanFailed; i++ {
		rid := seedRecipient(t, ctx, pool, cid, fmt.Sprintf("1555001%s%04d", cc, i), cc)
		if _, err := pool.Exec(ctx,
			`UPDATE campaign_recipients SET state='failed', last_error='wa_warning: account banned', updated_at=now() WHERE id=$1`, rid); err != nil {
			t.Fatalf("mark ban-failed: %v", err)
		}
	}
}

// TestSegmentBanRates asserts SegmentBanRates groups definition-A attempted/
// ban-failure counts by country_code, mirroring FleetBanRate's fleet-wide
// query but per segment.
func TestSegmentBanRates(t *testing.T) {
	pool, ctx := pgPool(t)
	// US: 3 sent + 2 ban-failed ; GB: 5 sent + 0 ban-failed
	seedFleetOutcomesCC(t, pool, "US", 3, 2)
	seedFleetOutcomesCC(t, pool, "GB", 5, 0)

	stats, err := SegmentBanRates(ctx, pool, 900)
	if err != nil {
		t.Fatalf("SegmentBanRates: %v", err)
	}
	m := map[string]SegStat{}
	for _, s := range stats {
		m[s.CC] = s
	}
	if m["US"].Attempted != 5 || m["US"].BanFailures != 2 {
		t.Fatalf("US = %+v; want attempted 5 banFailures 2", m["US"])
	}
	if m["GB"].Attempted != 5 || m["GB"].BanFailures != 0 {
		t.Fatalf("GB = %+v; want attempted 5 banFailures 0", m["GB"])
	}
}

// TestGovernor_HealRecovers asserts the AIMD loop is bidirectional: a burst of
// over-SLO ban outcomes decreases τ, and once the fleet heals (enough clean
// sends land in the window to push banRate back below SLO) the next
// EvaluateOnce increases τ by Step. Reuses the seedFleetOutcomes helper above.
func TestGovernor_HealRecovers(t *testing.T) {
	pool, ctx := pgPool(t)
	var last float64
	params := GovParams{SLO: 0.02, Step: 5, Factor: 0.5, MinRate: 1, MaxRate: 160}
	g := NewGovernor(pool, func(r float64) { last = r }, params, 900, 3, 100)

	seedFleetOutcomes(t, pool, 2, 3) // attempted=5, banFailed=3 → banRate=0.6 >= SLO → decrease
	if r, applied, err := g.EvaluateOnce(ctx); err != nil || !applied || r != 50 {
		t.Fatalf("decrease: r=%v applied=%v err=%v; want 50,true,nil", r, applied, err)
	}

	// Heal: add many clean sends so the window's banRate falls below SLO.
	seedFleetOutcomes(t, pool, 300, 0)
	r, applied, err := g.EvaluateOnce(ctx)
	if err != nil || !applied || r != 55 { // 50 + Step(5)
		t.Fatalf("recover: r=%v applied=%v err=%v; want 55,true,nil", r, applied, err)
	}
	if last != 55 {
		t.Fatalf("setRate got %v; want 55", last)
	}
}

func TestGovernor_EvaluateOnce(t *testing.T) {
	pool, ctx := pgPool(t)
	var setTo float64
	params := GovParams{SLO: 0.02, Step: 5, Factor: 0.5, MinRate: 1, MaxRate: 160}
	g := NewGovernor(pool, func(r float64) { setTo = r }, params, 900 /*window*/, 3 /*minSample*/, 100 /*current*/)

	// too few samples → skip (applied=false), τ unchanged, setRate not called.
	if r, applied, err := g.EvaluateOnce(ctx); err != nil || applied || r != 100 {
		t.Fatalf("no data: r=%v applied=%v err=%v; want 100,false,nil", r, applied, err)
	}
	if setTo != 0 {
		t.Fatalf("setRate should not have been called yet; got %v", setTo)
	}

	// seed 5 attempted, 3 ban-failures (banRate=0.6 >= SLO) → decrease to 50.
	seedFleetOutcomes(t, pool, 2 /*sent*/, 3 /*ban-failed*/)
	r, applied, err := g.EvaluateOnce(ctx)
	if err != nil || !applied || r != 50 {
		t.Fatalf("over-SLO: r=%v applied=%v err=%v; want 50,true,nil", r, applied, err)
	}
	if setTo != 50 {
		t.Fatalf("setRate got %v; want 50", setTo)
	}
}
