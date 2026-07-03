package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPump_EndToEnd is the M3 end-to-end regression: a running campaign's
// pending recipients flow through Dispatcher.DispatchRunning (fills the
// PumpEnqueuer) → Pump (token bucket + worker pool) → SendWorker.ProcessSend,
// reaching terminal 'sent' — while the M3-preserved invariants hold:
// sent_today symmetric accounting (bumped once at assign, untouched on
// success) and DispatchRunning idempotency (a second fill pass, with no
// pending recipients left, enqueues nothing).
//
// Harness: reuses newRecordingWorker (pump_test.go) for the real PG+Redis
// pool, real sendgate/billing-backed SendWorker with a recording fake
// Sender, and a seeded healthy account + running campaign with 5 pending
// recipients — the exact fixture TestPump_RateLimitsAndProcesses drives, but
// here the recipients are fed through the Dispatcher/PumpEnqueuer seam
// instead of being enqueued directly, proving the full pump dispatch mode
// path (Task 5 wiring) rather than just the Pump in isolation (Task 3).
func TestPump_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	rec, w, pool, campaignID, recipientIDs := newRecordingWorker(t)
	ctx := context.Background()

	pe := NewPumpEnqueuer(16)
	// dispatchBatch doesn't read the Dispatcher's billing field (charging
	// happens inside SendWorker.ProcessSend, wired separately above via
	// newRecordingWorker) — nil here mirrors batch_test.go's own Dispatcher
	// construction.
	d := NewDispatcher(pool, nil, pe, func(string) int64 { return 1 }, time.Second)
	resolve := func(_ context.Context, _ int64) (string, string, string, []byte, error) {
		return "hi", "", "", nil, nil
	}
	p := NewPump(pe, w, resolve, 1000 /*rate*/, 2 /*workers*/)

	pctx, cancel := context.WithCancel(ctx)
	go p.Run(pctx)

	// One fill pass: DispatchRunning scans the running campaign, assigns all
	// pending recipients to the healthy account, bumps sent_today, and pushes
	// each into the PumpEnqueuer.
	n, err := d.DispatchRunning(ctx, 16)
	if err != nil {
		t.Fatalf("DispatchRunning (fill): %v", err)
	}
	if n != len(recipientIDs) {
		t.Fatalf("DispatchRunning enqueued %d; want %d (all seeded pending recipients)", n, len(recipientIDs))
	}

	// Wait for the pump to fully drive every payload through ProcessSend to a
	// committed terminal state — poll DB state rather than racing the
	// in-flight Settle/markSent tail (mirrors pump_test.go's waitFor usage).
	waitFor(t, func() bool {
		var got int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM campaign_recipients WHERE campaign_id=$1 AND state='sent'`,
			campaignID).Scan(&got); err != nil {
			t.Fatalf("count sent: %v", err)
		}
		return got == len(recipientIDs)
	}, 3*time.Second)

	cancel()
	pe.Close()

	assertAllRecipientsSent(t, ctx, pool, campaignID, len(recipientIDs))

	if got := rec.count(); got != len(recipientIDs) {
		t.Fatalf("Sender.Send called %d times; want %d", got, len(recipientIDs))
	}

	// sent_today symmetric accounting: dispatchBatch bumped it once per
	// dispatched recipient at assign time, and success leaves it untouched
	// (TestProcessSend_SentTodayUnchangedOnSuccess covers the unit case) — so
	// after all sends succeed it must equal exactly the recipient count, with
	// no double-count and no lost bump.
	var sentToday int
	if err := pool.QueryRow(ctx,
		`SELECT sent_today FROM account_devices WHERE account_jid='j'`).Scan(&sentToday); err != nil {
		t.Fatalf("query sent_today: %v", err)
	}
	if sentToday != len(recipientIDs) {
		t.Fatalf("sent_today=%d; want %d (symmetric accounting)", sentToday, len(recipientIDs))
	}

	// Idempotency: no pending recipients remain, so a second fill pass must
	// enqueue nothing.
	n2, err := d.DispatchRunning(ctx, 16)
	if err != nil {
		t.Fatalf("DispatchRunning (second fill): %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second DispatchRunning enqueued %d; want 0 (idempotent, no pending left)", n2)
	}
}

// assertAllRecipientsSent fails the test unless every seeded recipient in the
// campaign is in terminal state 'sent'.
func assertAllRecipientsSent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, campaignID int64, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM campaign_recipients WHERE campaign_id=$1 AND state='sent'`,
		campaignID).Scan(&got); err != nil {
		t.Fatalf("count sent: %v", err)
	}
	if got != want {
		t.Fatalf("recipients in state='sent' = %d; want %d (all terminal)", got, want)
	}
}
