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
