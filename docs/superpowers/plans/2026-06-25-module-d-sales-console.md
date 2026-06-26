# Module D — 销售控制台 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** The sales console: a sales user manages the customers (tenants) they own — sees their balances/usage, and sets each customer's per-country unit price. Plus the admin action that establishes the ownership link (assign a tenant's sales owner). Maps to the white-paper sales role ("销售敲定发送国家+单价;看名下用量与账单").

**Architecture:** Sales scoping is **application-layer** (NOT RLS): a sales user owns tenants where `tenants.sales_owner_id = session.UserID`, queried via the BYPASSRLS `SystemPool` with an explicit `WHERE sales_owner_id = $uid` filter and an `salesOwns(tenantID)` ownership guard on every per-tenant action. Reuses Module C-1's `billing` (balance/ledger) and `pricing` (SetPrice/ListForTenant) and E-1's RBAC. Admin gets a "set sales owner" action so the link can be created.

**Tech Stack:** Go 1.26, pgx/v5, html/template+htmx, testcontainers (PG16+Redis7). Reuses `internal/console` (B/C-1/E-1/E-2), `internal/billing`, `internal/pricing`.

## Global Constraints

- Go module `github.com/acme/wadist`, Go 1.26.4. Toolchain `/usr/local/go/bin`. Tests with `TESTCONTAINERS_RYUK_DISABLED=true`.
- Sales data access is via `SystemPool` (cross-tenant) BUT every query/action MUST be filtered/guarded by `sales_owner_id = session.UserID`. A sales user must NEVER see or act on a tenant they don't own — prove this with a negative test in each task that adds a sales surface.
- Money int64 minor units; no floats. Reuse `billing.Balance`/`Ledger` and `pricing.SetPrice`/`GetPrice`/`ListForTenant` (do not duplicate).
- New POST routes use CSRF-outermost (`requireCSRF(requireAuth(roleGuard(handler)))`); every form carries the hidden csrf field and its handler passes `"CSRF"`.
- Do NOT change `NewServer`'s signature. Full console suite + repo build/vet stay green.
- Admin "assign sales owner" must validate the chosen user is `role='sales'` (a tenant's sales owner cannot be a customer/admin).

---

### Task 1: Admin assigns a tenant's sales owner

**Files:**
- Modify: `internal/console/tenants.go` (`SetSalesOwner`, `ListSalesUsers`, add `SalesOwnerID *int64` to `TenantRow`)
- Modify: `internal/console/admin.go` (`handleAdminSetSalesOwner`; pass sales-user list + current owner to the tenant page)
- Modify: `internal/console/server.go` (register `POST /admin/tenant/{id}/sales-owner`)
- Modify: `internal/console/templates/admin_tenant.html` (a "分配销售" form)
- Create: `internal/console/sales_assign_test.go`

**Interfaces:**
- Produces:
  - `type SalesUser struct { ID int64; Email string }`; `func (r *TenantRepo) ListSalesUsers(ctx) ([]SalesUser, error)` — `SELECT id, email FROM console_users WHERE role='sales' ORDER BY email`.
  - `func (r *TenantRepo) SetSalesOwner(ctx, tenantID, salesUserID int64) error` — validates the target is `role='sales'` (else error), then `UPDATE tenants SET sales_owner_id=$1 WHERE id=$2`.
  - `TenantRow.SalesOwnerID *int64` (so the admin page can show current owner).
  - `handleAdminSetSalesOwner` at `POST /admin/tenant/{id}/sales-owner` (admin-only, CSRF): parse `sales_user_id`, call `SetSalesOwner`, 303 → `/admin/tenant/{id}`.

- [ ] **Step 1: Write the failing test**

```go
// internal/console/sales_assign_test.go
package console

import (
	"context"
	"testing"
)

func TestSetSalesOwner(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	repo := NewTenantRepo(mgr.SystemPool())
	users := NewUserRepo(mgr.SystemPool())
	hash, _ := HashPassword("pw")

	var tid int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name) VALUES ('Acme') RETURNING id`, &tid)
	salesID, err := users.Create(ctx, "sales@x.test", hash, RoleSales, nil)
	if err != nil { t.Fatalf("create sales: %v", err) }
	adminID, err := users.Create(ctx, "admin@x.test", hash, RoleAdmin, nil)
	if err != nil { t.Fatalf("create admin: %v", err) }

	// assign a real sales user → ok
	if err := repo.SetSalesOwner(ctx, tid, salesID); err != nil {
		t.Fatalf("assign sales: %v", err)
	}
	got, err := repo.Get(ctx, tid)
	if err != nil || got.SalesOwnerID == nil || *got.SalesOwnerID != salesID {
		t.Fatalf("owner not set: %+v err=%v", got, err)
	}
	// assigning a non-sales user (admin) → error
	if err := repo.SetSalesOwner(ctx, tid, adminID); err == nil {
		t.Fatalf("expected error assigning a non-sales user as owner")
	}
	// list sales users includes our sales, not the admin
	list, err := repo.ListSalesUsers(ctx)
	if err != nil { t.Fatalf("list: %v", err) }
	var sawSales, sawAdmin bool
	for _, u := range list { if u.ID == salesID { sawSales = true }; if u.ID == adminID { sawAdmin = true } }
	if !sawSales || sawAdmin {
		t.Fatalf("ListSalesUsers wrong: sawSales=%v sawAdmin=%v", sawSales, sawAdmin)
	}
}
```

- [ ] **Step 2: Run (RED).** `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestSetSalesOwner -v` → FAIL.

- [ ] **Step 3: Implement in `tenants.go`.** Add `SalesOwnerID *int64` to `TenantRow`; update `List`/`Get` SELECTs to include `sales_owner_id` (scan into `*int64`). Add:

```go
type SalesUser struct {
	ID    int64
	Email string
}

func (r *TenantRepo) ListSalesUsers(ctx context.Context) ([]SalesUser, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, email FROM console_users WHERE role='sales' ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("list sales users: %w", err)
	}
	defer rows.Close()
	var out []SalesUser
	for rows.Next() {
		var u SalesUser
		if err := rows.Scan(&u.ID, &u.Email); err != nil {
			return nil, fmt.Errorf("scan sales user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetSalesOwner assigns a tenant's sales owner; the target must be a sales user.
func (r *TenantRepo) SetSalesOwner(ctx context.Context, tenantID, salesUserID int64) error {
	var role string
	err := r.pool.QueryRow(ctx, `SELECT role FROM console_users WHERE id=$1`, salesUserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("sales owner: user %d not found", salesUserID)
	}
	if err != nil {
		return fmt.Errorf("sales owner: lookup user: %w", err)
	}
	if role != string(RoleSales) {
		return fmt.Errorf("sales owner: user %d is not a sales user (role=%s)", salesUserID, role)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE tenants SET sales_owner_id=$1 WHERE id=$2`, salesUserID, tenantID); err != nil {
		return fmt.Errorf("set sales owner: %w", err)
	}
	return nil
}
```
(Ensure `errors` + `pgx` imported in tenants.go.)

- [ ] **Step 4: Run (GREEN).**

- [ ] **Step 5: Wire the admin handler + route + form.** In `server.go` Handler() add `mux.HandleFunc("POST /admin/tenant/{id}/sales-owner", s.requireAuth(adminOnly(s.requireCSRF(s.handleAdminSetSalesOwner))))` (match the existing admin POST wrapping order). In `admin.go` add `handleAdminSetSalesOwner` (parse `id` path + `sales_user_id` form int → `s.tenants.SetSalesOwner` → on error 400/500 → 303 to `/admin/tenant/{id}`), and update `handleAdminTenant` to also pass `"SalesUsers": s.tenants.ListSalesUsers(...)` and `"Tenant"` (already has SalesOwnerID). In `admin_tenant.html` add (with the CSRF field already present in that template):

```html
<h2>分配销售</h2>
<form method="post" action="/admin/tenant/{{.Tenant.ID}}/sales-owner">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <select name="sales_user_id" required>
    {{range .SalesUsers}}<option value="{{.ID}}">{{.Email}}</option>{{end}}
  </select>
  <button type="submit">保存销售归属</button>
</form>
<p>当前销售归属:{{if .Tenant.SalesOwnerID}}#{{.Tenant.SalesOwnerID}}{{else}}未分配{{end}}</p>
```

- [ ] **Step 6: Run full console suite + vet + commit**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/console/`

```bash
git add internal/console/tenants.go internal/console/admin.go internal/console/server.go internal/console/templates/admin_tenant.html internal/console/sales_assign_test.go
git commit -m "feat(console): admin assigns tenant sales owner (validated role=sales)"
```

---

### Task 2: Sales dashboard — owned customers + balances

**Files:**
- Create: `internal/console/sales.go` (sales read helpers + ownership guard + dashboard handler)
- Modify: `internal/console/server.go` (register `GET /sales`, sales-only)
- Create: `internal/console/templates/sales_dashboard.html`
- Create: `internal/console/sales_test.go`

**Interfaces:**
- Produces:
  - `type SalesCustomerRow struct { TenantRow; Balance, Frozen int64 }`.
  - `func (s *Server) salesOwnedCustomers(ctx) ([]SalesCustomerRow, error)` — reads session UserID, `SELECT id,name,status,sales_owner_id FROM tenants WHERE sales_owner_id=$1` (SystemPool), and `billing.Balance` per tenant.
  - `func (s *Server) salesOwns(ctx, tenantID int64) (bool, error)` — `SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1 AND sales_owner_id=$2)` with session UserID.
  - `handleSalesDashboard` at `GET /sales` (sales-only).

- [ ] **Step 1: Write the failing test** (sales A sees only its own customers)

```go
// internal/console/sales_test.go
package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
	salesA, _ := users.Create(ctx, "sa@x.test", hash, RoleSales, nil)
	salesB, _ := users.Create(ctx, "sb@x.test", hash, RoleSales, nil)
	var tA, tB int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name, sales_owner_id) VALUES ('AcmeA',$1) RETURNING id`, &tA, salesA)
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name, sales_owner_id) VALUES ('AcmeB',$1) RETURNING id`, &tB, salesB)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 111)`, tA)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 222)`, tB)

	// log in as salesA (loginAs needs a tenantID only for customers; pass nil for sales)
	cli := loginAs(t, ts, users, sessions, cfg, "sa@x.test", RoleSales, nil)
	resp, err := cli.Get(ts.URL + "/sales")
	if err != nil { t.Fatal(err) }
	body := readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "AcmeA") || !strings.Contains(body, "111") {
		t.Fatalf("sales dashboard missing own customer: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "AcmeB") || strings.Contains(body, "222") {
		t.Fatalf("sales A leaked sales B's customer: %s", body)
	}

	// a customer hitting /sales → 403
	custCli := loginAs(t, ts, users, sessions, cfg, "c@x.test", RoleCustomer, &tA)
	r2, _ := custCli.Get(ts.URL + "/sales")
	if r2 != nil { defer r2.Body.Close() }
	if r2.StatusCode != http.StatusForbidden {
		t.Fatalf("customer /sales: want 403, got %d", r2.StatusCode)
	}
	_ = salesB
}
```

> Implementer note: `loginAs(t, ts, users, sessions, cfg, email, role, tenantID *int64)` already exists (E-1). For sales/admin pass `nil` tenantID. Add a `billingRepoFor(t, mgr)` helper if not present (returns `billing.NewRepo(mgr.SystemPool())`) — or reuse an existing one (check sales/customer/admin tests for an existing billing-repo test helper; reuse it).

- [ ] **Step 2: Run (RED).**

- [ ] **Step 3: Implement `sales.go`**

```go
// internal/console/sales.go
package console

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

type SalesCustomerRow struct {
	TenantRow
	Balance int64
	Frozen  int64
}

// salesOwns reports whether the session's sales user owns the given tenant.
func (s *Server) salesOwns(ctx context.Context, tenantID int64) (bool, error) {
	data, ok := sessionFrom(ctx)
	if !ok || data.Role != RoleSales {
		return false, errors.New("console: not a sales session")
	}
	var exists bool
	if err := s.mgr.SystemPool().QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1 AND sales_owner_id=$2)`,
		tenantID, data.UserID).Scan(&exists); err != nil {
		return false, fmt.Errorf("sales ownership check: %w", err)
	}
	return exists, nil
}

func (s *Server) salesOwnedCustomers(ctx context.Context) ([]SalesCustomerRow, error) {
	data, ok := sessionFrom(ctx)
	if !ok || data.Role != RoleSales {
		return nil, errors.New("console: not a sales session")
	}
	rows, err := s.mgr.SystemPool().Query(ctx,
		`SELECT id, name, status, sales_owner_id FROM tenants WHERE sales_owner_id=$1 ORDER BY id`, data.UserID)
	if err != nil {
		return nil, fmt.Errorf("sales owned tenants: %w", err)
	}
	defer rows.Close()
	var ts []TenantRow
	for rows.Next() {
		var t TenantRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Status, &t.SalesOwnerID); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		ts = append(ts, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SalesCustomerRow, 0, len(ts))
	for _, t := range ts {
		bal, frozen, err := s.billing.Balance(ctx, t.ID)
		if err != nil {
			return nil, fmt.Errorf("balance for tenant %d: %w", t.ID, err)
		}
		out = append(out, SalesCustomerRow{TenantRow: t, Balance: bal, Frozen: frozen})
	}
	return out, nil
}

func (s *Server) handleSalesDashboard(w http.ResponseWriter, r *http.Request) {
	rows, err := s.salesOwnedCustomers(r.Context())
	if err != nil {
		http.Error(w, "load customers", http.StatusInternalServerError)
		return
	}
	t, err := parsePage("sales_dashboard.html")
	if err != nil {
		http.Error(w, "template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"Customers": rows})
}
```

> Note: drains `rows` fully into a slice BEFORE calling `billing.Balance` (which uses the pool) to avoid "conn busy" on a single connection — the helper collects tenants first, then queries balances. Keep that ordering.

- [ ] **Step 4: Register route** in `server.go`: `salesOnly := s.requireRole(RoleSales)`; `mux.HandleFunc("GET /sales", s.requireAuth(salesOnly(s.handleSalesDashboard)))`.

- [ ] **Step 5: Add `sales_dashboard.html`**

```html
{{define "content"}}
<h1>销售控制台 — 我的客户</h1>
<table>
  <thead><tr><th>ID</th><th>名称</th><th>状态</th><th>余额</th><th>冻结</th><th></th></tr></thead>
  <tbody>
  {{range .Customers}}
    <tr><td>{{.ID}}</td><td>{{.Name}}</td><td>{{.Status}}</td><td>{{.Balance}}</td><td>{{.Frozen}}</td>
        <td><a href="/sales/tenant/{{.ID}}">详情/定价</a></td></tr>
  {{else}}
    <tr><td colspan="6">名下暂无客户</td></tr>
  {{end}}
  </tbody>
</table>
{{end}}
```

- [ ] **Step 6: Run full console suite + vet + commit**

```bash
git add internal/console/sales.go internal/console/sales_test.go internal/console/server.go internal/console/templates/sales_dashboard.html
git commit -m "feat(console): sales dashboard — owned customers + balances (ownership-scoped)"
```

---

### Task 3: Sales tenant detail + set-price (ownership-guarded)

**Files:**
- Modify: `internal/console/sales.go` (`handleSalesTenant`, `handleSalesSetPrice`)
- Modify: `internal/console/server.go` (register `GET /sales/tenant/{id}`, `POST /sales/tenant/{id}/pricing`, sales-only)
- Create: `internal/console/templates/sales_tenant.html`
- Modify: `internal/console/sales_test.go` (add the ownership-guard + set-price tests)

**Interfaces:**
- Produces: `handleSalesTenant` (GET /sales/tenant/{id}: ownership-guard → balance + ledger + current prices); `handleSalesSetPrice` (POST /sales/tenant/{id}/pricing: ownership-guard → `pricing.SetPrice`). Both reject a non-owned tenant with 403.

- [ ] **Step 1: Write the failing tests** (append to `sales_test.go`)

```go
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

	cli := loginAs(t, ts, users, sessions, cfg, "sa@x.test", RoleSales, nil)
	tokA := csrfFor(t, cli, ts, "/sales/tenant/"+itoa(tA))

	// salesA views own tenant → 200
	if r, _ := cli.Get(ts.URL + "/sales/tenant/" + itoa(tA)); r == nil || r.StatusCode != 200 {
		t.Fatalf("own tenant detail not 200")
	}
	// salesA views NOT-owned tenant B → 403
	r2, _ := cli.Get(ts.URL + "/sales/tenant/" + itoa(tB))
	if r2 != nil { defer r2.Body.Close() }
	if r2.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owned detail: want 403, got %d", r2.StatusCode)
	}
	// salesA sets price on own tenant → 303, price stored
	pr, _ := cli.PostForm(ts.URL+"/sales/tenant/"+itoa(tA)+"/pricing", urlValues("csrf", tokA, "country", "US", "unit_price", "7"))
	if pr != nil { defer pr.Body.Close() }
	if pr.StatusCode != http.StatusSeeOther {
		t.Fatalf("own set-price: want 303, got %d", pr.StatusCode)
	}
	if got, err := pricingRepoFor(t, mgr).GetPrice(ctx, tA, "US"); err != nil || got != 7 {
		t.Fatalf("price not stored: %d %v", got, err)
	}
	// salesA sets price on NOT-owned tenant B → 403, nothing stored
	tokB := tokA // same csrf cookie/jar
	pr2, _ := cli.PostForm(ts.URL+"/sales/tenant/"+itoa(tB)+"/pricing", urlValues("csrf", tokB, "country", "US", "unit_price", "9"))
	if pr2 != nil { defer pr2.Body.Close() }
	if pr2.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owned set-price: want 403, got %d", pr2.StatusCode)
	}
	_ = salesB
}
```

> Implementer notes: add tiny helpers if absent — `itoa(int64) string` (= `strconv.FormatInt(v,10)`), `urlValues(kv ...string) url.Values`, `pricingRepoFor(t,mgr)` (= `pricing.NewRepo(mgr.SystemPool())`). Reuse `csrfFor`/`loginAs`. Imports: `net/url`, `strconv`.

- [ ] **Step 2: Run (RED).**

- [ ] **Step 3: Implement handlers in `sales.go`** (ownership guard FIRST in each):

```go
func (s *Server) handleSalesTenant(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil { http.Error(w, "bad id", http.StatusBadRequest); return }
	owns, err := s.salesOwns(r.Context(), id)
	if err != nil { http.Error(w, "ownership", http.StatusInternalServerError); return }
	if !owns { http.Error(w, "forbidden", http.StatusForbidden); return }

	bal, frozen, err := s.billing.Balance(r.Context(), id)
	if err != nil { http.Error(w, "balance", http.StatusInternalServerError); return }
	ledger, err := s.billing.Ledger(r.Context(), id, 50)
	if err != nil { http.Error(w, "ledger", http.StatusInternalServerError); return }
	var prices []pricing.Price
	if s.pricing != nil { prices, _ = s.pricing.ListForTenant(r.Context(), id) }

	t, err := parsePage("sales_tenant.html")
	if err != nil { http.Error(w, "template", http.StatusInternalServerError); return }
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"TenantID": id, "Balance": bal, "Frozen": frozen, "Ledger": ledger, "Prices": prices, "CSRF": s.issueCSRFToken(w, r)})
}

func (s *Server) handleSalesSetPrice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil { http.Error(w, "bad id", http.StatusBadRequest); return }
	owns, err := s.salesOwns(r.Context(), id)
	if err != nil { http.Error(w, "ownership", http.StatusInternalServerError); return }
	if !owns { http.Error(w, "forbidden", http.StatusForbidden); return }
	if s.pricing == nil { http.Error(w, "pricing not configured", http.StatusInternalServerError); return }

	country := strings.ToUpper(strings.TrimSpace(r.FormValue("country")))
	if len(country) != 2 { http.Error(w, "bad country", http.StatusBadRequest); return }
	unit, err := strconv.ParseInt(r.FormValue("unit_price"), 10, 64)
	if err != nil || unit <= 0 { http.Error(w, "bad unit_price", http.StatusBadRequest); return }
	if err := s.pricing.SetPrice(r.Context(), id, country, unit); err != nil {
		http.Error(w, "set price: "+err.Error(), http.StatusInternalServerError); return
	}
	http.Redirect(w, r, "/sales/tenant/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}
```
(Add imports `strconv`, `strings`, `github.com/acme/wadist/internal/pricing` to sales.go.)

- [ ] **Step 4: Register routes** in `server.go` (`salesOnly` from Task 2; CSRF-outermost on the POST):

```go
	mux.HandleFunc("GET /sales/tenant/{id}", s.requireAuth(salesOnly(s.handleSalesTenant)))
	mux.HandleFunc("POST /sales/tenant/{id}/pricing", s.requireCSRF(s.requireAuth(salesOnly(s.handleSalesSetPrice))))
```

- [ ] **Step 5: Add `sales_tenant.html`** (balance + ledger + current prices + set-price form with csrf):

```html
{{define "content"}}
<h1>客户 #{{.TenantID}}</h1>
<p>可用余额:<strong>{{.Balance}}</strong> ｜ 冻结:<strong>{{.Frozen}}</strong></p>
<h2>定价(按国家设单价)</h2>
<form method="post" action="/sales/tenant/{{.TenantID}}/pricing">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <label>国家(2位代码) <input name="country" maxlength="2" required></label>
  <label>单价(最小货币单位) <input type="number" name="unit_price" min="1" required></label>
  <button type="submit">保存单价</button>
</form>
<ul>{{range .Prices}}<li>{{.Country}}: {{.UnitPrice}}</li>{{end}}</ul>
<h2>流水(最近)</h2>
<table>
  <thead><tr><th>时间</th><th>类型</th><th>Δ余额</th><th>余额后</th></tr></thead>
  <tbody>{{range .Ledger}}<tr><td>{{.CreatedAt}}</td><td>{{.Kind}}</td><td>{{.DeltaBalance}}</td><td>{{.BalanceAfter}}</td></tr>{{end}}</tbody>
</table>
<p><a href="/sales">← 返回</a></p>
{{end}}
```

- [ ] **Step 6: Run full console suite + build + vet + commit**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/console/ && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./...`

```bash
git add internal/console/sales.go internal/console/sales_test.go internal/console/server.go internal/console/templates/sales_tenant.html
git commit -m "feat(console): sales tenant detail + set-price (ownership-guarded)"
```

---

## Self-Review

**Spec coverage (white-paper sales role):**
- Sales sees名下客户 (owned tenants only) + balances → Task 2 (ownership-scoped, negative test proves no leak). ✓
- Sales 设发送国家+单价 for owned customers → Task 3 (`handleSalesSetPrice`, ownership-guarded). ✓
- Sales 看名下用量与账单 (balance + ledger) → Task 3. ✓
- Ownership link creatable → Task 1 (admin assign sales owner, validated role=sales). ✓
- RBAC: `/sales*` sales-only (customer/admin → 403); per-tenant ownership guard (non-owned → 403) → Tasks 2 & 3 negative tests. ✓

**Isolation model note:** sales scoping is application-layer (SystemPool + `sales_owner_id` filter + `salesOwns` guard), distinct from customer RLS. Every per-tenant sales action calls `salesOwns` FIRST. This is the security crux — each task that adds a sales surface has a negative (403 / no-leak) test.

**Placeholder scan:** test helpers (`billingRepoFor`, `pricingRepoFor`, `itoa`, `urlValues`) flagged with "add if absent / reuse" notes. No TODO/TBD in production code.

**Type consistency:** `SalesUser`/`SalesCustomerRow`/`TenantRow.SalesOwnerID *int64`; reuses `billing.Balance`/`Ledger`, `pricing.SetPrice`/`GetPrice`/`ListForTenant`; CSRF-outermost on new POSTs; no `NewServer` change.

**Known follow-ups (out of scope):** creating sales/admin login accounts (seeding — carried since Module B); admin "create tenant + customer login" flow (white-paper "新建客户账号" — tenants currently created by admin SQL/seed, not a UI form); CSRF-ordering convention unification (carried).

---

## Execution: subagent-driven, one task at a time; task review (spec+quality) after each; whole-branch review at the end.
