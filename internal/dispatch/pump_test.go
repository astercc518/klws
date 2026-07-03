package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/sendgate"
)

func TestPumpEnqueuer_FullReturnsErr(t *testing.T) {
	p := NewPumpEnqueuer(1)
	ctx := context.Background()
	if err := p.EnqueueSend(ctx, SendPayload{RecipientID: 1}, 0); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	// buffer=1 now full → non-blocking, returns ErrPumpFull (does NOT block).
	if err := p.EnqueueSend(ctx, SendPayload{RecipientID: 2}, 0); !errors.Is(err, ErrPumpFull) {
		t.Fatalf("second enqueue err = %v; want ErrPumpFull", err)
	}
	got := <-p.C()
	if got.RecipientID != 1 {
		t.Fatalf("drained %d; want 1", got.RecipientID)
	}
}

// recordingSender wraps a Sender and atomically counts calls. fakeSender in
// worker_test.go increments a plain int, which races under the Pump's
// concurrent workers (-race), so the Pump test needs its own thread-safe
// recorder rather than reusing fakeSender directly.
type recordingSender struct {
	id string
	n  int64
}

func (r *recordingSender) Send(_ context.Context, _, _, _ string, _ *MediaHandle) (string, error) {
	atomic.AddInt64(&r.n, 1)
	return r.id, nil
}

func (r *recordingSender) count() int { return int(atomic.LoadInt64(&r.n)) }

// newRecordingWorker builds a real SendWorker — same Postgres+Redis test
// container wiring as newWorker in worker_test.go (gate/billing are real;
// only the Sender is faked) — with one active account (jid "j") and a
// campaign with 5 pending recipients, so ProcessSend runs the full
// admit→hold→send→settle path when the Pump drives it. The pool is returned
// so the test can assert on committed state (state='sent') rather than
// racing the in-flight Settle/markSent tail of ProcessSend against Send.
func newRecordingWorker(t *testing.T) (rec *recordingSender, w *SendWorker, pool *pgxpool.Pool, campaignID int64, recipientIDs []int64) {
	t.Helper()
	var ctx context.Context
	pool, ctx = pgPool(t)
	// baseGap=0 disables sendgate's human-pacing jitter: this test exercises
	// the Pump's own token-bucket pacing/concurrency, not sendgate's.
	gate := sendgate.NewSendGate(pool, sendgate.NewAdmission(redisClient(t)), 0)
	br := billing.NewRepo(pool)
	if err := br.Topup(ctx, 1, 1000, "seed"); err != nil {
		t.Fatalf("topup: %v", err)
	}
	rec = &recordingSender{id: "wamid.pump"}
	w = NewSendWorker(pool, gate, br, rec, &fakeUploader{})

	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, pool, "j", "US", mature, 100, 0)
	campaignID = seedCampaign(t, ctx, pool, "hi")
	for i := 0; i < 5; i++ {
		phone := fmt.Sprintf("155500000%02d", i)
		recipientIDs = append(recipientIDs, seedRecipient(t, ctx, pool, campaignID, phone, "US"))
	}
	return rec, w, pool, campaignID, recipientIDs
}

// waitFor polls cond until it's true or timeout elapses, failing the test if
// it never becomes true. Used to await async pump processing without a fixed
// sleep.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

func TestPump_RateLimitsAndProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	rec, w, pool, campaignID, recipientIDs := newRecordingWorker(t)
	pe := NewPumpEnqueuer(8)
	resolve := func(ctx context.Context, campaignID int64) (string, string, string, []byte, error) {
		return "hi", "", "", nil, nil
	}
	p := NewPump(pe, w, resolve, 1000 /*rate*/, 2 /*workers*/)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	for i, rid := range recipientIDs {
		pl := SendPayload{
			TenantID: 1, CampaignID: campaignID, RecipientID: rid, JID: "j",
			Phone: fmt.Sprintf("155500000%02d", i), Country: "US", MessageID: fmt.Sprintf("pump-%d", i),
		}
		if err := pe.EnqueueSend(ctx, pl, 0); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	// Wait until all 5 payloads have fully flowed through ProcessSend, i.e. are
	// committed 'sent' (not just past Sender.Send — Settle/markSent still run
	// after that), then shut down: cancel the pump ctx and close the
	// enqueuer's channel (the owner's shutdown contract — Task 5 wiring does
	// this in production).
	waitFor(t, func() bool {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id=$1 AND state='sent'`, campaignID).Scan(&n); err != nil {
			t.Fatalf("count sent: %v", err)
		}
		return n == len(recipientIDs)
	}, 2*time.Second)
	if got := rec.count(); got != len(recipientIDs) {
		t.Fatalf("Sender.Send called %d times; want %d", got, len(recipientIDs))
	}
	cancel()
	pe.Close()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned err = %v; want nil or context.Canceled", err)
	}
}
