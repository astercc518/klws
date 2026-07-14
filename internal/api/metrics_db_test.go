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

	seedRiskAccount(t, ctx, s, tid, "healthy@s.whatsapp.net", "active", 100, false)   // not anomalous
	seedRiskAccount(t, ctx, s, tid, "banned@s.whatsapp.net", "banned", 100, false)    // reason=ban
	seedRiskAccount(t, ctx, s, tid, "flagged@s.whatsapp.net", "flagged", 100, false)  // reason=ban
	seedRiskAccount(t, ctx, s, tid, "quarantine@s.whatsapp.net", "active", 100, true) // reason=quarantine
	seedRiskAccount(t, ctx, s, tid, "lowhealth@s.whatsapp.net", "active", 20, false)  // reason=low_health
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

// ---------------------------------------------------------------------------
// P3 task-4: GET /admin/reports/trend + GET /admin/reports/tenant-consumption
// ---------------------------------------------------------------------------

// seedMetricSnapshot inserts one metric_snapshots row (tenant_id always NULL,
// platform-level — same as internal/metrics.Store.Insert) with capturedAt as
// an RFC3339 string and accountsActive as the only varying gauge (the other
// NOT NULL int columns are filled with 0/constant so the row satisfies the
// schema without being relevant to the trend assertions).
func seedMetricSnapshot(t *testing.T, ctx context.Context, s *Server, capturedAt string, accountsActive int) {
	t.Helper()
	_, err := s.systemPool().Exec(ctx, `
INSERT INTO metric_snapshots
    (captured_at, tenant_id, accounts_total, accounts_active, accounts_banned,
     accounts_quarantined, queue_backlog, processed_1h, delivered_1h, failed_1h, avg_delivery_ms)
VALUES ($1::timestamptz, NULL, 100, $2, 0, 0, 0, 0, 0, 0, NULL)`, capturedAt, accountsActive)
	if err != nil {
		t.Fatalf("seed metric snapshot: %v", err)
	}
}

// TestHandleAdminReportsTrend_DayBucketAggregation mirrors
// internal/metrics.TestSnapshotTrendDayBucketAggregation but through the HTTP
// handler: two Shanghai-local days, two samples each, asserting the averaged
// day-bucket series comes back in the JSON envelope.
func TestReportsTrend_DayBucketAggregation(t *testing.T) {
	s, ctx := newFinanceServer(t)
	// day1 (Asia/Shanghai 07-10): 09:00 and 17:00 CST -> avg(10,20)=15.
	seedMetricSnapshot(t, ctx, s, "2026-07-10T01:00:00Z", 10)
	seedMetricSnapshot(t, ctx, s, "2026-07-10T09:00:00Z", 20)
	// day2 (07-11): avg(40,60)=50.
	seedMetricSnapshot(t, ctx, s, "2026-07-11T01:00:00Z", 40)
	seedMetricSnapshot(t, ctx, s, "2026-07-11T09:00:00Z", 60)

	w := doGET(t, s, s.handleAdminReportsTrend,
		"/admin/reports/trend?metric=accounts_active&bucket=day&from=2026-07-09T00:00:00Z&to=2026-07-12T00:00:00Z")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var env struct {
		Data []struct {
			Bucket string  `json:"bucket"`
			Value  float64 `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 2 {
		t.Fatalf("expected 2 day buckets, got %d: %+v", len(env.Data), env.Data)
	}
	if env.Data[0].Value != 15 {
		t.Errorf("day1 avg = %v, want 15", env.Data[0].Value)
	}
	if env.Data[1].Value != 50 {
		t.Errorf("day2 avg = %v, want 50", env.Data[1].Value)
	}
}

// TestHandleAdminReportsTrend_BadMetric asserts an unwhitelisted metric name
// (which metrics.Store.Trend rejects internally) surfaces as 400, not 500.
func TestReportsTrend_BadMetric(t *testing.T) {
	s, ctx := newFinanceServer(t)
	seedMetricSnapshot(t, ctx, s, "2026-07-10T01:00:00Z", 10)

	w := doGET(t, s, s.handleAdminReportsTrend,
		"/admin/reports/trend?metric=accounts_total%3B+DROP+TABLE+metric_snapshots%3B--&bucket=day")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s, want 400", w.Code, w.Body.String())
	}

	// table must still exist — the injection attempt must not have executed.
	var count int
	if err := s.systemPool().QueryRow(ctx, `SELECT count(*) FROM metric_snapshots`).Scan(&count); err != nil {
		t.Fatalf("metric_snapshots should still exist: %v", err)
	}
}

// TestHandleAdminReportsTrend_BadBucket asserts an unwhitelisted bucket value
// surfaces as 400.
func TestReportsTrend_BadBucket(t *testing.T) {
	s, _ := newFinanceServer(t)
	w := doGET(t, s, s.handleAdminReportsTrend, "/admin/reports/trend?metric=accounts_active&bucket=month")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s, want 400", w.Code, w.Body.String())
	}
}

// TestHandleAdminReportsTenantConsumption_RankingAndPagination seeds three
// tenants with distinct settle spend (and one hold row that must NOT count,
// same exclusion rule as handleAdminFinanceStats' netExpr/settle filter) and
// asserts descending consumption ranking, limit/offset pagination, and total.
func TestReportsTenantConsumption_RankingAndPagination(t *testing.T) {
	s, ctx := newFinanceServer(t)
	t1, _ := seedTenantUser(t, ctx, s, "rc1@acme.test")
	t2, _ := seedTenantUser(t, ctx, s, "rc2@acme.test")
	t3, _ := seedTenantUser(t, ctx, s, "rc3@acme.test")

	seedLedger(t, ctx, s, t1, "settle", -500, 0, 500, 0, "2026-07-05T10:00:00+08:00", "rc-t1")
	seedLedger(t, ctx, s, t2, "settle", -300, 0, 700, 0, "2026-07-05T11:00:00+08:00", "rc-t2")
	seedLedger(t, ctx, s, t3, "settle", -800, 0, 200, 0, "2026-07-05T12:00:00+08:00", "rc-t3")
	// hold nets to zero and must not distort ranking (mirrors netExpr exclusion test in finance_db_test.go).
	seedLedger(t, ctx, s, t1, "hold", -50, 50, 450, 50, "2026-07-05T12:30:00+08:00", "rc-t1-hold")

	w := doGET(t, s, s.handleAdminReportsTenantConsumption, "/admin/reports/tenant-consumption?from=2026-07-05&to=2026-07-05")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Rows []struct {
				TenantID    int64  `json:"tenant_id"`
				TenantName  string `json:"tenant_name"`
				Consumption int64  `json:"consumption"`
			} `json:"rows"`
			Total int `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Total != 3 {
		t.Fatalf("total = %d, want 3", env.Data.Total)
	}
	if len(env.Data.Rows) != 3 {
		t.Fatalf("rows len = %d, want 3", len(env.Data.Rows))
	}
	// descending: t3(800) > t1(500) > t2(300)
	if env.Data.Rows[0].TenantID != t3 || env.Data.Rows[0].Consumption != 800 {
		t.Errorf("rows[0] = %+v, want tenant %d consumption 800", env.Data.Rows[0], t3)
	}
	if env.Data.Rows[1].TenantID != t1 || env.Data.Rows[1].Consumption != 500 {
		t.Errorf("rows[1] = %+v, want tenant %d consumption 500", env.Data.Rows[1], t1)
	}
	if env.Data.Rows[2].TenantID != t2 || env.Data.Rows[2].Consumption != 300 {
		t.Errorf("rows[2] = %+v, want tenant %d consumption 300", env.Data.Rows[2], t2)
	}

	// pagination: limit=2 -> first two rows, total still 3.
	wp := doGET(t, s, s.handleAdminReportsTenantConsumption, "/admin/reports/tenant-consumption?from=2026-07-05&to=2026-07-05&limit=2")
	var envp struct {
		Data struct {
			Rows  []map[string]any `json:"rows"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wp.Body.Bytes(), &envp); err != nil {
		t.Fatalf("decode paged: %v", err)
	}
	if len(envp.Data.Rows) != 2 || envp.Data.Total != 3 {
		t.Errorf("limit paging: rows=%d total=%d", len(envp.Data.Rows), envp.Data.Total)
	}

	wp2 := doGET(t, s, s.handleAdminReportsTenantConsumption, "/admin/reports/tenant-consumption?from=2026-07-05&to=2026-07-05&limit=2&offset=2")
	var envp2 struct {
		Data struct {
			Rows  []map[string]any `json:"rows"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wp2.Body.Bytes(), &envp2); err != nil {
		t.Fatalf("decode paged2: %v", err)
	}
	if len(envp2.Data.Rows) != 1 || envp2.Data.Total != 3 {
		t.Errorf("offset paging: rows=%d total=%d", len(envp2.Data.Rows), envp2.Data.Total)
	}
}
