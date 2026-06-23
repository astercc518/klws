package dispatch

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeQueue struct {
	mu sync.Mutex
	got []SendPayload
}
func (q *fakeQueue) EnqueueSend(_ context.Context, p SendPayload, _ time.Duration) error {
	q.mu.Lock(); defer q.mu.Unlock(); q.got = append(q.got, p); return nil
}

func TestDispatchBatch_AssignsBumpsEnqueues(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	pool, ctx := pgPool(t)
	q := &fakeQueue{}
	d := NewDispatcher(pool, nil, q, func(string) int64 { return 5 }, time.Second)
	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, pool, "a@s.whatsapp.net", "US", mature, 100, 0)
	cid := seedCampaign(t, ctx, pool, "hello {{.name}}")
	rid := seedRecipient(t, ctx, pool, cid, "15551112222", "US")

	n, err := d.dispatchBatch(ctx, cid, 10)
	if err != nil { t.Fatalf("dispatch: %v", err) }
	if n != 1 { t.Fatalf("assigned=%d, want 1", n) }
	// recipient assigned + message_id set
	var st, jid, msg string
	pool.QueryRow(ctx, `SELECT state::text, assigned_jid, message_id FROM campaign_recipients WHERE id=$1`, rid).Scan(&st, &jid, &msg)
	if st != "pending" { t.Fatalf("state=%q, want pending (no assigned state in enum)", st) }
	if jid != "a@s.whatsapp.net" || msg == "" { t.Fatalf("recip jid=%q msg=%q", jid, msg) }
	// sent_today bumped
	var sent int
	pool.QueryRow(ctx, `SELECT sent_today FROM account_devices WHERE account_jid='a@s.whatsapp.net'`).Scan(&sent)
	if sent != 1 { t.Fatalf("sent_today=%d, want 1", sent) }
	// enqueued
	if len(q.got) != 1 || q.got[0].RecipientID != rid { t.Fatalf("enqueued=%v", q.got) }
}

func TestDispatchBatch_NoCapacityLeavesPending(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	pool, ctx := pgPool(t)
	q := &fakeQueue{}
	d := NewDispatcher(pool, nil, q, func(string) int64 { return 5 }, time.Second)
	// no US accounts at all
	cid := seedCampaign(t, ctx, pool, "x")
	rid := seedRecipient(t, ctx, pool, cid, "15551112222", "US")
	n, err := d.dispatchBatch(ctx, cid, 10)
	if err != nil { t.Fatalf("dispatch: %v", err) }
	if n != 0 { t.Fatalf("assigned=%d, want 0", n) }
	var st string
	pool.QueryRow(ctx, `SELECT state::text FROM campaign_recipients WHERE id=$1`, rid).Scan(&st)
	if st != "pending" { t.Fatalf("state=%q, want pending", st) }
	if len(q.got) != 0 { t.Fatalf("should not enqueue") }
}

func TestDispatchBatch_AssignedNotRepulled(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	pool, ctx := pgPool(t)
	q1 := &fakeQueue{}
	d := NewDispatcher(pool, nil, q1, func(string) int64 { return 5 }, time.Second)
	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, pool, "a@s.whatsapp.net", "US", mature, 100, 0)
	cid := seedCampaign(t, ctx, pool, "hello {{.name}}")
	rid := seedRecipient(t, ctx, pool, cid, "15551112222", "US")

	// First dispatch: should assign the recipient.
	n, err := d.dispatchBatch(ctx, cid, 10)
	if err != nil { t.Fatalf("first dispatch: %v", err) }
	if n != 1 { t.Fatalf("first dispatch assigned=%d, want 1", n) }

	// Verify: assigned_jid set, message_id set, sent_today==1, enqueued once.
	var jid, msg string
	pool.QueryRow(ctx, `SELECT assigned_jid, message_id FROM campaign_recipients WHERE id=$1`, rid).Scan(&jid, &msg)
	if jid == "" || msg == "" { t.Fatalf("after first dispatch: jid=%q msg=%q", jid, msg) }
	var sent int
	pool.QueryRow(ctx, `SELECT sent_today FROM account_devices WHERE account_jid='a@s.whatsapp.net'`).Scan(&sent)
	if sent != 1 { t.Fatalf("after first dispatch: sent_today=%d, want 1", sent) }
	if len(q1.got) != 1 { t.Fatalf("after first dispatch: enqueued=%d, want 1", len(q1.got)) }

	// Second dispatch with a fresh queue: already-assigned recipient must NOT be re-pulled.
	q2 := &fakeQueue{}
	d2 := NewDispatcher(pool, nil, q2, func(string) int64 { return 5 }, time.Second)
	n2, err := d2.dispatchBatch(ctx, cid, 10)
	if err != nil { t.Fatalf("second dispatch: %v", err) }
	if n2 != 0 { t.Fatalf("second dispatch assigned=%d, want 0 (no duplicate)", n2) }

	// sent_today must still be 1 (no double bump).
	pool.QueryRow(ctx, `SELECT sent_today FROM account_devices WHERE account_jid='a@s.whatsapp.net'`).Scan(&sent)
	if sent != 1 { t.Fatalf("after second dispatch: sent_today=%d, want still 1", sent) }
	// No duplicate enqueue.
	if len(q2.got) != 0 { t.Fatalf("after second dispatch: enqueued=%d, want 0", len(q2.got)) }
}
