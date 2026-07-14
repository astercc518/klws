package metrics

import (
	"context"
	"testing"
	"time"
)

// pgPool + applyMigrations are provided by dbcollector_testsupport_test.go
// (shared testcontainers harness for this package).

func TestSamplerAggregatesAccountsAndRecipients(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v (%s)", err, sql)
		}
	}

	// account_devices: total=5, active=2 (a1 active, a4 active+quarantined),
	// banned=2 (a2 banned, a3 flagged), quarantined=1 (a4 only — quarantined
	// is independent of ban_status per spec: quarantined_until > now()).
	mustExec(`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status) VALUES (1,'a1@s.whatsapp.net','1','active')`)
	mustExec(`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status) VALUES (1,'a2@s.whatsapp.net','2','banned')`)
	mustExec(`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status) VALUES (1,'a3@s.whatsapp.net','3','flagged')`)
	mustExec(`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status, quarantined_until) VALUES (1,'a4@s.whatsapp.net','4','active', now() + interval '1 hour')`)
	mustExec(`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status) VALUES (1,'a5@s.whatsapp.net','5','logged_out')`)

	var templateID, campaignID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES (1,'text','hi') RETURNING id`,
	).Scan(&templateID); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	// campaigns.created_at is the proxy anchor for avg_delivery_ms (see sampler.go
	// doc comment: campaign_recipients has no created_at column). Set 20 minutes
	// in the past so recipient p5's delivery delay is a known, assertable value.
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state, created_at) VALUES (1,$1,'running', now() - interval '20 minutes') RETURNING id`,
		templateID,
	).Scan(&campaignID); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}

	// p1: pending → queue_backlog.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state) VALUES ($1,1,'p1','US','pending')`, campaignID)
	// p2: sent, updated recently → processed_1h.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, updated_at) VALUES ($1,1,'p2','US','sent', now() - interval '10 minutes')`, campaignID)
	// p3: failed, updated recently → processed_1h + failed_1h.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, updated_at) VALUES ($1,1,'p3','US','failed', now() - interval '5 minutes')`, campaignID)
	// p4: skipped, updated recently → processed_1h.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, updated_at) VALUES ($1,1,'p4','US','skipped', now() - interval '30 minutes')`, campaignID)
	// p5: sent + delivered recently → processed_1h + delivered_1h; delay = delivered_at
	// (-10min) - campaign.created_at (-20min) = 10 minutes = 600000ms.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, updated_at, delivered_at) VALUES ($1,1,'p5','US','sent', now() - interval '10 minutes', now() - interval '10 minutes')`, campaignID)
	// p6: sent + delivered, but both 2h old → outside every 1h window, must not
	// contaminate processed_1h/delivered_1h/avg_delivery_ms.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, updated_at, delivered_at) VALUES ($1,1,'p6','US','sent', now() - interval '2 hours', now() - interval '2 hours')`, campaignID)

	store := NewStore(pool)
	s := NewSampler(store, pool, time.Minute, 90)
	snap, err := s.SampleOnce(ctx)
	if err != nil {
		t.Fatalf("SampleOnce: %v", err)
	}

	if snap.AccountsTotal != 5 {
		t.Errorf("AccountsTotal = %d, want 5", snap.AccountsTotal)
	}
	if snap.AccountsActive != 2 {
		t.Errorf("AccountsActive = %d, want 2", snap.AccountsActive)
	}
	if snap.AccountsBanned != 2 {
		t.Errorf("AccountsBanned = %d, want 2", snap.AccountsBanned)
	}
	if snap.AccountsQuarantined != 1 {
		t.Errorf("AccountsQuarantined = %d, want 1", snap.AccountsQuarantined)
	}
	if snap.QueueBacklog != 1 {
		t.Errorf("QueueBacklog = %d, want 1", snap.QueueBacklog)
	}
	if snap.Processed1h != 4 { // p2 sent, p3 failed, p4 skipped, p5 sent
		t.Errorf("Processed1h = %d, want 4", snap.Processed1h)
	}
	if snap.Delivered1h != 1 { // only p5
		t.Errorf("Delivered1h = %d, want 1", snap.Delivered1h)
	}
	if snap.Failed1h != 1 { // p3
		t.Errorf("Failed1h = %d, want 1", snap.Failed1h)
	}
	if snap.AvgDeliveryMs == nil {
		t.Fatal("AvgDeliveryMs: expected non-nil (p5 delivered within the last hour)")
	} else {
		got := *snap.AvgDeliveryMs
		want := 10 * 60 * 1000
		// slack for the wall-clock drift between the two now() calls used to seed p5.
		if got < want-3000 || got > want+3000 {
			t.Errorf("AvgDeliveryMs = %d, want ~%d (±3s)", got, want)
		}
	}
}

func TestSamplerAvgDeliveryMsNilWhenNoRecentDeliveries(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v (%s)", err, sql)
		}
	}
	mustExec(`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status) VALUES (1,'a1@s.whatsapp.net','1','active')`)

	var templateID, campaignID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES (1,'text','hi') RETURNING id`,
	).Scan(&templateID); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state, created_at) VALUES (1,$1,'running', now() - interval '3 hours') RETURNING id`,
		templateID,
	).Scan(&campaignID); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	// only an old delivery (outside the 1h window).
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, updated_at, delivered_at) VALUES ($1,1,'p1','US','sent', now() - interval '2 hours', now() - interval '2 hours')`, campaignID)
	// a pending recipient with no delivered_at at all.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state) VALUES ($1,1,'p2','US','pending')`, campaignID)

	store := NewStore(pool)
	s := NewSampler(store, pool, time.Minute, 90)
	snap, err := s.SampleOnce(ctx)
	if err != nil {
		t.Fatalf("SampleOnce: %v", err)
	}
	if snap.Delivered1h != 0 {
		t.Errorf("Delivered1h = %d, want 0", snap.Delivered1h)
	}
	if snap.AvgDeliveryMs != nil {
		t.Errorf("AvgDeliveryMs = %v, want nil (no deliveries in the last hour)", *snap.AvgDeliveryMs)
	}
}

func TestSamplerRunLoopInsertsAndExitsOnCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	if _, err := pool.Exec(ctx,
		`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status) VALUES (1,'a1@s.whatsapp.net','1','active')`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	store := NewStore(pool)
	s := NewSampler(store, pool, 20*time.Millisecond, 90)

	loopCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if err := s.RunLoop(loopCtx); err == nil {
		t.Fatal("RunLoop: expected a non-nil error when ctx is cancelled/times out")
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM metric_snapshots`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n == 0 {
		t.Fatal("RunLoop: expected at least one snapshot inserted before cancellation")
	}
}
