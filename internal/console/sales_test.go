// internal/console/sales_test.go
package console

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/pricing"
	"github.com/acme/wadist/internal/store"
)

// itoa converts an int64 to its decimal string representation.
func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// urlValues builds a url.Values from alternating key/value strings.
func urlValues(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

// pricingRepoFor returns a pricing.Repo backed by the manager's SystemPool.
func pricingRepoFor(t *testing.T, mgr *store.Manager) *pricing.Repo {
	t.Helper()
	return pricing.NewRepo(mgr.SystemPool())
}

// billingRepoFor returns a billing.Repo backed by the manager's SystemPool.
func billingRepoFor(t *testing.T, mgr *store.Manager) *billing.Repo {
	t.Helper()
	return billing.NewRepo(mgr.SystemPool())
}

// loginWithExisting creates a session for an already-existing user (by ID) and
// returns a cookie-jar HTTP client pre-loaded with the signed session cookie.
// Use this when the user was created outside loginAs (e.g. to capture the ID).
func loginWithExisting(t *testing.T, ts *httptest.Server, sessions *SessionStore, cfg Config, userID int64, role Role, tenantID *int64) *http.Client {
	t.Helper()
	ctx := context.Background()
	sid, err := sessions.Create(ctx, SessionData{UserID: userID, Role: role, TenantID: tenantID})
	if err != nil {
		t.Fatalf("loginWithExisting: create session: %v", err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// Plant the signed session cookie into the jar.
	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	jar.SetCookies(req.URL, []*http.Cookie{{
		Name:  cfg.CookieName,
		Value: SignCookie(cfg.SessionKey, sid),
		Path:  "/",
	}})
	return client
}

func TestSalesDashboardOwnershipScoped(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	srv, _ := NewServer(cfg, users, sessions, mgr)
	srv.WithBilling(billingRepoFor(t, mgr)).WithTenants(NewTenantRepo(mgr.SystemPool()))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	hash, _ := HashPassword("pw")
	// Create sales users first so we have their IDs for tenant seeding.
	salesA, _ := users.Create(ctx, "sa@x.test", hash, RoleSales, nil)
	salesB, _ := users.Create(ctx, "sb@x.test", hash, RoleSales, nil)

	var tA, tB int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name, sales_owner_id) VALUES ('AcmeA',$1) RETURNING id`, &tA, salesA)
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name, sales_owner_id) VALUES ('AcmeB',$1) RETURNING id`, &tB, salesB)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 111)`, tA)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 222)`, tB)

	// Log in as salesA via direct session injection (user already exists).
	cli := loginWithExisting(t, ts, sessions, cfg, salesA, RoleSales, nil)
	resp, err := cli.Get(ts.URL + "/sales")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "AcmeA") || !strings.Contains(body, "111") {
		t.Fatalf("sales dashboard missing own customer: %d %s", resp.StatusCode, body)
	}
	// Ownership isolation: salesA must NOT see salesB's customer.
	if strings.Contains(body, "AcmeB") || strings.Contains(body, "222") {
		t.Fatalf("sales A leaked sales B's customer: %s", body)
	}

	// A customer hitting /sales → 403.
	custCli := loginAs(t, ts, users, sessions, cfg, "c@x.test", RoleCustomer, &tA)
	r2, _ := custCli.Get(ts.URL + "/sales")
	if r2 != nil {
		defer r2.Body.Close()
	}
	if r2.StatusCode != http.StatusForbidden {
		t.Fatalf("customer /sales: want 403, got %d", r2.StatusCode)
	}
	_ = salesB
}

func TestSalesTenantOwnershipGuard(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	srv, _ := NewServer(cfg, users, sessions, mgr)
	srv.WithBilling(billingRepoFor(t, mgr)).WithTenants(NewTenantRepo(mgr.SystemPool())).WithPricing(pricingRepoFor(t, mgr))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	hash, _ := HashPassword("pw")
	salesA, _ := users.Create(ctx, "sa@x.test", hash, RoleSales, nil)
	salesB, _ := users.Create(ctx, "sb@x.test", hash, RoleSales, nil)
	var tA, tB int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name, sales_owner_id) VALUES ('A',$1) RETURNING id`, &tA, salesA)
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name, sales_owner_id) VALUES ('B',$1) RETURNING id`, &tB, salesB)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 50)`, tA)

	cli := loginWithExisting(t, ts, sessions, cfg, salesA, RoleSales, nil)
	tokA := csrfFor(t, cli, ts, "/sales/tenant/"+itoa(tA))

	// salesA views own tenant → 200
	if r, _ := cli.Get(ts.URL + "/sales/tenant/" + itoa(tA)); r == nil || r.StatusCode != 200 {
		t.Fatalf("own tenant detail not 200")
	}
	// salesA views NOT-owned tenant B → 403
	r2, _ := cli.Get(ts.URL + "/sales/tenant/" + itoa(tB))
	if r2 != nil {
		defer r2.Body.Close()
	}
	if r2.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owned detail: want 403, got %d", r2.StatusCode)
	}
	// salesA sets price on own tenant → 303, price stored
	pr, _ := cli.PostForm(ts.URL+"/sales/tenant/"+itoa(tA)+"/pricing", urlValues("csrf", tokA, "country", "US", "unit_price", "7"))
	if pr != nil {
		defer pr.Body.Close()
	}
	if pr.StatusCode != http.StatusSeeOther {
		t.Fatalf("own set-price: want 303, got %d", pr.StatusCode)
	}
	if got, err := pricingRepoFor(t, mgr).GetPrice(ctx, tA, "US"); err != nil || got != 7 {
		t.Fatalf("price not stored: %d %v", got, err)
	}
	// salesA sets price on NOT-owned tenant B → 403, nothing stored
	tokB := tokA // same csrf cookie/jar
	pr2, _ := cli.PostForm(ts.URL+"/sales/tenant/"+itoa(tB)+"/pricing", urlValues("csrf", tokB, "country", "US", "unit_price", "9"))
	if pr2 != nil {
		defer pr2.Body.Close()
	}
	if pr2.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owned set-price: want 403, got %d", pr2.StatusCode)
	}
	if _, err := pricingRepoFor(t, mgr).GetPrice(ctx, tB, "US"); !errors.Is(err, pricing.ErrNoPrice) {
		t.Fatalf("non-owned POST must store no price; GetPrice(tB) err = %v (want pricing.ErrNoPrice)", err)
	}
	_ = salesB
}
