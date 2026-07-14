// internal/api/metrics_db_test.go — TDD tests for P3 task-3:
// GET /admin/risk/overview + GET /admin/risk/accounts (real-time DB
// aggregation over account_devices / campaign_recipients, no snapshot
// reads). Mirrors the testcontainers harness used by instances_db_test.go /
// audit_db_test.go: a raw *pgxpool.Pool wired via Server.sysPool (no
// store.Manager needed — these handlers only issue SystemPool SQL).
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// seedRiskAccount inserts one account_devices row with the given ban_status/
// health_score/quarantined_until-offset (nil = NULL). tenantID must already
// exist (seedTenantUser).
func seedRiskAccount(t *testing.T, ctx context.Context, s *Server, tenantID int64, jid, banStatus string, health int, quarantinedFuture bool) {
	t.Helper()
	q := `INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status, health_score, quarantined_until)
	      VALUES ($1,$2,$2,$3::ban_status_t,$4,` + map[bool]string{true: "now() + interval '1 hour'", false: "NULL"}[quarantinedFuture] + `)`
	if _, err := s.systemPool().Exec(ctx, q, tenantID, jid, banStatus, health); err != nil {
		t.Fatalf("seed risk account %s: %v", jid, err)
	}
}

func callRiskOverview(t *testing.T, s *Server) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/risk/overview", nil)
	s.handleAdminRiskOverview(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.Data
}

func callRiskAccounts(t *testing.T, s *Server, query string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/risk/accounts"+query, nil)
	s.handleAdminRiskAccounts(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.Data
}

// TestRiskOverview_AccountsAndHealthBuckets seeds a mix of ban_status/
// quarantine/health_score account rows and asserts ban_rate, account
// counts, and health_buckets (low<40, mid[40,70), high>=70).
func TestRiskOverview_AccountsAndHealthBuckets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	pool := testPool(t)
	s := &Server{sysPool: pool}
	tid, _ := seedTenantUser(t, ctx, s, "risk-overview@acme.test")

	// 6 accounts total.
	seedRiskAccount(t, ctx, s, tid, "r1@s.whatsapp.net", "active", 90, false)  // active, high
	seedRiskAccount(t, ctx, s, tid, "r2@s.whatsapp.net", "banned", 80, false)  // banned, high
	seedRiskAccount(t, ctx, s, tid, "r3@s.whatsapp.net", "flagged", 50, false) // flagged, mid
	seedRiskAccount(t, ctx, s, tid, "r4@s.whatsapp.net", "active", 30, false)  // active, low
	seedRiskAccount(t, ctx, s, tid, "r5@s.whatsapp.net", "active", 100, true)  // active, quarantined, high
	seedRiskAccount(t, ctx, s, tid, "r6@s.whatsapp.net", "active", 65, false)  // active, mid

	ov := callRiskOverview(t, s)

	if ov["accounts_total"].(float64) != 6 {
		t.Errorf("accounts_total = %v, want 6", ov["accounts_total"])
	}
	if ov["accounts_active"].(float64) != 4 { // r1,r4,r5,r6
		t.Errorf("accounts_active = %v, want 4", ov["accounts_active"])
	}
	if ov["accounts_quarantined"].(float64) != 1 { // r5 only
		t.Errorf("accounts_quarantined = %v, want 1", ov["accounts_quarantined"])
	}
	// ban_rate = banned+flagged(2) / total(6)
	if got := ov["ban_rate"].(float64); got < 0.333 || got > 0.334 {
		t.Errorf("ban_rate = %v, want ~0.3333", got)
	}
	hb := ov["health_buckets"].(map[string]any)
	if hb["low"].(float64) != 1 { // r4 (30)
		t.Errorf("health_buckets.low = %v, want 1", hb["low"])
	}
	if hb["mid"].(float64) != 2 { // r3(50), r6(65)
		t.Errorf("health_buckets.mid = %v, want 2", hb["mid"])
	}
	if hb["high"].(float64) != 3 { // r1(90), r2(80), r5(100)
		t.Errorf("health_buckets.high = %v, want 3", hb["high"])
	}
}

// TestRiskOverview_RecipientMetrics_And_ZeroDivisionGuard covers the 1h
// throughput metrics (copied SQL shape from internal/metrics/sampler.go) and
// the delivered_rate_1h / failed_rate_1h divide-by-zero guard when
// processed_1h == 0.
func TestRiskOverview_RecipientMetrics_And_ZeroDivisionGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	pool := testPool(t)
	s := &Server{sysPool: pool}
	tid, _ := seedTenantUser(t, ctx, s, "risk-recip@acme.test")
	seedRiskAccount(t, ctx, s, tid, "z1@s.whatsapp.net", "active", 100, false)

	// No campaigns/recipients at all yet: processed_1h == 0 must not divide by zero.
	ov := callRiskOverview(t, s)
	if ov["processed_1h"].(float64) != 0 {
		t.Fatalf("processed_1h = %v, want 0", ov["processed_1h"])
	}
	if ov["delivered_rate_1h"].(float64) != 0 {
		t.Errorf("delivered_rate_1h = %v, want 0 (div/0 guard)", ov["delivered_rate_1h"])
	}
	if ov["failed_rate_1h"].(float64) != 0 {
		t.Errorf("failed_rate_1h = %v, want 0 (div/0 guard)", ov["failed_rate_1h"])
	}
	if ov["avg_delivery_ms"] != nil {
		t.Errorf("avg_delivery_ms = %v, want nil (no deliveries)", ov["avg_delivery_ms"])
	}

	var templateID, campaignID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES ($1,'text','hi') RETURNING id`, tid,
	).Scan(&templateID); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state, created_at) VALUES ($1,$2,'running', now() - interval '20 minutes') RETURNING id`,
		tid, templateID,
	).Scan(&campaignID); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v (%s)", err, sql)
		}
	}
	// p1: pending → queue_backlog.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state) VALUES ($1,$2,'p1','US','pending')`, campaignID, tid)
	// p2: sent recently → processed_1h.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, updated_at) VALUES ($1,$2,'p2','US','sent', now() - interval '10 minutes')`, campaignID, tid)
	// p3: failed recently → processed_1h + failed_1h.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, updated_at) VALUES ($1,$2,'p3','US','failed', now() - interval '5 minutes')`, campaignID, tid)
	// p4: sent + delivered recently → processed_1h + delivered_1h.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, updated_at, delivered_at) VALUES ($1,$2,'p4','US','sent', now() - interval '10 minutes', now() - interval '10 minutes')`, campaignID, tid)
	// p5: sent+delivered but 2h old → must not count.
	mustExec(`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state, updated_at, delivered_at) VALUES ($1,$2,'p5','US','sent', now() - interval '2 hours', now() - interval '2 hours')`, campaignID, tid)

	ov = callRiskOverview(t, s)
	if ov["queue_backlog"].(float64) != 1 {
		t.Errorf("queue_backlog = %v, want 1", ov["queue_backlog"])
	}
	if ov["processed_1h"].(float64) != 3 { // p2,p3,p4
		t.Errorf("processed_1h = %v, want 3", ov["processed_1h"])
	}
	// delivered_rate_1h = 1/3, failed_rate_1h = 1/3
	if got := ov["delivered_rate_1h"].(float64); got < 0.333 || got > 0.334 {
		t.Errorf("delivered_rate_1h = %v, want ~0.3333", got)
	}
	if got := ov["failed_rate_1h"].(float64); got < 0.333 || got > 0.334 {
		t.Errorf("failed_rate_1h = %v, want ~0.3333", got)
	}
	if ov["avg_delivery_ms"] == nil {
		t.Errorf("avg_delivery_ms = nil, want non-nil (p4 delivered within the last hour)")
	}
}

// TestRiskAccounts_ReasonClassificationAndFiltering seeds accounts covering
// all three anomaly reasons plus one healthy account, and asserts: only
// anomalous accounts are returned, reason classification follows the
// documented priority (ban > quarantine > low_health), the q filter works,
// the reason filter works, and stats is a reason→count breakdown.
func TestRiskAccounts_ReasonClassificationAndFiltering(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	pool := testPool(t)
	s := &Server{sysPool: pool}
	tid, _ := seedTenantUser(t, ctx, s, "risk-accounts@acme.test")

	seedRiskAccount(t, ctx, s, tid, "healthy@s.whatsapp.net", "active", 100, false)     // not anomalous
	seedRiskAccount(t, ctx, s, tid, "banned@s.whatsapp.net", "banned", 100, false)      // reason=ban
	seedRiskAccount(t, ctx, s, tid, "flagged@s.whatsapp.net", "flagged", 100, false)    // reason=ban
	seedRiskAccount(t, ctx, s, tid, "quarantine@s.whatsapp.net", "active", 100, true)   // reason=quarantine
	seedRiskAccount(t, ctx, s, tid, "lowhealth@s.whatsapp.net", "active", 20, false)    // reason=low_health
	// priority conflict: banned AND quarantined AND low_health simultaneously
	// must classify as "ban" (highest priority), not quarantine/low_health.
	seedRiskAccount(t, ctx, s, tid, "multi@s.whatsapp.net", "banned", 10, true)

	all := callRiskAccounts(t, s, "")
	if all["total"].(float64) != 5 {
		t.Fatalf("total = %v, want 5 (healthy excluded)", all["total"])
	}
	rows := all["rows"].([]any)
	if len(rows) != 5 {
		t.Fatalf("rows len = %d, want 5", len(rows))
	}
	reasonByJID := map[string]string{}
	for _, rv := range rows {
		r := rv.(map[string]any)
		reasonByJID[r["jid"].(string)] = r["reason"].(string)
		if r["jid"] == "healthy@s.whatsapp.net" {
			t.Fatalf("healthy account must not appear in risk accounts")
		}
	}
	if reasonByJID["banned@s.whatsapp.net"] != "ban" {
		t.Errorf("banned reason = %v, want ban", reasonByJID["banned@s.whatsapp.net"])
	}
	if reasonByJID["flagged@s.whatsapp.net"] != "ban" {
		t.Errorf("flagged reason = %v, want ban", reasonByJID["flagged@s.whatsapp.net"])
	}
	if reasonByJID["quarantine@s.whatsapp.net"] != "quarantine" {
		t.Errorf("quarantine reason = %v, want quarantine", reasonByJID["quarantine@s.whatsapp.net"])
	}
	if reasonByJID["lowhealth@s.whatsapp.net"] != "low_health" {
		t.Errorf("lowhealth reason = %v, want low_health", reasonByJID["lowhealth@s.whatsapp.net"])
	}
	if reasonByJID["multi@s.whatsapp.net"] != "ban" {
		t.Errorf("multi-condition reason = %v, want ban (highest priority)", reasonByJID["multi@s.whatsapp.net"])
	}

	stats := all["stats"].(map[string]any)
	if stats["ban"].(float64) != 3 { // banned, flagged, multi
		t.Errorf("stats.ban = %v, want 3", stats["ban"])
	}
	if stats["quarantine"].(float64) != 1 {
		t.Errorf("stats.quarantine = %v, want 1", stats["quarantine"])
	}
	if stats["low_health"].(float64) != 1 {
		t.Errorf("stats.low_health = %v, want 1", stats["low_health"])
	}

	// reason filter
	banOnly := callRiskAccounts(t, s, "?reason=ban")
	if banOnly["total"].(float64) != 3 {
		t.Errorf("reason=ban total = %v, want 3", banOnly["total"])
	}
	// stats must stay the full unfiltered breakdown even under a reason filter
	// (mirrors ListInstances: stats is a global, not filtered, roll-up).
	filteredStats := banOnly["stats"].(map[string]any)
	if filteredStats["quarantine"].(float64) != 1 || filteredStats["low_health"].(float64) != 1 {
		t.Errorf("stats must be unfiltered under reason filter, got %v", filteredStats)
	}

	// q filter on jid substring
	qFiltered := callRiskAccounts(t, s, "?q=lowhealth")
	if qFiltered["total"].(float64) != 1 {
		t.Errorf("q filter total = %v, want 1", qFiltered["total"])
	}

	// pagination
	pg := callRiskAccounts(t, s, "?limit=2")
	if len(pg["rows"].([]any)) != 2 || pg["total"].(float64) != 5 {
		t.Errorf("limit paging: rows=%d total=%v", len(pg["rows"].([]any)), pg["total"])
	}
	pg2 := callRiskAccounts(t, s, "?limit=2&offset=4")
	if len(pg2["rows"].([]any)) != 1 || pg2["total"].(float64) != 5 {
		t.Errorf("offset paging: rows=%d total=%v", len(pg2["rows"].([]any)), pg2["total"])
	}

	// spot-check row shape: tenant_name join + health_score + quarantined_until presence
	for _, rv := range rows {
		r := rv.(map[string]any)
		if r["jid"] == "quarantine@s.whatsapp.net" {
			if r["tenant_name"] != "Acme" {
				t.Errorf("tenant_name = %v, want Acme", r["tenant_name"])
			}
			if r["quarantined_until"] == nil {
				t.Errorf("quarantined_until = nil, want set")
			}
			if r["health_score"].(float64) != 100 {
				t.Errorf("health_score = %v, want 100", r["health_score"])
			}
		}
	}
}
