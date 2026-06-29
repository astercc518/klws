package receipt

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Skipped unless WADIST_TEST_DSN points at a migrated database.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("WADIST_TEST_DSN")
	if dsn == "" {
		t.Skip("WADIST_TEST_DSN not set; skipping receipt DB integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedRecipient(t *testing.T, p *pgxpool.Pool, msgID, jid string) {
	t.Helper()
	ctx := context.Background()
	var tmpl int64
	if err := p.QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES (1,'text','hi') RETURNING id`).Scan(&tmpl); err != nil {
		t.Fatalf("template: %v", err)
	}
	var camp int64
	if err := p.QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state) VALUES (1,$1,'running') RETURNING id`, tmpl).Scan(&camp); err != nil {
		t.Fatalf("campaign: %v", err)
	}
	if _, err := p.Exec(ctx,
		`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, message_id, assigned_jid)
		 VALUES ($1,1,'+10000000000','US','sent',$2,$3)`, camp, msgID, jid); err != nil {
		t.Fatalf("recipient: %v", err)
	}
}

func times(t *testing.T, p *pgxpool.Pool, msgID string) (deliveredAt, readAt *time.Time) {
	t.Helper()
	if err := p.QueryRow(context.Background(),
		`SELECT delivered_at, read_at FROM campaign_recipients WHERE message_id=$1`, msgID).
		Scan(&deliveredAt, &readAt); err != nil {
		t.Fatalf("read times: %v", err)
	}
	return
}

func TestRecord(t *testing.T) {
	ctx := context.Background()
	p := testPool(t)
	p.Exec(ctx, `DELETE FROM campaign_recipients`)
	p.Exec(ctx, `DELETE FROM campaigns`)

	const jid = "62811@s.whatsapp.net"
	seedRecipient(t, p, "WAMSG1", jid)
	r := New(p)

	t1 := time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 6, 29, 10, 5, 0, 0, time.UTC)
	t3 := time.Date(2026, 6, 29, 11, 0, 0, 0, time.UTC)

	// delivered
	if err := r.Record(ctx, Event{MessageIDs: []string{"WAMSG1"}, SenderJID: jid, Kind: Delivered, At: t1}); err != nil {
		t.Fatalf("record delivered: %v", err)
	}
	d, rd := times(t, p, "WAMSG1")
	if d == nil || !d.Equal(t1) {
		t.Fatalf("delivered_at = %v, want %v", d, t1)
	}
	if rd != nil {
		t.Fatalf("read_at = %v, want nil", rd)
	}

	// read (later) — sets read_at; delivered_at unchanged (COALESCE keeps t1)
	if err := r.Record(ctx, Event{MessageIDs: []string{"WAMSG1"}, SenderJID: jid, Kind: Read, At: t2}); err != nil {
		t.Fatalf("record read: %v", err)
	}
	d, rd = times(t, p, "WAMSG1")
	if d == nil || !d.Equal(t1) {
		t.Errorf("delivered_at after read = %v, want unchanged %v", d, t1)
	}
	if rd == nil || !rd.Equal(t2) {
		t.Errorf("read_at = %v, want %v", rd, t2)
	}

	// idempotent: a later delivered receipt must NOT overwrite the earlier time
	if err := r.Record(ctx, Event{MessageIDs: []string{"WAMSG1"}, SenderJID: jid, Kind: Delivered, At: t3}); err != nil {
		t.Fatalf("record delivered again: %v", err)
	}
	d, _ = times(t, p, "WAMSG1")
	if d == nil || !d.Equal(t1) {
		t.Errorf("delivered_at after re-record = %v, want unchanged %v", d, t1)
	}

	// wrong JID disambiguation: no update
	if err := r.Record(ctx, Event{MessageIDs: []string{"WAMSG1"}, SenderJID: "other@s.whatsapp.net", Kind: Read, At: t3}); err != nil {
		t.Fatalf("record wrong jid: %v", err)
	}
	_, rd = times(t, p, "WAMSG1")
	if rd == nil || !rd.Equal(t2) {
		t.Errorf("read_at changed by wrong-jid receipt = %v, want %v", rd, t2)
	}
}
