// internal/console/admin_write_test.go
package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/pricing"
)

func TestAdminRechargeAndSetPrice(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	bill := billing.NewRepo(mgr.SystemPool())
	price := pricing.NewRepo(mgr.SystemPool())
	tenants := NewTenantRepo(mgr.SystemPool())
	srv, _ := NewServer(cfg, users, sessions, mgr)
	srv.WithBilling(bill).WithTenants(tenants).WithPricing(price)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var tid int64
	if err := mgr.SystemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('Acme') RETURNING id`).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	admin := loginAs(t, ts, users, sessions, cfg, "admin@x.test", RoleAdmin, nil)

	// recharge 500
	resp, err := admin.PostForm(ts.URL+"/admin/tenant/"+strconv.FormatInt(tid, 10)+"/recharge",
		url.Values{"amount": {"500"}, "ref": {"wire-001"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recharge: want 303, got %d", resp.StatusCode)
	}
	bal, _, _ := bill.Balance(ctx, tid)
	if bal != 500 {
		t.Fatalf("after recharge: want balance 500, got %d", bal)
	}

	// set price US=7
	resp2, err := admin.PostForm(ts.URL+"/admin/tenant/"+strconv.FormatInt(tid, 10)+"/pricing",
		url.Values{"country": {"US"}, "unit_price": {"7"}})
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("set price: want 303, got %d", resp2.StatusCode)
	}
	if got, err := price.GetPrice(ctx, tid, "US"); err != nil || got != 7 {
		t.Fatalf("price after set: want 7, got %d err=%v", got, err)
	}
}

func TestAdminWriteRoutesForbidNonAdmin(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	bill := billing.NewRepo(mgr.SystemPool())
	price := pricing.NewRepo(mgr.SystemPool())
	tenants := NewTenantRepo(mgr.SystemPool())
	srv, _ := NewServer(cfg, users, sessions, mgr)
	srv.WithBilling(bill).WithTenants(tenants).WithPricing(price)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var tid int64
	if err := mgr.SystemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('Acme') RETURNING id`).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	idStr := strconv.FormatInt(tid, 10)
	// a customer must be forbidden on both write routes
	cust := loginAs(t, ts, users, sessions, cfg, "cust@x.test", RoleCustomer, &tid)

	resp, err := cust.PostForm(ts.URL+"/admin/tenant/"+idStr+"/recharge", url.Values{"amount": {"100"}, "ref": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("customer recharge: want 403, got %d", resp.StatusCode)
	}
	resp2, err := cust.PostForm(ts.URL+"/admin/tenant/"+idStr+"/pricing", url.Values{"country": {"US"}, "unit_price": {"5"}})
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("customer set-price: want 403, got %d", resp2.StatusCode)
	}
	// and the customer must not have changed any state
	if bal, _, _ := bill.Balance(ctx, tid); bal != 0 {
		t.Fatalf("customer must not change balance, got %d", bal)
	}
}
