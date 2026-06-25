# Module E-1 — 客户门户基座(硬前置 + 只读视图) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Stand up the customer-facing portal foundation — the two hard prerequisites (fail-closed RLS DSN; CSRF on all POST routes) plus the read-only customer surface (a role-aware dashboard with wallet balance + own campaigns, and an account/billing view), all DB-level tenant-isolated. This is the safe half of the white-paper Week-3 customer portal; the send/write flow is Module E-2.

**Architecture:** Build on Module B's console (`internal/console`: auth, sessions, RBAC, and `withTenantTx` — an RLS-scoped tx on the `app_tenant` pool). Customer reads go exclusively through `withTenantTx`, so Postgres FORCE-RLS (`tenant_isolation` policy on all 9 tenant tables, incl. `campaigns`/`tenant_wallets`/`wallet_ledger`) guarantees a customer sees only its own rows. The dashboard becomes role-aware: staff keep the existing view; customers get balance + campaigns. CSRF is a double-submit-cookie middleware wrapping every POST. The console refuses to boot without the RLS pool DSNs so isolation can never silently degrade.

**Tech Stack:** Go 1.26, pgx/v5, html/template+htmx, testcontainers (PG16 + Redis7). Reuses `internal/console` (Module B), `internal/billing`, `internal/pricing` (Module C-1).

## Global Constraints

- Go module `github.com/acme/wadist`, Go 1.26.4. Toolchain at `/usr/local/go/bin`.
- Integration tests run with `TESTCONTAINERS_RYUK_DISABLED=true`. Never weaken `make gate`.
- **Customer data isolation is DB-enforced**: every customer-scoped read MUST go through `s.withTenantTx(ctx)` (the `app_tenant` RLS pool). Never read customer data via `SystemPool`/`bizPool` in a customer handler. The 9 tenant tables are FORCE-RLS keyed on `app.current_tenant_id`.
- Money is integer minor units; no floats.
- Do NOT change `NewServer`'s signature (Module B + C-1 tests depend on it). Add capability via builder methods / new fields, consistent with C-1's `WithBilling`/`WithPricing`/`WithTenants`.
- All state-mutating POST routes (existing: `/login`, `/logout`, `/admin/tenant/{id}/recharge`, `/admin/tenant/{id}/pricing`) MUST be CSRF-protected after Task 2. Do not exempt any POST without a stated reason.
- Test output pristine; `go vet` clean; whole repo `go build ./...` clean.

---

### Task 1: Fail-closed RLS DSN (console refuses to boot without tenant isolation)

**Files:**
- Modify: `cmd/console/main.go` (validate RLS DSNs in `run`)
- Create: `cmd/console/rls_guard_test.go`

**Interfaces:**
- Produces: `run(ctx)` returns an error (does not start the server) when `WADIST_APP_TENANT_DSN` or `WADIST_APP_SYSTEM_DSN` is empty. Error message names the missing var.

**Why:** Module C-1's final review flagged this as a HARD prerequisite: `store.Manager` falls back to the superuser `bizPool` for the tenant pool when `AppTenantDSN` is unset, which silently disables RLS — a customer would then see other tenants' data. The console (the first component exposing per-tenant data to end users) must fail closed.

- [ ] **Step 1: Read** `cmd/console/main.go` (the `run` function, how it calls `config.Load()` → `baseCfg`, and where it builds the store Manager). Note `config.Config` has `AppTenantDSN` and `AppSystemDSN` fields (from `WADIST_APP_TENANT_DSN` / `WADIST_APP_SYSTEM_DSN`).

- [ ] **Step 2: Write the failing test**

```go
// cmd/console/rls_guard_test.go
package main

import (
	"context"
	"strings"
	"testing"
)

// run must refuse to start without the RLS role DSNs, so customer tenant
// isolation can never silently degrade to the superuser pool.
func TestRunFailsClosedWithoutRLSDSNs(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://app:app@127.0.0.1:5432/wadist?sslmode=disable")
	t.Setenv("WADIST_CONSOLE_SESSION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("WADIST_APP_TENANT_DSN", "") // explicitly unset
	t.Setenv("WADIST_APP_SYSTEM_DSN", "")

	_, _, err := run(context.Background())
	if err == nil {
		t.Fatal("run must fail closed when RLS DSNs are unset")
	}
	if !strings.Contains(err.Error(), "WADIST_APP_TENANT_DSN") {
		t.Fatalf("error should name the missing var, got: %v", err)
	}
}
```

- [ ] **Step 3: Run it (RED).** Run: `/usr/local/go/bin/go test ./cmd/console/ -run TestRunFailsClosedWithoutRLSDSNs -v` → FAIL (run starts / different error).

- [ ] **Step 4: Implement the guard** in `cmd/console/main.go`, immediately after `baseCfg, err := config.Load()` succeeds (before acquiring any resource):

```go
	if baseCfg.AppTenantDSN == "" || baseCfg.AppSystemDSN == "" {
		return nil, nil, fmt.Errorf("console: WADIST_APP_TENANT_DSN and WADIST_APP_SYSTEM_DSN are required (tenant RLS isolation must not fall back to the superuser pool)")
	}
```

Ensure `fmt` is imported. (This runs before `walog.Production()`/`store.Init`, so nothing needs cleanup on this path.)

- [ ] **Step 5: Run it (GREEN)** + confirm the smoke test still skips cleanly without a DSN.

Run: `/usr/local/go/bin/go test ./cmd/console/ -run TestRunFailsClosed -v` → PASS.
Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./cmd/console/` → clean.

> Note: the existing `TestRun_BootsLogin` smoke test sets `WADIST_POSTGRES_DSN` only and skips when unset; with this guard it would now also need the RLS DSNs to actually boot. Since it already skips without `WADIST_POSTGRES_DSN`, it still skips in CI — but if you run it live, set `WADIST_APP_TENANT_DSN`/`WADIST_APP_SYSTEM_DSN` too. Update that smoke test's setup comment to mention the new requirement.

- [ ] **Step 6: Commit**

```bash
git add cmd/console/main.go cmd/console/rls_guard_test.go
git commit -m "feat(console): fail closed when RLS role DSNs are unset (no silent tenant-isolation bypass)"
```

---

### Task 2: CSRF protection (double-submit cookie) on all POST routes

**Files:**
- Create: `internal/console/csrf.go`
- Create: `internal/console/csrf_test.go`
- Modify: `internal/console/server.go` (wrap POST routes; expose token to form-rendering handlers)
- Modify: `internal/console/templates/login.html`, `templates/dashboard.html`, `templates/admin_tenant.html` (hidden CSRF field in every form)
- Modify: handlers that render those forms (`handleLoginPage`, `handleLoginSubmit` error re-render, `handleDashboard`, `handleAdminTenant`) to pass the token

**Interfaces:**
- Produces:
  - `const csrfCookieName = "wadist_csrf"`, `const csrfFormField = "csrf"`.
  - `func (s *Server) issueCSRFToken(w http.ResponseWriter, r *http.Request) string` — returns the request's CSRF token, minting + setting the cookie if absent (idempotent within a request).
  - `func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc` — for POST: rejects (403) when the form field `csrf` (or header `X-CSRF-Token`) is missing or does not constant-time-match the `wadist_csrf` cookie.

- [ ] **Step 1: Write the failing test**

```go
// internal/console/csrf_test.go
package console

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCSRFBlocksPostWithoutToken(t *testing.T) {
	srv, _ := NewServer(testConfig(), nil, nil, nil)
	h := srv.requireCSRF(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// no cookie, no field → 403
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/x", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing token: want 403, got %d", rec.Code)
	}

	// matching cookie + field → pass
	form := url.Values{csrfFormField: {"tok-123"}}
	req := httptest.NewRequest("POST", "/x", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "tok-123"})
	rec2 := httptest.NewRecorder()
	h(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("matching token: want 200, got %d", rec2.Code)
	}

	// cookie present but field mismatched → 403
	req3 := httptest.NewRequest("POST", "/x", strings.NewReader(url.Values{csrfFormField: {"wrong"}}.Encode()))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req3.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "tok-123"})
	rec3 := httptest.NewRecorder()
	h(rec3, req3)
	if rec3.Code != http.StatusForbidden {
		t.Fatalf("mismatched token: want 403, got %d", rec3.Code)
	}
}

func TestIssueCSRFTokenSetsCookieOnce(t *testing.T) {
	srv, _ := NewServer(testConfig(), nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/login", nil)
	tok := srv.issueCSRFToken(rec, req)
	if tok == "" {
		t.Fatal("token must be non-empty")
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), csrfCookieName) {
		t.Fatalf("cookie not set: %q", rec.Header().Get("Set-Cookie"))
	}
}
```

- [ ] **Step 2: Run it (RED).** `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run 'TestCSRF|TestIssueCSRF' -v` → FAIL (`requireCSRF` undefined).

- [ ] **Step 3: Implement `csrf.go`**

```go
// internal/console/csrf.go
package console

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
)

const (
	csrfCookieName = "wadist_csrf"
	csrfFormField  = "csrf"
	csrfHeader     = "X-CSRF-Token"
)

// issueCSRFToken returns the request's CSRF token, minting and setting the
// cookie if the request doesn't already carry one. Safe to call on any GET that
// renders a form. The token is a non-secret double-submit value: defense rests
// on same-origin policy preventing a cross-site page from reading/forging it.
func (s *Server) issueCSRFToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	return tok
}

// requireCSRF rejects a POST whose submitted token (form field or header) does
// not match the cookie. Wrap every state-mutating POST handler with this.
func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(csrfCookieName)
		if err != nil || c.Value == "" {
			http.Error(w, "missing CSRF cookie", http.StatusForbidden)
			return
		}
		sent := r.FormValue(csrfFormField)
		if sent == "" {
			sent = r.Header.Get(csrfHeader)
		}
		if sent == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(c.Value)) != 1 {
			http.Error(w, "bad CSRF token", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
```

- [ ] **Step 4: Run it (GREEN).** Same command as Step 2 → PASS.

- [ ] **Step 5: Wrap all POST routes** in `server.go`'s `Handler()`. Wrap each existing POST handler with `s.requireCSRF(...)`, composing with the existing auth/role wrappers. Examples:

```go
	mux.HandleFunc("POST /login", s.requireCSRF(s.handleLoginSubmit))
	mux.HandleFunc("POST /logout", s.requireAuth(s.requireCSRF(s.handleLogout)))
	// admin writes (Module C-1): requireAuth + adminOnly + requireCSRF
	mux.HandleFunc("POST /admin/tenant/{id}/recharge", s.requireAuth(adminOnly(s.requireCSRF(s.handleAdminRecharge))))
	mux.HandleFunc("POST /admin/tenant/{id}/pricing", s.requireAuth(adminOnly(s.requireCSRF(s.handleAdminSetPrice))))
```

- [ ] **Step 6: Inject the token into every form.** In each GET handler that renders a form, call `tok := s.issueCSRFToken(w, r)` and add `"CSRF": tok` to the template data map:
  - `handleLoginPage` and the error re-render path in `handleLoginSubmit` → add `"CSRF": s.issueCSRFToken(w, r)`.
  - `handleDashboard` → add `"CSRF": ...` (the logout form needs it).
  - `handleAdminTenant` (Module C-1) → add `"CSRF": ...` (recharge + pricing forms).
  Then add a hidden field to each `<form>` in `login.html`, `dashboard.html`, `admin_tenant.html`:

```html
<input type="hidden" name="csrf" value="{{.CSRF}}">
```

- [ ] **Step 7: Update the existing login/logout/admin tests** if they POST without a token (they will now get 403). The cookie-jar based `TestLoginLogoutFlow` and the admin write tests must first GET the form page (which sets the csrf cookie) and submit the token. Add a small test helper:

```go
// csrfFor does a GET to grab the csrf cookie+token the jar will carry, returning the token.
func csrfFor(t *testing.T, client *http.Client, ts *httptest.Server, path string) string {
	t.Helper()
	resp, err := client.Get(ts.URL + path)
	if err != nil { t.Fatalf("csrf GET: %v", err) }
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	// extract value="..." following name="csrf"
	s := string(b)
	i := strings.Index(s, `name="csrf" value="`)
	if i < 0 { t.Fatalf("no csrf field in %s", path) }
	rest := s[i+len(`name="csrf" value="`):]
	return rest[:strings.IndexByte(rest, '"')]
}
```
Then in each POST test, fetch the token (`csrfFor(t, client, ts, "/login")` etc.) and add `csrfFormField: {tok}` to the `url.Values`. The cookie is carried by the jar automatically. Update: `auth_test.go` (login/logout), `admin_write_test.go` (recharge/pricing), and the non-admin 403 test (it gets 403 from RBAC before CSRF, so it can keep working — but add the token so the test asserts RBAC, not CSRF, is the blocker; fetch the token via an admin-less GET to `/login`).

- [ ] **Step 8: Run the full console suite + vet + commit**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/console/`
Expected: all green.

```bash
git add internal/console/csrf.go internal/console/csrf_test.go internal/console/server.go \
        internal/console/templates/login.html internal/console/templates/dashboard.html \
        internal/console/templates/admin_tenant.html internal/console/auth_test.go internal/console/admin_write_test.go
git commit -m "feat(console): CSRF double-submit protection on all POST routes"
```

---

### Task 3: Role-aware customer dashboard (balance + own campaigns, RLS-scoped)

**Files:**
- Modify: `internal/console/server.go` (`handleDashboard` branches by role)
- Create: `internal/console/customer.go` (customer read helpers via `withTenantTx`)
- Create: `internal/console/templates/customer_dashboard.html`
- Create: `internal/console/customer_test.go`

**Interfaces:**
- Produces:
  - `type CampaignRow struct { ID int64; State string; Total, Sent, Failed int }`.
  - `func (s *Server) customerBalance(ctx context.Context) (balance, frozen int64, err error)` — opens `withTenantTx`, reads the session tenant's `tenant_wallets` row (RLS auto-scopes; missing row → 0,0), commits/rolls back.
  - `func (s *Server) customerCampaigns(ctx context.Context, limit int) ([]CampaignRow, error)` — `withTenantTx`, `SELECT id,state,total,sent,failed FROM campaigns ORDER BY id DESC LIMIT n` (RLS auto-scopes to the session tenant).
  - `handleDashboard`: `RoleCustomer` → render `customer_dashboard.html` with balance + campaigns; staff (`admin`/`sales`) → existing dashboard.html.

- [ ] **Step 1: Write the failing test** (customer sees only their own data — the RLS guarantee, proven non-vacuously with two tenants)

```go
// internal/console/customer_test.go
package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
	if err != nil { t.Fatal(err) }
	body := readAll(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("dashboard status %d", resp.StatusCode)
	}
	// sees its own balance (111) and campaign total (50); NOT B's 222/99
	if !strings.Contains(body, "111") || strings.Contains(body, "222") {
		t.Fatalf("RLS leak or missing balance: %s", body)
	}
}
```

Add `mustScan`/`mustExec` helpers (variadic args) in `customer_test.go` if not already present in the package's test files (check first; `loginAs`/`readAll`/`newTestManager`/`newTestRedis` exist from B/C-1).

- [ ] **Step 2: Run it (RED).** `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestCustomerDashboardIsTenantScoped -v` → FAIL.

- [ ] **Step 3: Implement `customer.go`**

```go
// internal/console/customer.go
package console

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type CampaignRow struct {
	ID            int64
	State         string
	Total, Sent, Failed int
}

func (s *Server) customerBalance(ctx context.Context) (balance, frozen int64, err error) {
	tx, err := s.withTenantTx(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT balance, frozen FROM tenant_wallets`).Scan(&balance, &frozen)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("customer balance: %w", err)
	}
	return balance, frozen, nil
}

func (s *Server) customerCampaigns(ctx context.Context, limit int) ([]CampaignRow, error) {
	if limit <= 0 {
		limit = 50
	}
	tx, err := s.withTenantTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id, state::text, total, sent, failed FROM campaigns ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("customer campaigns: %w", err)
	}
	defer rows.Close()
	var out []CampaignRow
	for rows.Next() {
		var c CampaignRow
		if err := rows.Scan(&c.ID, &c.State, &c.Total, &c.Sent, &c.Failed); err != nil {
			return nil, fmt.Errorf("scan campaign: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
```

> Note: `tenant_wallets`/`campaigns` are FORCE-RLS; `withTenantTx` sets `app.current_tenant_id`, so no explicit `WHERE tenant_id` is needed — the policy filters. Do NOT bypass with SystemPool.

- [ ] **Step 4: Make `handleDashboard` role-aware** in `server.go`:

```go
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data, _ := sessionFrom(r.Context())
	if data.Role == RoleCustomer {
		bal, frozen, err := s.customerBalance(r.Context())
		if err != nil {
			http.Error(w, "load balance", http.StatusInternalServerError)
			return
		}
		camps, err := s.customerCampaigns(r.Context(), 50)
		if err != nil {
			http.Error(w, "load campaigns", http.StatusInternalServerError)
			return
		}
		t, err := parsePage("customer_dashboard.html")
		if err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = render(w, t, map[string]any{"Balance": bal, "Frozen": frozen, "Campaigns": camps, "CSRF": s.issueCSRFToken(w, r)})
		return
	}
	// staff: existing dashboard
	t, err := parsePage("dashboard.html")
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"Role": string(data.Role), "CSRF": s.issueCSRFToken(w, r)})
}
```

- [ ] **Step 5: Add `customer_dashboard.html`**

```html
{{define "content"}}
<h1>我的控制台</h1>
<p>钱包余额:<strong>{{.Balance}}</strong> ｜ 冻结:<strong>{{.Frozen}}</strong></p>
<p><a href="/account">账单与充值记录</a></p>
<h2>我的群发任务</h2>
<table>
  <thead><tr><th>ID</th><th>状态</th><th>总数</th><th>成功</th><th>失败</th></tr></thead>
  <tbody>
  {{range .Campaigns}}
    <tr><td>{{.ID}}</td><td>{{.State}}</td><td>{{.Total}}</td><td>{{.Sent}}</td><td>{{.Failed}}</td></tr>
  {{else}}
    <tr><td colspan="5">暂无任务</td></tr>
  {{end}}
  </tbody>
</table>
<form method="post" action="/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit">登出</button></form>
{{end}}
```

- [ ] **Step 6: Run test + full console suite + vet + commit**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/console/ -run TestCustomer -v` then the full package suite.

```bash
git add internal/console/customer.go internal/console/customer_test.go internal/console/server.go internal/console/templates/customer_dashboard.html
git commit -m "feat(console): role-aware customer dashboard (RLS-scoped balance + campaigns)"
```

---

### Task 4: Customer account/billing view (RLS-scoped balance + ledger)

**Files:**
- Modify: `internal/console/customer.go` (add `customerLedger`)
- Modify: `internal/console/server.go` (register `GET /account`, customer-only)
- Create: `internal/console/templates/customer_account.html`
- Modify: `internal/console/customer_test.go` (add the ledger/account test)

**Interfaces:**
- Produces:
  - `func (s *Server) customerLedger(ctx context.Context, limit int) ([]billing.LedgerEntry, error)` — `withTenantTx`, reads `wallet_ledger` newest-first (RLS-scoped); reuse `billing.LedgerEntry` for shape (or a local struct if importing billing into customer.go is undesirable — prefer reusing `billing.LedgerEntry`).
  - `handleCustomerAccount` at `GET /account`, behind `requireAuth` + `requireRole(RoleCustomer)`: renders balance + ledger.

- [ ] **Step 1: Write the failing test** (append to `customer_test.go`)

```go
func TestCustomerAccountShowsOwnLedger(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	srv, _ := NewServer(cfg, users, sessions, mgr)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var tA int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name) VALUES ('A') RETURNING id`, &tA)
	// a ledger row for tenant A (a topup-style credit)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 300)`, tA)
	mustExec(t, mgr, ctx, `INSERT INTO wallet_ledger (tenant_id, kind, delta_balance, delta_frozen, balance_after, frozen_after, idem_key) VALUES ($1,'topup',300,0,300,0,'k-a-1')`, tA)

	custA := loginAs(t, ts, users, sessions, cfg, "a@x.test", RoleCustomer, &tA)
	resp, err := custA.Get(ts.URL + "/account")
	if err != nil { t.Fatal(err) }
	body := readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "topup") || !strings.Contains(body, "300") {
		t.Fatalf("account page missing ledger: %d %s", resp.StatusCode, body)
	}

	// a staff (admin) hitting /account is forbidden (customer-only)
	admin := loginAs(t, ts, users, sessions, cfg, "admin@x.test", RoleAdmin, nil)
	r2, err := admin.Get(ts.URL + "/account")
	if err != nil { t.Fatal(err) }
	r2.Body.Close()
	if r2.StatusCode != http.StatusForbidden {
		t.Fatalf("admin /account: want 403, got %d", r2.StatusCode)
	}
}
```

- [ ] **Step 2: Run it (RED).** → FAIL (`/account` 404 / `customerLedger` undefined).

- [ ] **Step 3: Implement `customerLedger`** in `customer.go` (add `"github.com/acme/wadist/internal/billing"` import):

```go
func (s *Server) customerLedger(ctx context.Context, limit int) ([]billing.LedgerEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	tx, err := s.withTenantTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
SELECT id, charge_id, kind, delta_balance, delta_frozen, balance_after, frozen_after, created_at
  FROM wallet_ledger ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("customer ledger: %w", err)
	}
	defer rows.Close()
	var out []billing.LedgerEntry
	for rows.Next() {
		var e billing.LedgerEntry
		if err := rows.Scan(&e.ID, &e.ChargeID, &e.Kind, &e.DeltaBalance, &e.DeltaFrozen, &e.BalanceAfter, &e.FrozenAfter, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ledger: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Add the handler + route.** In `server.go` `Handler()`:

```go
	custOnly := s.requireRole(RoleCustomer)
	mux.HandleFunc("GET /account", s.requireAuth(custOnly(s.handleCustomerAccount)))
```

Handler:
```go
func (s *Server) handleCustomerAccount(w http.ResponseWriter, r *http.Request) {
	bal, frozen, err := s.customerBalance(r.Context())
	if err != nil { http.Error(w, "load balance", http.StatusInternalServerError); return }
	ledger, err := s.customerLedger(r.Context(), 100)
	if err != nil { http.Error(w, "load ledger", http.StatusInternalServerError); return }
	t, err := parsePage("customer_account.html")
	if err != nil { http.Error(w, "template error", http.StatusInternalServerError); return }
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"Balance": bal, "Frozen": frozen, "Ledger": ledger})
}
```

- [ ] **Step 5: Add `customer_account.html`**

```html
{{define "content"}}
<h1>账单与充值记录</h1>
<p>可用余额:<strong>{{.Balance}}</strong> ｜ 冻结:<strong>{{.Frozen}}</strong></p>
<table>
  <thead><tr><th>时间</th><th>类型</th><th>Δ余额</th><th>Δ冻结</th><th>余额后</th></tr></thead>
  <tbody>
  {{range .Ledger}}
    <tr><td>{{.CreatedAt}}</td><td>{{.Kind}}</td><td>{{.DeltaBalance}}</td><td>{{.DeltaFrozen}}</td><td>{{.BalanceAfter}}</td></tr>
  {{else}}
    <tr><td colspan="5">暂无流水</td></tr>
  {{end}}
  </tbody>
</table>
<p><a href="/">← 返回</a></p>
{{end}}
```

- [ ] **Step 6: Run full console suite + build + vet + commit**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/console/ && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./...`
Expected: all green/clean.

```bash
git add internal/console/customer.go internal/console/customer_test.go internal/console/server.go internal/console/templates/customer_account.html
git commit -m "feat(console): customer account/billing view (RLS-scoped balance + ledger)"
```

---

## Self-Review

**Spec coverage (white-paper Week-3 read-only half + C-1 carried prereqs):**
- Fail-closed RLS DSN (C-1 carried hard prereq) → Task 1. ✓
- CSRF on all POST routes (C-1 carried hard prereq) → Task 2. ✓
- Customer dashboard: balance + own campaigns, tenant-isolated → Task 3 (RLS proven non-vacuously with two tenants). ✓
- Customer account/billing view (流水 + 余额) → Task 4. ✓
- Customer-only RBAC on `/account`; staff 403 → Task 4 test. ✓

**Deferred to Module E-2 (the write/send flow):** number-pack upload + dedup + suppression filter (needs the blind-index HMAC key), Spintax preview endpoint, cost estimate (count × `pricing.PriceFor`), campaign create + submit (RLS `WITH CHECK` inserts via `withTenantTx`). E-1 is read-only + security plumbing.

**Placeholder scan:** test helpers `mustScan`/`mustExec`/`csrfFor` are introduced with explicit "check if present / add if not" notes — flagged, not silent. No TODO/TBD in production code.

**Type consistency:** customer reads all go via `s.withTenantTx` (RLS); `CampaignRow`/`billing.LedgerEntry` reused; CSRF constants (`csrfCookieName`/`csrfFormField`) consistent across middleware, handlers, and templates; no `NewServer` signature change.

**Known follow-up:** the CSRF token is rendered into templates via the data map on each form-rendering handler — when E-2 adds new customer forms, each must include `<input type="hidden" name="csrf" value="{{.CSRF}}">` and its handler must pass `"CSRF"`.

---

## Execution: subagent-driven, one task at a time; task review (spec+quality) after each; whole-branch review at the end.
