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
