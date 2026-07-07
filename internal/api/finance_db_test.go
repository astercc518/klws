package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/audit"
)

func newFinanceServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	pool := testPool(t)
	return &Server{sysPool: pool, deps: Deps{Audit: audit.NewAuditWriter(pool)}}, context.Background()
}

// seedLedger inserts one wallet_ledger row and returns nothing; idem_key is
// derived from the caller-supplied unique suffix.
func seedLedger(t *testing.T, ctx context.Context, s *Server, tenantID int64, kind string, dbal, dfroz, balAfter, frozAfter int64, at string, idem string) {
	t.Helper()
	_, err := s.systemPool().Exec(ctx, `
INSERT INTO wallet_ledger (tenant_id, kind, delta_balance, delta_frozen, balance_after, frozen_after, idem_key, created_at)
VALUES ($1, $2::ledger_kind_t, $3, $4, $5, $6, $7, $8::timestamptz)`,
		tenantID, kind, dbal, dfroz, balAfter, frozAfter, idem, at)
	if err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
}

func TestHandleAdminLedgerPaginated(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "led@acme.test")
	seedLedger(t, ctx, s, tid, "topup", 1000, 0, 1000, 0, "2026-07-01T10:00:00+08:00", "k1")
	seedLedger(t, ctx, s, tid, "settle", -200, 0, 800, 0, "2026-07-02T10:00:00+08:00", "k2")
	seedLedger(t, ctx, s, tid, "topup", 500, 0, 1300, 0, "2026-07-03T10:00:00+08:00", "k3")

	// filter kind=topup → 2 rows
	w := doGET(t, s, s.handleAdminLedger, "/admin/finance/ledger?kind=topup")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"total":2`) {
		t.Errorf("want total 2, body=%s", body)
	}
}

func TestHandleAdminLedgerExport(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "exp@acme.test")
	seedLedger(t, ctx, s, tid, "topup", 1000, 0, 1000, 0, "2026-07-01T10:00:00+08:00", "e1")
	seedLedger(t, ctx, s, tid, "settle", -200, 0, 800, 0, "2026-07-02T10:00:00+08:00", "e2")

	w := doGET(t, s, s.handleAdminLedgerExport, "/admin/finance/ledger/export")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("content-type %q", ct)
	}
	// header line + 2 data lines + trailing newline = 3 \n
	body := w.Body.String()
	if lines := strings.Count(body, "\n"); lines != 3 {
		t.Errorf("want 3 newlines, got %d body=%q", lines, body)
	}
	if !strings.HasPrefix(body, "id,created_at,tenant_id,tenant_name,kind,") {
		t.Errorf("bad header: %q", body)
	}
	// audit row written
	var n int
	s.systemPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='finance.ledger_export'`).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 export audit, got %d", n)
	}
}

func TestHandleAdminFinanceStats(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "stat@acme.test")
	// Two topups (1500 total), one settle (300 spend).
	seedLedger(t, ctx, s, tid, "topup", 1000, 0, 1000, 0, "2026-07-01T10:00:00+08:00", "s1")
	seedLedger(t, ctx, s, tid, "settle", -300, 0, 700, 0, "2026-07-01T11:00:00+08:00", "s2")
	seedLedger(t, ctx, s, tid, "topup", 500, 0, 1200, 0, "2026-07-02T09:00:00+08:00", "s3")
	// hold nets to zero — must NOT affect totals.
	seedLedger(t, ctx, s, tid, "hold", -100, 100, 1200, 100, "2026-07-02T09:30:00+08:00", "s4")
	// reject has a NON-zero net (-999): if the FILTER logic ever broadened
	// to sum across all kinds instead of enumerating topup/settle/refund/adjust,
	// this would corrupt a total and fail the assertions below. Unlike the
	// hold row (which nets to zero and can't distinguish "excluded" from
	// "included but harmless"), this makes the exclusion load-bearing.
	seedLedger(t, ctx, s, tid, "reject", -999, 0, 201, 100, "2026-07-02T09:45:00+08:00", "s5")

	w := doGET(t, s, s.handleAdminFinanceStats, "/admin/finance/stats?from=2026-07-01&to=2026-07-02")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"topup":1500`) {
		t.Errorf("want topup 1500: %s", body)
	}
	if !contains(body, `"settle":300`) { // stored as -300, reported positive
		t.Errorf("want settle 300: %s", body)
	}
	if !contains(body, `"refund":0`) {
		t.Errorf("want refund 0 (hold/reject excluded): %s", body)
	}
	if !contains(body, `"adjust":0`) {
		t.Errorf("want adjust 0 (hold/reject excluded): %s", body)
	}
	if !contains(body, `"net":1200`) { // 1500 - 300 + 0 + 0
		t.Errorf("want net 1200 (hold/reject excluded): %s", body)
	}
}

func TestFinanceStatsTimezoneBucketing(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "tz@acme.test")
	// 2026-07-01T23:30:00Z == 2026-07-02T07:30 CN → must bucket to 07-02.
	seedLedger(t, ctx, s, tid, "topup", 100, 0, 100, 0, "2026-07-01T23:30:00Z", "tz1")

	w := doGET(t, s, s.handleAdminFinanceStats, "/admin/finance/stats?from=2026-07-02&to=2026-07-02")
	body := w.Body.String()
	if !contains(body, `"day":"2026-07-02"`) {
		t.Errorf("row should bucket to CN 07-02: %s", body)
	}
}

func TestHandleAdminFinanceBill(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "bill@acme.test")
	// Before window: a topup establishing opening balance 1000.
	seedLedger(t, ctx, s, tid, "topup", 1000, 0, 1000, 0, "2026-06-15T10:00:00+08:00", "b0")
	// In window: +500 topup, -200 settle → closing 1300.
	seedLedger(t, ctx, s, tid, "topup", 500, 0, 1500, 0, "2026-07-02T10:00:00+08:00", "b1")
	seedLedger(t, ctx, s, tid, "settle", -200, 0, 1300, 0, "2026-07-03T10:00:00+08:00", "b2")

	w := doGET(t, s, s.handleAdminFinanceBill,
		"/admin/finance/bill?tenant_id="+itoa(tid)+"&from=2026-07-01&to=2026-07-31")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"opening":1000`) {
		t.Errorf("want opening 1000: %s", body)
	}
	if !contains(body, `"closing":1300`) {
		t.Errorf("want closing 1300: %s", body)
	}
	// reconciliation: opening + topup - settle == closing (1000 + 500 - 200)
	if !contains(body, `"topup":500`) || !contains(body, `"settle":200`) {
		t.Errorf("summary wrong: %s", body)
	}
}

func TestHandleAdminFinanceBillMissingTenant(t *testing.T) {
	s, _ := newFinanceServer(t)
	w := doGET(t, s, s.handleAdminFinanceBill, "/admin/finance/bill?from=2026-07-01&to=2026-07-31")
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing tenant_id want 400, got %d", w.Code)
	}
}

func TestHandleAdminFinanceBillExport(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "billexp@acme.test")
	seedLedger(t, ctx, s, tid, "topup", 500, 0, 500, 0, "2026-07-02T10:00:00+08:00", "be1")

	w := doGET(t, s, s.handleAdminFinanceBillExport,
		"/admin/finance/bill/export?tenant_id="+itoa(tid)+"&from=2026-07-01&to=2026-07-31")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("content-type %q", ct)
	}
	var n int
	s.systemPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='finance.bill_export'`).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 bill_export audit, got %d", n)
	}
}

// seedCampaign inserts a template + campaign and returns the campaign id.
func seedCampaign(t *testing.T, ctx context.Context, s *Server, tenantID int64, state string, total, sent, failed int) int64 {
	t.Helper()
	var tplID int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES ($1,'text','hi') RETURNING id`, tenantID).Scan(&tplID); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	var id int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state, total, sent, failed)
		 VALUES ($1,$2,$3::campaign_state_t,$4,$5,$6) RETURNING id`,
		tenantID, tplID, state, total, sent, failed).Scan(&id); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	return id
}

func TestHandleAdminListCampaignsFiltered(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "camp@acme.test")
	seedCampaign(t, ctx, s, tid, "running", 100, 50, 2)
	seedCampaign(t, ctx, s, tid, "paused", 80, 80, 0)
	seedCampaign(t, ctx, s, tid, "completed", 10, 10, 0)

	// filter state=running → 1 row, but stats counts are global
	w := doGET(t, s, s.handleAdminListCampaigns, "/admin/campaigns?state=running")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"total":1`) {
		t.Errorf("want filtered total 1: %s", body)
	}
	if !contains(body, `"running":1`) || !contains(body, `"paused":1`) {
		t.Errorf("want global stats running=1 paused=1: %s", body)
	}
}

// seedRecipient inserts one campaign_recipients row for the given campaign/tenant.
func seedRecipient(t *testing.T, ctx context.Context, s *Server, campaignID, tenantID int64, phone, state string) {
	t.Helper()
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code, state)
		 VALUES ($1,$2,$3,'US',$4::recipient_state_t)`,
		campaignID, tenantID, phone, state); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}
}

func TestHandleAdminListRecipients(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "recip@acme.test")
	cid := seedCampaign(t, ctx, s, tid, "running", 3, 2, 1)
	seedRecipient(t, ctx, s, cid, tid, "+15550001", "sent")
	seedRecipient(t, ctx, s, cid, tid, "+15550002", "sent")
	seedRecipient(t, ctx, s, cid, tid, "+15550003", "failed")

	w := doGET(t, s, s.handleAdminListRecipients, "/admin/recipients?state=sent")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"total":2`) {
		t.Errorf("want filtered total 2: %s", body)
	}
	if !contains(body, `"sent":2`) {
		t.Errorf("want global stats sent=2: %s", body)
	}
	if !contains(body, `"failed":1`) {
		t.Errorf("want global stats failed=1: %s", body)
	}
}

func TestHandleAdminCommissions(t *testing.T) {
	s, ctx := newFinanceServer(t)

	var salesID int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO console_users (email,password_hash,role,commission_rate) VALUES ('s1','x','sales',0.10) RETURNING id`,
	).Scan(&salesID); err != nil {
		t.Fatalf("seed sales user: %v", err)
	}

	var tenantA, tenantB int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO tenants (name,status,sales_owner_id,commission_rate) VALUES ('A','active',$1,0.20) RETURNING id`,
		salesID).Scan(&tenantA); err != nil {
		t.Fatalf("seed tenant A: %v", err)
	}
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO tenants (name,status,sales_owner_id,commission_rate) VALUES ('B','active',$1,NULL) RETURNING id`,
		salesID).Scan(&tenantB); err != nil {
		t.Fatalf("seed tenant B: %v", err)
	}

	// In-month settle: tenant A consumes 1000, tenant B consumes 500.
	seedLedger(t, ctx, s, tenantA, "settle", -1000, 0, 0, 0, "2026-07-15T10:00:00+08:00", "c1")
	seedLedger(t, ctx, s, tenantB, "settle", -500, 0, 0, 0, "2026-07-15T10:00:00+08:00", "c2")
	// Out-of-month settle for tenant A — must be excluded.
	seedLedger(t, ctx, s, tenantA, "settle", -9999, 0, 0, 0, "2026-06-15T10:00:00+08:00", "c3")

	w := doGET(t, s, s.handleAdminCommissions, "/admin/commissions?month=2026-07")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// tenant A: round(1000*0.20)=200, tenant B: round(500*0.10)=50 → 250 total.
	if !contains(body, `"consumption":1500`) {
		t.Errorf("want consumption 1500: %s", body)
	}
	if !contains(body, `"commission":250`) {
		t.Errorf("want commission 250: %s", body)
	}
	if !contains(body, `"totals":{"commission":250,"consumption":1500}`) {
		t.Errorf("want totals consumption=1500 commission=250: %s", body)
	}
	if !contains(body, `"month":"2026-07"`) {
		t.Errorf("want month echoed: %s", body)
	}
}

func doGET(t *testing.T, s *Server, h gin.HandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	h(c)
	return w
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
