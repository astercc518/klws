package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

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
	gate := sendgate.NewSendGate(pool, sendgate.NewAdmission(redisClient(t), sendgate.BackoffParams{}), 0)
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

func TestPump_SetRate(t *testing.T) {
	pe := NewPumpEnqueuer(1)
	p := NewPump(pe, nil, nil, 100, 2) // sender/resolve nil: not exercised here
	p.SetRate(50)
	if got := p.lim.Limit(); got != rate.Limit(50) {
		t.Fatalf("after SetRate(50) limit=%v; want 50", got)
	}
	p.SetRate(0) // <=0 → unlimited
	if got := p.lim.Limit(); got != rate.Inf {
		t.Fatalf("after SetRate(0) limit=%v; want Inf", got)
	}
}

func TestPump_SetSegmentRate(t *testing.T) {
	pe := NewPumpEnqueuer(1)
	p := NewPump(pe, nil, nil, 100, 2)
	if p.segLimiter("US") != nil {
		t.Fatalf("no segment limiter expected initially")
	}
	p.SetSegmentRate("US", 5)
	lim := p.segLimiter("US")
	if lim == nil || lim.Limit() != rate.Limit(5) {
		t.Fatalf("US segment limiter = %v; want rate 5", lim)
	}
	if p.segLimiter("GB") != nil {
		t.Fatalf("GB must be unaffected (nil)")
	}
	p.SetSegmentRate("US", 0) // uncap → removed
	if p.segLimiter("US") != nil {
		t.Fatalf("US segment limiter should be removed after SetSegmentRate(0)")
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

// TestPump_DrainsWhenWaitPredictsDeadline guards the deadline-PREDICT path of
// the drain-don't-drop contract, which is distinct from plain ctx cancellation.
//
// rate.Limiter.Wait returns a non-nil error while ctx.Err() is still nil when
// the ctx carries a *live* (future) deadline and the limiter predicts the
// required wait would exceed it ("rate: Wait(n=1) would exceed context
// deadline"). A guard of `err != nil && ctx.Err() != nil` is FALSE here, so a
// buggy pump would (a) skip acquiring a token and (b) hand ProcessSend the
// about-to-expire ctx — dropping committed-but-unsent buffered work.
//
// Construction: exhaust the limiter's startup burst token (Allow), enqueue one
// buffered payload, then Run with a live 100ms deadline. rate=0.5/sec ⇒ the
// next token is ~2s away, so Wait predict-errors immediately with ctx.Err()==nil.
// resolve sleeps 300ms (>100ms) so under the buggy path the ctx is expired by
// the time ProcessSend issues its first query → the send fails and the payload
// never reaches 'sent'. Under the correct fix (fall back to context.Background
// on ANY Wait error) the buffered payload completes regardless.
func TestPump_DrainsWhenWaitPredictsDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	rec, w, pool, campaignID, recipientIDs := newRecordingWorker(t)
	pe := NewPumpEnqueuer(8)
	resolve := func(ctx context.Context, campaignID int64) (string, string, string, []byte, error) {
		time.Sleep(300 * time.Millisecond) // outlast the 100ms deadline
		return "hi", "", "", nil, nil
	}
	p := NewPump(pe, w, resolve, 0.5 /*rate: ~2s per token*/, 1 /*workers*/)
	// Spend the single startup burst token so the buffered payload's Wait must
	// predict a ~2s delay (no token left), triggering the deadline-predict error.
	if !p.lim.Allow() {
		t.Fatal("expected to consume the startup burst token")
	}

	pl := SendPayload{
		TenantID: 1, CampaignID: campaignID, RecipientID: recipientIDs[0], JID: "j",
		Phone: "15550000000", Country: "US", MessageID: "drain-0",
	}
	if err := pe.EnqueueSend(context.Background(), pl, 0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	pe.Close() // closed-but-non-empty: the worker must drain the buffered payload

	// Live deadline in the near future: at the Wait call ctx.Err()==nil, but the
	// limiter predicts the ~2s wait exceeds this 100ms deadline → Wait errors.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	<-done // Run returns once the worker drains the closed channel and exits.

	var st string
	if err := pool.QueryRow(context.Background(),
		`SELECT state::text FROM campaign_recipients WHERE id=$1`, recipientIDs[0]).Scan(&st); err != nil {
		t.Fatalf("load recipient state: %v", err)
	}
	if st != "sent" {
		t.Fatalf("buffered payload state = %q; want \"sent\" (drain-don't-drop violated on deadline-predict path)", st)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("Sender.Send called %d times; want 1 (buffered send dropped)", got)
	}
}
