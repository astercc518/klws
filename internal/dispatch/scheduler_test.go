package dispatch

import (
	"context"
	"testing"
	"time"
)

// setupRunningCampaignCase seeds a real PG+Redis environment with one
// 'running' campaign and a pending recipient, and returns a configured
// Dispatcher and background context for use in integration tests.
func setupRunningCampaignCase(t *testing.T) (*Dispatcher, context.Context) {
	t.Helper()
	pool, ctx := pgPool(t)
	q := &fakeQueue{}
	d := NewDispatcher(pool, nil, q, func(string) int64 { return 5 }, time.Second)

	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, pool, "b@s.whatsapp.net", "IN", mature, 100, 0)
	cid := seedCampaign(t, ctx, pool, "hello world") // seedCampaign defaults state='running'
	seedRecipient(t, ctx, pool, cid, "919900000001", "IN")

	return d, ctx
}

func TestDispatchRunning_DispatchesRunningCampaigns(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	d, ctx := setupRunningCampaignCase(t)
	n, err := d.DispatchRunning(ctx, 10)
	if err != nil {
		t.Fatalf("DispatchRunning: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected >=1 dispatched across running campaigns, got %d", n)
	}
}

// TestDispatchRunningBudget_CapsTotalAcrossCampaigns proves DispatchRunningBudget
// caps enqueues to `budget` TOTAL across ALL running campaigns — unlike
// DispatchRunning, which would hand each campaign the full batch (and thus, in
// pump mode with a bounded channel, could push more payloads than there are
// free slots, triggering a mid-batch ErrPumpFull rollback that orphans
// already-pushed channel payloads; see B1 in the M3 review).
//
// Seeds TWO running campaigns, each with more pending recipients than half the
// budget, and asserts the total assigned across both equals exactly the
// budget — not 2x the budget.
func TestDispatchRunningBudget_CapsTotalAcrossCampaigns(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	q := &fakeQueue{}
	d := NewDispatcher(pool, nil, q, func(string) int64 { return 5 }, time.Second)

	mature := time.Now().Add(-30 * 24 * time.Hour)
	// Two independent healthy accounts (one per campaign's tenant-agnostic
	// selection pool) with plenty of quota headroom so account capacity is
	// never the limiting factor — only the budget is.
	seedAccount(t, ctx, pool, "budget-a@s.whatsapp.net", "IN", mature, 100, 0)
	seedAccount(t, ctx, pool, "budget-b@s.whatsapp.net", "IN", mature, 100, 0)

	cid1 := seedCampaign(t, ctx, pool, "hello world 1")
	cid2 := seedCampaign(t, ctx, pool, "hello world 2")
	for i := 0; i < 5; i++ {
		seedRecipient(t, ctx, pool, cid1, "91990000"+pad2(i), "IN")
		seedRecipient(t, ctx, pool, cid2, "91990001"+pad2(i), "IN")
	}
	// 10 pending recipients total across both campaigns; budget well below that.
	const budget = 4

	n, err := d.DispatchRunningBudget(ctx, budget)
	if err != nil {
		t.Fatalf("DispatchRunningBudget: %v", err)
	}
	if n != budget {
		t.Fatalf("DispatchRunningBudget returned %d; want exactly %d (total budget, not per-campaign)", n, budget)
	}

	var assignedCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM campaign_recipients WHERE campaign_id IN ($1,$2) AND assigned_jid IS NOT NULL`,
		cid1, cid2).Scan(&assignedCount); err != nil {
		t.Fatalf("count assigned: %v", err)
	}
	if assignedCount != budget {
		t.Fatalf("assigned recipient rows = %d; want exactly %d (budget capped across BOTH campaigns combined)", assignedCount, budget)
	}
}

func pad2(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0' + i))
}
