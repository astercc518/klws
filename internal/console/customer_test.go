// internal/console/customer_test.go
package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/acme/wadist/internal/store"
)

// mustScan executes a query via SystemPool, scanning the single returned column
// into dest. args are the query parameters.
func mustScan(t *testing.T, mgr *store.Manager, ctx context.Context, q string, dest interface{}, args ...interface{}) {
	t.Helper()
	if err := mgr.SystemPool().QueryRow(ctx, q, args...).Scan(dest); err != nil {
		t.Fatalf("mustScan(%q): %v", q, err)
	}
}

// mustExec executes a statement via SystemPool, not scanning any result.
func mustExec(t *testing.T, mgr *store.Manager, ctx context.Context, q string, args ...interface{}) {
	t.Helper()
	if _, err := mgr.SystemPool().Exec(ctx, q, args...); err != nil {
		t.Fatalf("mustExec(%q): %v", q, err)
	}
}

func TestCustomerDashboardIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	srv, _ := NewServer(cfg, users, sessions, mgr)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// two tenants, each with a wallet + a campaign (seed via SystemPool / BYPASSRLS)
	var tA, tB, tplA, tplB int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name) VALUES ('A') RETURNING id`, &tA)
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name) VALUES ('B') RETURNING id`, &tB)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 111)`, tA)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 222)`, tB)
	mustScan(t, mgr, ctx, `INSERT INTO campaign_templates (tenant_id, kind, body) VALUES ($1,'text','a') RETURNING id`, &tplA, tA)
	mustScan(t, mgr, ctx, `INSERT INTO campaign_templates (tenant_id, kind, body) VALUES ($1,'text','b') RETURNING id`, &tplB, tB)
	mustExec(t, mgr, ctx, `INSERT INTO campaigns (tenant_id, template_id, state, total) VALUES ($1,$2,'running',50)`, tA, tplA)
	mustExec(t, mgr, ctx, `INSERT INTO campaigns (tenant_id, template_id, state, total) VALUES ($1,$2,'running',99)`, tB, tplB)

	// customer A logs in and views the dashboard
	custA := loginAs(t, ts, users, sessions, cfg, "a@x.test", RoleCustomer, &tA)
	resp, err := custA.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard status %d", resp.StatusCode)
	}
	// sees its own balance (111) and campaign total (50); NOT B's 222/99
	if !strings.Contains(body, "111") || strings.Contains(body, "222") {
		t.Fatalf("RLS leak or missing balance: %s", body)
	}
}
