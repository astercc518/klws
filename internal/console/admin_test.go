// internal/console/admin_test.go
package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acme/wadist/internal/billing"
)

// loginAs seeds a user with the given role and returns a cookie-jar client logged in.
func loginAs(t *testing.T, ts *httptest.Server, repo *UserRepo, sessions *SessionStore, cfg Config, email string, role Role, tenantID *int64) *http.Client {
	t.Helper()
	hash, err := HashPassword("pw123456")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := repo.Create(context.Background(), email, hash, role, tenantID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	jar, _ := newJar()
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.PostForm(ts.URL+"/login", url.Values{"email": {email}, "password": {"pw123456"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resp.Body.Close()
	return client
}

func TestAdminTenantsPageAndRBAC(t *testing.T) {
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	bill := billing.NewRepo(mgr.SystemPool())
	tenants := NewTenantRepo(mgr.SystemPool())
	srv, _ := NewServer(cfg, users, sessions, mgr)
	srv.WithBilling(bill).WithTenants(tenants)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// seed a tenant
	var tid int64
	if err := mgr.SystemPool().QueryRow(context.Background(), `INSERT INTO tenants (name) VALUES ('Acme') RETURNING id`).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	// admin can see the tenant list
	admin := loginAs(t, ts, users, sessions, cfg, "admin@x.test", RoleAdmin, nil)
	resp, err := admin.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "Acme") {
		t.Fatalf("admin /admin: status=%d body=%s", resp.StatusCode, body)
	}

	// customer is forbidden
	cust := loginAs(t, ts, users, sessions, cfg, "cust@x.test", RoleCustomer, &tid)
	resp2, err := cust.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("customer /admin: want 403, got %d", resp2.StatusCode)
	}
}

func TestAdminTenantDetailAndBadID(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	bill := billing.NewRepo(mgr.SystemPool())
	tenants := NewTenantRepo(mgr.SystemPool())
	srv, _ := NewServer(cfg, users, sessions, mgr)
	srv.WithBilling(bill).WithTenants(tenants)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var tid int64
	if err := mgr.SystemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('Acme') RETURNING id`).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	// give the tenant a wallet + a ledger row via a topup so the detail page has content
	if err := bill.Topup(ctx, tid, 250, "seed-ref"); err != nil {
		t.Fatalf("topup: %v", err)
	}

	admin := loginAs(t, ts, users, sessions, cfg, "admin@x.test", RoleAdmin, nil)

	// detail page shows balance + ledger
	resp, err := admin.Get(ts.URL + "/admin/tenant/" + strconv.FormatInt(tid, 10))
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "250") {
		t.Fatalf("detail page: status=%d body=%s", resp.StatusCode, body)
	}

	// non-integer id -> 400
	resp2, err := admin.Get(ts.URL + "/admin/tenant/not-a-number")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad id: want 400, got %d", resp2.StatusCode)
	}
}
