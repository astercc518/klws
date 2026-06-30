package riskbreaker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests exercise the real SQL against a throwaway Postgres. They are
// skipped unless WADIST_TEST_DSN points at a migrated database, so a plain
// `go test ./...` without a DB stays green.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("WADIST_TEST_DSN")
	if dsn == "" {
		t.Skip("WADIST_TEST_DSN not set; skipping riskbreaker DB integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func mustExec(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := p.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func insertCampaign(t *testing.T, p *pgxpool.Pool, tmpl int64, state string) int64 {
	t.Helper()
	var id int64
	if err := p.QueryRow(context.Background(),
		`INSERT INTO campaigns (tenant_id, template_id, state) VALUES (1, $1, $2) RETURNING id`,
		tmpl, state).Scan(&id); err != nil {
		t.Fatalf("insert campaign: %v", err)
	}
	return id
}

// seedRecipients inserts n rows in the given state; if banErr, their last_error
// carries a ban signal.
func seedRecipients(t *testing.T, p *pgxpool.Pool, campaignID int64, state string, n int, banErr bool) {
	t.Helper()
	lastErr := "delivered ok"
	if banErr {
		lastErr = "send failed: wa_warning (403)"
	}
	mustExec(t, p, `
INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, last_error, updated_at)
SELECT $1, 1, $2 || g::text, 'US', $3, $4, now()
  FROM generate_series(1, $5) g`,
		campaignID, state+"-", state, lastErr, n)
}

func campaignState(t *testing.T, p *pgxpool.Pool, id int64) string {
	t.Helper()
	var s string
	if err := p.QueryRow(context.Background(), `SELECT state::text FROM campaigns WHERE id=$1`, id).Scan(&s); err != nil {
		t.Fatalf("get state: %v", err)
	}
	return s
}

func cleanSlate(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	mustExec(t, p, `DELETE FROM campaign_recipients`)
	mustExec(t, p, `DELETE FROM audit_log WHERE action LIKE 'campaign.circuit_break%'`)
	mustExec(t, p, `DELETE FROM campaigns`)
}

func testTemplate(t *testing.T, p *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := p.QueryRow(context.Background(),
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES (1, 'text', 'hi') RETURNING id`).
		Scan(&id); err != nil {
		t.Fatalf("insert template: %v", err)
	}
	return id
}

func TestEvaluateOnce_TripsOverThreshold(t *testing.T) {
	ctx := context.Background()
	p := testPool(t)
	cleanSlate(t, p)
	tmpl := testTemplate(t, p)
	b := New(p)

	cfg := config{threshold: 0.15, enabled: true, dryRun: false, minSample: 20, windowSec: 900, evalInterval: time.Second}

	// A: 20 ban-failed + 10 sent => rate 0.667, sample 30 -> trips.
	a := insertCampaign(t, p, tmpl, "running")
	seedRecipients(t, p, a, "failed", 20, true)
	seedRecipients(t, p, a, "sent", 10, false)
	// B: 30 sent, 0 ban => no trip.
	bID := insertCampaign(t, p, tmpl, "running")
	seedRecipients(t, p, bID, "sent", 30, false)
	// C: 5 ban-failed => over threshold but under minSample -> no trip.
	cID := insertCampaign(t, p, tmpl, "running")
	seedRecipients(t, p, cID, "failed", 5, true)

	tripped, err := b.EvaluateOnce(ctx, cfg)
	if err != nil {
		t.Fatalf("EvaluateOnce: %v", err)
	}
	if tripped != 1 {
		t.Fatalf("tripped = %d, want 1", tripped)
	}
	if got := campaignState(t, p, a); got != "paused" {
		t.Errorf("campaign A state = %q, want paused", got)
	}
	if got := campaignState(t, p, bID); got != "running" {
		t.Errorf("campaign B state = %q, want running", got)
	}
	if got := campaignState(t, p, cID); got != "running" {
		t.Errorf("campaign C (under sample) state = %q, want running", got)
	}

	// Audit row written for A.
	var n int
	if err := p.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action='campaign.circuit_break' AND resource_id=$1`, a).Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	if n != 1 {
		t.Errorf("audit rows for A = %d, want 1", n)
	}

	// Idempotent: A is now paused, so a second pass trips nothing.
	tripped2, err := b.EvaluateOnce(ctx, cfg)
	if err != nil {
		t.Fatalf("EvaluateOnce 2: %v", err)
	}
	if tripped2 != 0 {
		t.Errorf("second pass tripped = %d, want 0", tripped2)
	}
}

func TestEvaluateOnce_DryRunDoesNotPause(t *testing.T) {
	ctx := context.Background()
	p := testPool(t)
	cleanSlate(t, p)
	tmpl := testTemplate(t, p)
	b := New(p)

	cfg := config{threshold: 0.15, enabled: true, dryRun: true, minSample: 20, windowSec: 900, evalInterval: time.Second}
	a := insertCampaign(t, p, tmpl, "running")
	seedRecipients(t, p, a, "failed", 25, true)

	tripped, err := b.EvaluateOnce(ctx, cfg)
	if err != nil {
		t.Fatalf("EvaluateOnce: %v", err)
	}
	if tripped != 0 {
		t.Errorf("dry-run tripped = %d, want 0", tripped)
	}
	if got := campaignState(t, p, a); got != "running" {
		t.Errorf("dry-run left state = %q, want running", got)
	}
}
