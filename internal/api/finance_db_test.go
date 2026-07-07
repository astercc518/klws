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

func doGET(t *testing.T, s *Server, h gin.HandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	h(c)
	return w
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
