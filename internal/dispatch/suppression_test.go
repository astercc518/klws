package dispatch

import (
	"testing"
	"time"

	"github.com/acme/wadist/internal/crypto"
)

func TestDispatchBatch_SuppressedRecipientSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	q := &fakeQueue{}
	d := NewDispatcher(pool, nil, q, func(string) int64 { return 5 }, time.Second)

	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, pool, "a@s.whatsapp.net", "US", mature, 100, 0)
	cid := seedCampaign(t, ctx, pool, "hello")

	phone := "+15559990000"
	country := "US"

	// Insert recipient with phone_bidx set.
	blindKey := make([]byte, 32)
	for i := range blindKey {
		blindKey[i] = 0x42
	}
	bidx := crypto.BlindIndex(blindKey, phone)

	var rid int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, phone_bidx)
		VALUES ($1, 1, $2, $3, $4) RETURNING id`,
		cid, phone, country, bidx).Scan(&rid); err != nil {
		t.Fatalf("seed suppressed recipient: %v", err)
	}

	// Add to suppression_list for tenant 1.
	if _, err := pool.Exec(ctx, `
		INSERT INTO suppression_list (tenant_id, phone_bidx, reason)
		VALUES (1, $1, 'test suppression')`, bidx); err != nil {
		t.Fatalf("seed suppression_list: %v", err)
	}

	n, err := d.dispatchBatch(ctx, cid, 10)
	if err != nil {
		t.Fatalf("dispatchBatch: %v", err)
	}
	if n != 0 {
		t.Fatalf("assigned=%d, want 0 (suppressed recipient must not be sent)", n)
	}

	// Recipient must be in 'skipped' state.
	var state string
	pool.QueryRow(ctx, `SELECT state::text FROM campaign_recipients WHERE id=$1`, rid).Scan(&state)
	if state != "skipped" {
		t.Fatalf("state=%q, want skipped", state)
	}

	// Must not have been enqueued.
	if len(q.got) != 0 {
		t.Fatalf("enqueued=%d, want 0 (suppressed recipient must not be enqueued)", len(q.got))
	}
}

func TestDispatchBatch_NonSuppressedProceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	q := &fakeQueue{}
	d := NewDispatcher(pool, nil, q, func(string) int64 { return 5 }, time.Second)

	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, pool, "b@s.whatsapp.net", "US", mature, 100, 0)
	cid := seedCampaign(t, ctx, pool, "hello")

	phone := "+15551234567"
	country := "US"

	blindKey := make([]byte, 32)
	bidx := crypto.BlindIndex(blindKey, phone)

	var rid int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, phone_bidx)
		VALUES ($1, 1, $2, $3, $4) RETURNING id`,
		cid, phone, country, bidx).Scan(&rid); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}

	// No suppression entry for this recipient.

	n, err := d.dispatchBatch(ctx, cid, 10)
	if err != nil {
		t.Fatalf("dispatchBatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("assigned=%d, want 1 (non-suppressed recipient must proceed)", n)
	}
	if len(q.got) != 1 || q.got[0].RecipientID != rid {
		t.Fatalf("enqueued=%v, want [rid=%d]", q.got, rid)
	}
}
