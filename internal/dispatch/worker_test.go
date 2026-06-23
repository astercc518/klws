package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/sendgate"
)

type fakeSender struct {
	err error
	id  string
}

func (f *fakeSender) Send(_ context.Context, jid, phone, body string, _ *MediaHandle) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.id, nil
}

func newWorker(t *testing.T) (*SendWorker, context.Context, *sendgate.SendGate, *billing.Repo, *fakeSender) {
	t.Helper()
	pool, ctx := pgPool(t)
	gate := sendgate.NewSendGate(pool, sendgate.NewAdmission(redisClient(t)), time.Second)
	br := billing.NewRepo(pool)
	fs := &fakeSender{id: "wamid.1"}
	return NewSendWorker(pool, gate, br, fs, &fakeUploader{}), ctx, gate, br, fs
}

func p(rid int64, jid string) SendPayload {
	return SendPayload{TenantID: 1, CampaignID: 1, RecipientID: rid, JID: jid, Phone: "1555", Country: "US", MessageID: "1:" + itoa(rid), Vars: map[string]any{"name": "Ada"}}
}
func itoa(i int64) string { return string(rune('0' + i)) }

func TestProcessSend_SuccessSettlesAndMarksSent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	w, ctx, _, br, _ := newWorker(t)
	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, w.pool, "a@s.whatsapp.net", "US", mature, 100, 0)
	br.Topup(ctx, 1, 1000, "seed")
	cid := seedCampaign(t, ctx, w.pool, "hi")
	rid := seedRecipient(t, ctx, w.pool, cid, "1555", "US")
	pl := SendPayload{TenantID: 1, CampaignID: cid, RecipientID: rid, JID: "a@s.whatsapp.net", Phone: "1555", Country: "US", MessageID: "m1", Vars: map[string]any{"name": "Ada"}}

	if err := w.ProcessSend(ctx, pl, "hi {{.name}}", "", "", nil); err != nil {
		t.Fatalf("process: %v", err)
	}
	var st, msg string
	w.pool.QueryRow(ctx, `SELECT state::text, message_id FROM campaign_recipients WHERE id=$1`, rid).Scan(&st, &msg)
	if st != "sent" || msg != "wamid.1" {
		t.Fatalf("recip st=%q msg=%q", st, msg)
	}
	var chState string
	w.pool.QueryRow(ctx, `SELECT state::text FROM billing_charges WHERE message_id='m1'`).Scan(&chState)
	if chState != "settled" {
		t.Fatalf("charge=%q, want settled", chState)
	}
}

func TestProcessSend_SendFailRefundsAndDropsHealth(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	w, ctx, _, br, fs := newWorker(t)
	fs.err = errors.New("wa_warning: blocked")
	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, w.pool, "a@s.whatsapp.net", "US", mature, 100, 0)
	br.Topup(ctx, 1, 1000, "seed")
	cid := seedCampaign(t, ctx, w.pool, "hi")
	rid := seedRecipient(t, ctx, w.pool, cid, "1555", "US")
	pl := SendPayload{TenantID: 1, CampaignID: cid, RecipientID: rid, JID: "a@s.whatsapp.net", Phone: "1555", Country: "US", MessageID: "m1", Vars: map[string]any{}}

	err := w.ProcessSend(ctx, pl, "hi", "", "", nil)
	if err == nil {
		t.Fatal("expected send error to surface for retry")
	}
	var st string
	w.pool.QueryRow(ctx, `SELECT state::text FROM campaign_recipients WHERE id=$1`, rid).Scan(&st)
	if st != "failed" {
		t.Fatalf("recip st=%q, want failed", st)
	}
	var ch string
	w.pool.QueryRow(ctx, `SELECT state::text FROM billing_charges WHERE message_id='m1'`).Scan(&ch)
	if ch != "refund_pending" {
		t.Fatalf("charge=%q, want refund_pending", ch)
	}
	var h int
	w.pool.QueryRow(ctx, `SELECT health_score FROM account_devices WHERE account_jid='a@s.whatsapp.net'`).Scan(&h)
	if h != 70 {
		t.Fatalf("health=%d, want 70 (wa_warning -30)", h)
	}
}

func TestProcessSend_AdmitDeniedRequeues(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	w, ctx, _, br, _ := newWorker(t)
	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, w.pool, "a@s.whatsapp.net", "US", mature, 100, 0)
	w.pool.Exec(ctx, `UPDATE account_devices SET quarantined_until=now()+interval '1 hour' WHERE account_jid='a@s.whatsapp.net'`)
	br.Topup(ctx, 1, 1000, "seed")
	cid := seedCampaign(t, ctx, w.pool, "hi")
	rid := seedRecipient(t, ctx, w.pool, cid, "1555", "US")
	w.pool.Exec(ctx, `UPDATE campaign_recipients SET assigned_jid='a@s.whatsapp.net', state='pending' WHERE id=$1`, rid)
	pl := SendPayload{TenantID: 1, CampaignID: cid, RecipientID: rid, JID: "a@s.whatsapp.net", Phone: "1555", Country: "US", MessageID: "m1", Vars: map[string]any{}}

	if err := w.ProcessSend(ctx, pl, "hi", "", "", nil); err != nil {
		t.Fatalf("process (deny should not error): %v", err)
	}
	var st string
	var jid *string
	w.pool.QueryRow(ctx, `SELECT state::text, assigned_jid FROM campaign_recipients WHERE id=$1`, rid).Scan(&st, &jid)
	if st != "pending" || jid != nil {
		t.Fatalf("recip st=%q jid=%v, want pending/NULL", st, jid)
	}
	var n int
	w.pool.QueryRow(ctx, `SELECT count(*) FROM billing_charges WHERE message_id='m1'`).Scan(&n)
	if n != 0 {
		t.Fatalf("charge count=%d, want 0 (no hold on deny)", n)
	}
}
