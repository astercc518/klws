# Admin Console SP3b — Edit/Delete CRUD Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-entity edit + (FK-safe) delete/status operations for users, tenants, proxies, and devices, each auditing the change, with row-action UI.

**Architecture:** 7 new handlers in `internal/api/admin_api.go` (all using `s.systemPool()` + SP2's `recordAudit`/`recordAuditTx`), 7 routes, DB round-trip tests via the SP2/SP3a testcontainers harness, and edit/delete dialogs on the four table components. Delete is hard only for proxies (`ON DELETE SET NULL`) and devices (tx-release proxy binding); users/tenants are soft-lifecycle. No engine packages.

**Tech Stack:** Go 1.26 (gin, pgx/v5, pgconn, testcontainers postgres:16); Next.js 16.2.9 frontend.

## Global Constraints

- **No engine-package edits.** Only `internal/api/*` and `frontend/*`.
- **Handlers use `s.systemPool()`** (SP2 seam), so DB tests inject a pool via `&Server{sysPool: pool, deps: Deps{Audit: audit.NewAuditWriter(pool)}}`.
- **Every mutation writes an audit row** via `s.recordAudit(...)` (post-commit) or `s.recordAuditTx(...)` (device delete, in-tx). Password/secrets never in details (n/a here).
- **Delete semantics:** users/tenants NOT hard-deleted (tenant has a suspend status action instead); proxies/devices hard-deleted; device delete must decrement its bound proxy's `current_bindings`.
- **`suspended` is a marker only** — do not add any login/send enforcement.
- **Identity columns immutable:** never edit `account_jid` (device) or `proxy_url` (proxy).
- **Backend tests need Docker** (available). `go test ./internal/api/...`. Backend commits: `go build ./... && go vet ./internal/api/...` first.
- **Frontend:** no JS runner — `npm run build` + `npx eslint <file>` from `/var/klwa/frontend`; accepted house lint `react-hooks/set-state-in-effect`; bar = no NEW error type / no unused-var in touched files. Customized Next 16.2.9 — don't assume training-data Next.
- Branch `feat/admin-sp3b-crud`. Commit after each task with the exact message shown.

---

### Task 1: User edit endpoint + DB test

**Files:**
- Modify: `internal/api/admin_api.go` (add `handleAdminUpdateUser`, `isUniqueViolation`)
- Modify: `internal/api/router.go` (route)
- Test: `internal/api/crud_db_test.go` (new)

**Interfaces:**
- Produces: `PUT /admin/users/:id` `{email, role, tenant_id?}` → `{id,email,role,tenant_id}`; helper `func isUniqueViolation(err error) bool`.
- Consumes: `console.Role`/`RoleAdmin`/`RoleSales`/`RoleCustomer` (already imported), `s.systemPool()`, `recordAudit`, `actorID`, `testPool`/`seedTenantUser` (SP2 harness).

- [ ] **Step 1: Write the failing DB test**

Create `internal/api/crud_db_test.go`:

```go
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/audit"
)

func newCrudServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	pool := testPool(t)
	return &Server{sysPool: pool, deps: Deps{Audit: audit.NewAuditWriter(pool)}}, context.Background()
}

// doJSON drives a handler with an optional JSON body + a single :id param.
func doJSON(t *testing.T, s *Server, h gin.HandlerFunc, method, target, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	c.Request = httptest.NewRequest(method, target, rdr)
	c.Request.Header.Set("Content-Type", "application/json")
	if id != "" {
		c.Params = gin.Params{{Key: "id", Value: id}}
	}
	h(c)
	return w
}

func TestHandleAdminUpdateUser(t *testing.T) {
	s, ctx := newCrudServer(t)
	tid, adminID := seedTenantUser(t, ctx, s, "admin@acme.test") // admin user + tenant
	// a customer user under the tenant
	var custID int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO console_users (email, password_hash, role, tenant_id) VALUES ('cust@acme.test','x','customer',$1) RETURNING id`, tid).Scan(&custID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	// edit customer email + keep role/tenant
	w := doJSON(t, s, s.handleAdminUpdateUser, http.MethodPut, "/admin/users/x", itoa(custID),
		`{"email":"cust2@acme.test","role":"customer","tenant_id":`+itoa(tid)+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("edit status %d body %s", w.Code, w.Body.String())
	}
	var email string
	s.systemPool().QueryRow(ctx, `SELECT email FROM console_users WHERE id=$1`, custID).Scan(&email)
	if email != "cust2@acme.test" {
		t.Errorf("email not updated: %q", email)
	}
	// audit row written
	var n int
	s.systemPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='user.update' AND resource_id=$1`, custID).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 user.update audit, got %d", n)
	}

	// promote customer → admin nulls tenant_id
	w = doJSON(t, s, s.handleAdminUpdateUser, http.MethodPut, "/admin/users/x", itoa(custID),
		`{"email":"cust2@acme.test","role":"admin"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("promote status %d body %s", w.Code, w.Body.String())
	}
	var tenantID *int64
	s.systemPool().QueryRow(ctx, `SELECT tenant_id FROM console_users WHERE id=$1`, custID).Scan(&tenantID)
	if tenantID != nil {
		t.Errorf("admin tenant_id should be null, got %v", *tenantID)
	}

	// duplicate email → 409 (collide with admin@acme.test)
	w = doJSON(t, s, s.handleAdminUpdateUser, http.MethodPut, "/admin/users/x", itoa(custID),
		`{"email":"admin@acme.test","role":"admin"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("dup email want 409, got %d", w.Code)
	}

	// customer without tenant → 400
	w = doJSON(t, s, s.handleAdminUpdateUser, http.MethodPut, "/admin/users/x", itoa(custID),
		`{"email":"c3@acme.test","role":"customer"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("customer-no-tenant want 400, got %d", w.Code)
	}

	// missing id → 404
	w = doJSON(t, s, s.handleAdminUpdateUser, http.MethodPut, "/admin/users/x", "999999",
		`{"email":"z@acme.test","role":"admin"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing user want 404, got %d", w.Code)
	}
	_ = adminID
}
```

Add a tiny helper at the bottom of the test file (`strconv`/`strings` are already in the import block above):

```go
func itoa(v int64) string { return strconv.FormatInt(v, 10) }
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/api/ -run TestHandleAdminUpdateUser`
Expected: FAIL — `s.handleAdminUpdateUser` undefined.

- [ ] **Step 3: Implement the handler + helper + route**

In `internal/api/admin_api.go`, add `"github.com/jackc/pgx/v5/pgconn"` to imports if missing, and:

```go
// isUniqueViolation reports whether err is a Postgres 23505 unique-constraint error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// handleAdminUpdateUser: PUT /api/v1/admin/users/:id {email, role, tenant_id?}.
// Same role/tenant rule as create: customer needs a tenant; admin/sales none.
func (s *Server) handleAdminUpdateUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid user id")
		return
	}
	var req struct {
		Email    string `json:"email" binding:"required,email"`
		Role     string `json:"role" binding:"required"`
		TenantID *int64 `json:"tenant_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body (email, role required)")
		return
	}
	switch console.Role(req.Role) {
	case console.RoleAdmin, console.RoleSales:
		req.TenantID = nil
	case console.RoleCustomer:
		if req.TenantID == nil || *req.TenantID <= 0 {
			fail(c, http.StatusBadRequest, "customer requires a tenant_id")
			return
		}
	default:
		fail(c, http.StatusBadRequest, "role must be admin, sales, or customer")
		return
	}
	tag, err := s.systemPool().Exec(c.Request.Context(),
		`UPDATE console_users SET email=$1, role=$2, tenant_id=$3, updated_at=now() WHERE id=$4`,
		req.Email, req.Role, req.TenantID, id)
	if err != nil {
		if isUniqueViolation(err) {
			fail(c, http.StatusConflict, "email already exists")
			return
		}
		fail(c, http.StatusInternalServerError, "update user failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "user.update", ResourceType: "user", ResourceID: id,
		Details: map[string]any{"email": req.Email, "role": req.Role, "tenant_id": req.TenantID}})
	ok(c, gin.H{"id": id, "email": req.Email, "role": req.Role, "tenant_id": req.TenantID})
}
```

Add the route in `internal/api/router.go` (near the other user routes):

```go
		admin.PUT("/users/:id", s.handleAdminUpdateUser)
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./internal/api/ -run TestHandleAdminUpdateUser -v`
Expected: PASS.

- [ ] **Step 5: Build/vet + commit**

Run: `go build ./... && go vet ./internal/api/...`

```bash
git add internal/api/admin_api.go internal/api/router.go internal/api/crud_db_test.go
git commit -m "feat(api): user edit endpoint (PUT /admin/users/:id) + audit

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Tenant edit + status endpoints + DB test

**Files:**
- Modify: `internal/api/admin_api.go` (`handleAdminUpdateTenant`, `handleAdminSetTenantStatus`)
- Modify: `internal/api/router.go` (2 routes)
- Test: `internal/api/crud_db_test.go` (append)

**Interfaces:**
- Produces: `PUT /admin/tenants/:id` `{name}`; `POST /admin/tenants/:id/status` `{status}`.
- Consumes: `s.systemPool()`, `recordAudit`, `actorID`, `testPool`/`seedTenantUser`, `doJSON`/`itoa` (Task 1).

- [ ] **Step 1: Write failing tests (append to `crud_db_test.go`)**

```go
func TestHandleAdminUpdateTenant(t *testing.T) {
	s, ctx := newCrudServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "t-admin@acme.test")

	w := doJSON(t, s, s.handleAdminUpdateTenant, http.MethodPut, "/admin/tenants/x", itoa(tid), `{"name":"Renamed Inc"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var name string
	s.systemPool().QueryRow(ctx, `SELECT name FROM tenants WHERE id=$1`, tid).Scan(&name)
	if name != "Renamed Inc" {
		t.Errorf("name not updated: %q", name)
	}
	var n int
	s.systemPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='tenant.update' AND resource_id=$1`, tid).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 tenant.update audit, got %d", n)
	}
	// missing id → 404
	if doJSON(t, s, s.handleAdminUpdateTenant, http.MethodPut, "/admin/tenants/x", "999999", `{"name":"x"}`).Code != http.StatusNotFound {
		t.Errorf("missing tenant want 404")
	}
}

func TestHandleAdminSetTenantStatus(t *testing.T) {
	s, ctx := newCrudServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "s-admin@acme.test")

	w := doJSON(t, s, s.handleAdminSetTenantStatus, http.MethodPost, "/admin/tenants/x/status", itoa(tid), `{"status":"suspended"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var st string
	s.systemPool().QueryRow(ctx, `SELECT status FROM tenants WHERE id=$1`, tid).Scan(&st)
	if st != "suspended" {
		t.Errorf("status not updated: %q", st)
	}
	// invalid status → 400
	if doJSON(t, s, s.handleAdminSetTenantStatus, http.MethodPost, "/admin/tenants/x/status", itoa(tid), `{"status":"deleted"}`).Code != http.StatusBadRequest {
		t.Errorf("invalid status want 400")
	}
	// missing id → 404
	if doJSON(t, s, s.handleAdminSetTenantStatus, http.MethodPost, "/admin/tenants/x/status", "999999", `{"status":"active"}`).Code != http.StatusNotFound {
		t.Errorf("missing tenant want 404")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/api/ -run 'TestHandleAdminUpdateTenant|TestHandleAdminSetTenantStatus'`
Expected: FAIL — handlers undefined.

- [ ] **Step 3: Implement handlers + routes**

In `admin_api.go`:

```go
// handleAdminUpdateTenant: PUT /api/v1/admin/tenants/:id {name}.
func (s *Server) handleAdminUpdateTenant(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid tenant id")
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name is required")
		return
	}
	tag, err := s.systemPool().Exec(c.Request.Context(),
		`UPDATE tenants SET name=$1, updated_at=now() WHERE id=$2`, req.Name, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "update tenant failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "tenant not found")
		return
	}
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: id, ActorID: actorID(c),
		Action: "tenant.update", ResourceType: "tenant", ResourceID: id,
		Details: map[string]any{"name": req.Name}})
	ok(c, gin.H{"id": id, "name": req.Name})
}

// handleAdminSetTenantStatus: POST /api/v1/admin/tenants/:id/status {status}.
func (s *Server) handleAdminSetTenantStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid tenant id")
		return
	}
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Status != "active" && req.Status != "suspended") {
		fail(c, http.StatusBadRequest, "status must be 'active' or 'suspended'")
		return
	}
	tag, err := s.systemPool().Exec(c.Request.Context(),
		`UPDATE tenants SET status=$1, updated_at=now() WHERE id=$2`, req.Status, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "update status failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "tenant not found")
		return
	}
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: id, ActorID: actorID(c),
		Action: "tenant.status", ResourceType: "tenant", ResourceID: id,
		Details: map[string]any{"status": req.Status}})
	ok(c, gin.H{"id": id, "status": req.Status})
}
```

Routes in `router.go`:

```go
		admin.PUT("/tenants/:id", s.handleAdminUpdateTenant)
		admin.POST("/tenants/:id/status", s.handleAdminSetTenantStatus)
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/api/ -run 'TestHandleAdminUpdateTenant|TestHandleAdminSetTenantStatus' -v`
Expected: PASS.

- [ ] **Step 5: Build/vet + commit**

Run: `go build ./... && go vet ./internal/api/...`

```bash
git add internal/api/admin_api.go internal/api/router.go internal/api/crud_db_test.go
git commit -m "feat(api): tenant edit + status (suspend/activate) endpoints + audit

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Proxy edit + delete endpoints + DB test

**Files:**
- Modify: `internal/api/admin_api.go` (`handleAdminUpdateProxy`, `handleAdminDeleteProxy`)
- Modify: `internal/api/router.go` (2 routes)
- Test: `internal/api/crud_db_test.go` (append)

**Interfaces:**
- Produces: `PUT /admin/resources/proxies/:id` `{proxy_type, country_code, max_bindings}`; `DELETE /admin/resources/proxies/:id`.
- Consumes: `s.systemPool()`, `recordAudit`, `actorID`, `testPool`, `seedProxy` (SP3a), `doJSON`/`itoa`.

- [ ] **Step 1: Write failing tests (append)**

```go
func TestHandleAdminUpdateProxy(t *testing.T) {
	s, ctx := newCrudServer(t)
	seedProxy(t, ctx, s, "socks5://9.9.9.9:1080", "US", "socks5", true)
	var pid int64
	s.systemPool().QueryRow(ctx, `SELECT id FROM proxy_pool WHERE proxy_url='socks5://9.9.9.9:1080'`).Scan(&pid)
	// bump current_bindings to 2 with max 5 (via a direct set + matching max)
	s.systemPool().Exec(ctx, `UPDATE proxy_pool SET max_bindings=5, current_bindings=2 WHERE id=$1`, pid)

	// valid edit (max_bindings 3 >= current 2)
	w := doJSON(t, s, s.handleAdminUpdateProxy, http.MethodPut, "/admin/resources/proxies/x", itoa(pid),
		`{"proxy_type":"http","country_code":"DE","max_bindings":3}`)
	if w.Code != http.StatusOK {
		t.Fatalf("edit status %d body %s", w.Code, w.Body.String())
	}
	var typ, cc string
	var mb int
	s.systemPool().QueryRow(ctx, `SELECT proxy_type::text, country_code, max_bindings FROM proxy_pool WHERE id=$1`, pid).Scan(&typ, &cc, &mb)
	if typ != "http" || cc != "DE" || mb != 3 {
		t.Errorf("proxy not updated: %s %s %d", typ, cc, mb)
	}
	var n int
	s.systemPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='proxy.update' AND resource_id=$1`, pid).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 proxy.update audit, got %d", n)
	}

	// max_bindings below current (1 < 2) → 400
	if doJSON(t, s, s.handleAdminUpdateProxy, http.MethodPut, "/admin/resources/proxies/x", itoa(pid),
		`{"proxy_type":"http","country_code":"DE","max_bindings":1}`).Code != http.StatusBadRequest {
		t.Errorf("max_bindings<current want 400")
	}
	// missing id → 404
	if doJSON(t, s, s.handleAdminUpdateProxy, http.MethodPut, "/admin/resources/proxies/x", "999999",
		`{"proxy_type":"http","country_code":"DE","max_bindings":3}`).Code != http.StatusNotFound {
		t.Errorf("missing proxy want 404")
	}
}

func TestHandleAdminDeleteProxy(t *testing.T) {
	s, ctx := newCrudServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "px-del@acme.test")
	seedProxy(t, ctx, s, "socks5://8.8.8.8:1080", "US", "socks5", true)
	var pid int64
	s.systemPool().QueryRow(ctx, `SELECT id FROM proxy_pool WHERE proxy_url='socks5://8.8.8.8:1080'`).Scan(&pid)
	// a device bound to this proxy → after delete, its proxy_id should be NULL (ON DELETE SET NULL)
	seedDevice(t, ctx, s, tid, "d-px@wa", "555", "active", "")
	s.systemPool().Exec(ctx, `UPDATE account_devices SET proxy_id=$1 WHERE account_jid='d-px@wa'`, pid)

	w := doJSON(t, s, s.handleAdminDeleteProxy, http.MethodDelete, "/admin/resources/proxies/x", itoa(pid), "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete status %d body %s", w.Code, w.Body.String())
	}
	var exists bool
	s.systemPool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM proxy_pool WHERE id=$1)`, pid).Scan(&exists)
	if exists {
		t.Errorf("proxy should be deleted")
	}
	var devProxy *int64
	s.systemPool().QueryRow(ctx, `SELECT proxy_id FROM account_devices WHERE account_jid='d-px@wa'`).Scan(&devProxy)
	if devProxy != nil {
		t.Errorf("bound device proxy_id should be nulled, got %v", *devProxy)
	}
	// second delete → 404
	if doJSON(t, s, s.handleAdminDeleteProxy, http.MethodDelete, "/admin/resources/proxies/x", itoa(pid), "").Code != http.StatusNotFound {
		t.Errorf("re-delete want 404")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/api/ -run 'TestHandleAdminUpdateProxy|TestHandleAdminDeleteProxy'`
Expected: FAIL — handlers undefined.

- [ ] **Step 3: Implement handlers + routes**

```go
// handleAdminUpdateProxy: PUT /api/v1/admin/resources/proxies/:id
// {proxy_type, country_code, max_bindings}. proxy_url is immutable.
func (s *Server) handleAdminUpdateProxy(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid proxy id")
		return
	}
	var req struct {
		ProxyType   string `json:"proxy_type" binding:"required"`
		CountryCode string `json:"country_code" binding:"required"`
		MaxBindings int    `json:"max_bindings" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "proxy_type, country_code, max_bindings are required")
		return
	}
	if req.ProxyType != "socks5" && req.ProxyType != "http" && req.ProxyType != "https" {
		fail(c, http.StatusBadRequest, "proxy_type must be socks5, http or https")
		return
	}
	if len(req.CountryCode) != 2 || req.MaxBindings < 1 {
		fail(c, http.StatusBadRequest, "country_code must be 2 chars and max_bindings >= 1")
		return
	}
	// The WHERE guard rejects lowering max_bindings below current_bindings
	// (which would violate chk_bindings).
	tag, err := s.systemPool().Exec(c.Request.Context(),
		`UPDATE proxy_pool SET proxy_type=$1::proxy_type_t, country_code=$2, max_bindings=$3, updated_at=now()
		   WHERE id=$4 AND $3 >= current_bindings`,
		req.ProxyType, strings.ToUpper(req.CountryCode), req.MaxBindings, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "update proxy failed")
		return
	}
	if tag.RowsAffected() == 0 {
		// distinguish not-found from max_bindings-too-low
		var exists bool
		s.systemPool().QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM proxy_pool WHERE id=$1)`, id).Scan(&exists)
		if exists {
			fail(c, http.StatusBadRequest, "max_bindings below current bindings")
		} else {
			fail(c, http.StatusNotFound, "proxy not found")
		}
		return
	}
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "proxy.update", ResourceType: "proxy", ResourceID: id,
		Details: map[string]any{"proxy_type": req.ProxyType, "country_code": strings.ToUpper(req.CountryCode), "max_bindings": req.MaxBindings}})
	ok(c, gin.H{"id": id})
}

// handleAdminDeleteProxy: DELETE /api/v1/admin/resources/proxies/:id.
// FK ON DELETE SET NULL nulls account_devices.proxy_id automatically.
func (s *Server) handleAdminDeleteProxy(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid proxy id")
		return
	}
	tag, err := s.systemPool().Exec(c.Request.Context(), `DELETE FROM proxy_pool WHERE id=$1`, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "delete proxy failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "proxy not found")
		return
	}
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "proxy.delete", ResourceType: "proxy", ResourceID: id})
	ok(c, gin.H{"id": id, "deleted": true})
}
```

Routes in `router.go`:

```go
		admin.PUT("/resources/proxies/:id", s.handleAdminUpdateProxy)
		admin.DELETE("/resources/proxies/:id", s.handleAdminDeleteProxy)
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/api/ -run 'TestHandleAdminUpdateProxy|TestHandleAdminDeleteProxy' -v`
Expected: PASS.

- [ ] **Step 5: Build/vet + commit**

Run: `go build ./... && go vet ./internal/api/...`

```bash
git add internal/api/admin_api.go internal/api/router.go internal/api/crud_db_test.go
git commit -m "feat(api): proxy edit + hard delete endpoints + audit

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Device edit + delete endpoints + DB test

The delete is the highest-risk piece: it must release the bound proxy's slot in a transaction.

**Files:**
- Modify: `internal/api/admin_api.go` (`handleAdminUpdateDevice`, `handleAdminDeleteDevice`)
- Modify: `internal/api/router.go` (2 routes)
- Test: `internal/api/crud_db_test.go` (append)

**Interfaces:**
- Produces: `PUT /admin/resources/devices/:id` `{tenant_id?, phone_number?, tags?}`; `DELETE /admin/resources/devices/:id`.
- Consumes: `s.systemPool()`, `recordAudit`/`recordAuditTx`, `actorID`, `errDeviceNotFound` (existing), `pgx`, `testPool`/`seedTenantUser`/`seedDevice`/`seedProxy`, `doJSON`/`itoa`.

- [ ] **Step 1: Write failing tests (append)**

```go
func TestHandleAdminUpdateDevice(t *testing.T) {
	s, ctx := newCrudServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "dv-edit@acme.test")
	seedDevice(t, ctx, s, tid, "dv1@wa", "111", "active", "")
	var did int64
	s.systemPool().QueryRow(ctx, `SELECT id FROM account_devices WHERE account_jid='dv1@wa'`).Scan(&did)

	// edit phone + tags
	w := doJSON(t, s, s.handleAdminUpdateDevice, http.MethodPut, "/admin/resources/devices/x", itoa(did),
		`{"phone_number":"222","tags":["vip","us"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("edit status %d body %s", w.Code, w.Body.String())
	}
	var phone string
	var tags []string
	s.systemPool().QueryRow(ctx, `SELECT phone_number, tags FROM account_devices WHERE id=$1`, did).Scan(&phone, &tags)
	if phone != "222" || len(tags) != 2 {
		t.Errorf("device not updated: %s %v", phone, tags)
	}
	var n int
	s.systemPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='device.update' AND resource_id=$1`, did).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 device.update audit, got %d", n)
	}
	// tenant_id that doesn't exist → 400
	if doJSON(t, s, s.handleAdminUpdateDevice, http.MethodPut, "/admin/resources/devices/x", itoa(did), `{"tenant_id":999999}`).Code != http.StatusBadRequest {
		t.Errorf("bad tenant want 400")
	}
	// no fields → 400
	if doJSON(t, s, s.handleAdminUpdateDevice, http.MethodPut, "/admin/resources/devices/x", itoa(did), `{}`).Code != http.StatusBadRequest {
		t.Errorf("no fields want 400")
	}
	// missing id → 404
	if doJSON(t, s, s.handleAdminUpdateDevice, http.MethodPut, "/admin/resources/devices/x", "999999", `{"phone_number":"9"}`).Code != http.StatusNotFound {
		t.Errorf("missing device want 404")
	}
}

func TestHandleAdminDeleteDevice_releasesBinding(t *testing.T) {
	s, ctx := newCrudServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "dv-del@acme.test")
	seedProxy(t, ctx, s, "socks5://7.7.7.7:1080", "US", "socks5", true)
	var pid int64
	s.systemPool().QueryRow(ctx, `SELECT id FROM proxy_pool WHERE proxy_url='socks5://7.7.7.7:1080'`).Scan(&pid)
	// bind a device to the proxy at bindings=1
	seedDevice(t, ctx, s, tid, "dv-bound@wa", "333", "active", "")
	s.systemPool().Exec(ctx, `UPDATE proxy_pool SET current_bindings=1 WHERE id=$1`, pid)
	s.systemPool().Exec(ctx, `UPDATE account_devices SET proxy_id=$1 WHERE account_jid='dv-bound@wa'`, pid)
	var did int64
	s.systemPool().QueryRow(ctx, `SELECT id FROM account_devices WHERE account_jid='dv-bound@wa'`).Scan(&did)

	w := doJSON(t, s, s.handleAdminDeleteDevice, http.MethodDelete, "/admin/resources/devices/x", itoa(did), "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete status %d body %s", w.Code, w.Body.String())
	}
	// device gone
	var exists bool
	s.systemPool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_devices WHERE id=$1)`, did).Scan(&exists)
	if exists {
		t.Errorf("device should be deleted")
	}
	// proxy binding released
	var cb int
	s.systemPool().QueryRow(ctx, `SELECT current_bindings FROM proxy_pool WHERE id=$1`, pid).Scan(&cb)
	if cb != 0 {
		t.Errorf("proxy current_bindings should be 0, got %d", cb)
	}
	// audit + 404 on re-delete
	var n int
	s.systemPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='device.delete' AND resource_id=$1`, did).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 device.delete audit, got %d", n)
	}
	if doJSON(t, s, s.handleAdminDeleteDevice, http.MethodDelete, "/admin/resources/devices/x", itoa(did), "").Code != http.StatusNotFound {
		t.Errorf("re-delete want 404")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/api/ -run 'TestHandleAdminUpdateDevice|TestHandleAdminDeleteDevice_releasesBinding'`
Expected: FAIL — handlers undefined.

- [ ] **Step 3: Implement handlers + routes**

```go
// handleAdminUpdateDevice: PUT /api/v1/admin/resources/devices/:id
// {tenant_id?, phone_number?, tags?}. account_jid is immutable. Only provided
// fields update.
func (s *Server) handleAdminUpdateDevice(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid device id")
		return
	}
	var req struct {
		TenantID *int64    `json:"tenant_id"`
		Phone    *string   `json:"phone_number"`
		Tags     *[]string `json:"tags"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := c.Request.Context()
	if req.TenantID != nil {
		var exists bool
		s.systemPool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1)`, *req.TenantID).Scan(&exists)
		if !exists {
			fail(c, http.StatusBadRequest, "tenant_id does not exist")
			return
		}
	}
	var sets []string
	var args []any
	details := map[string]any{}
	if req.TenantID != nil {
		args = append(args, *req.TenantID)
		sets = append(sets, fmt.Sprintf("tenant_id=$%d", len(args)))
		details["tenant_id"] = *req.TenantID
	}
	if req.Phone != nil {
		args = append(args, *req.Phone)
		sets = append(sets, fmt.Sprintf("phone_number=$%d", len(args)))
		details["phone_number"] = *req.Phone
	}
	if req.Tags != nil {
		args = append(args, *req.Tags)
		sets = append(sets, fmt.Sprintf("tags=$%d", len(args)))
		details["tags"] = *req.Tags
	}
	if len(sets) == 0 {
		fail(c, http.StatusBadRequest, "no fields to update")
		return
	}
	args = append(args, id)
	q := "UPDATE account_devices SET " + strings.Join(sets, ", ") + ", updated_at=now() WHERE id=$" + strconv.Itoa(len(args))
	tag, err := s.systemPool().Exec(ctx, q, args...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "update device failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "device not found")
		return
	}
	s.recordAudit(ctx, auditEvent{ActorID: actorID(c),
		Action: "device.update", ResourceType: "device", ResourceID: id, Details: details})
	ok(c, gin.H{"id": id})
}

// handleAdminDeleteDevice: DELETE /api/v1/admin/resources/devices/:id. In one tx:
// release the bound proxy's slot (if any), delete the row, audit.
func (s *Server) handleAdminDeleteDevice(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid device id")
		return
	}
	ctx := c.Request.Context()
	err = pgx.BeginTxFunc(ctx, s.systemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
		var proxyID *int64
		e := tx.QueryRow(ctx, `SELECT proxy_id FROM account_devices WHERE id=$1 FOR UPDATE`, id).Scan(&proxyID)
		if errors.Is(e, pgx.ErrNoRows) {
			return errDeviceNotFound
		}
		if e != nil {
			return e
		}
		if proxyID != nil {
			if _, e := tx.Exec(ctx, `UPDATE proxy_pool SET current_bindings=GREATEST(current_bindings-1,0) WHERE id=$1`, *proxyID); e != nil {
				return e
			}
		}
		if _, e := tx.Exec(ctx, `DELETE FROM account_devices WHERE id=$1`, id); e != nil {
			return e
		}
		return s.recordAuditTx(ctx, tx, auditEvent{ActorID: actorID(c),
			Action: "device.delete", ResourceType: "device", ResourceID: id})
	})
	switch {
	case errors.Is(err, errDeviceNotFound):
		fail(c, http.StatusNotFound, "device not found")
		return
	case err != nil:
		fail(c, http.StatusInternalServerError, "delete device failed")
		return
	}
	ok(c, gin.H{"id": id, "deleted": true})
}
```

Routes in `router.go`:

```go
		admin.PUT("/resources/devices/:id", s.handleAdminUpdateDevice)
		admin.DELETE("/resources/devices/:id", s.handleAdminDeleteDevice)
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/api/ -run 'TestHandleAdminUpdateDevice|TestHandleAdminDeleteDevice_releasesBinding' -v`
Expected: PASS.

- [ ] **Step 5: Build/vet + full api test + commit**

Run: `go build ./... && go vet ./internal/api/... && go test ./internal/api/`

```bash
git add internal/api/admin_api.go internal/api/router.go internal/api/crud_db_test.go
git commit -m "feat(api): device edit + delete (releases proxy binding in tx) + audit

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Users table — edit dialog

**Files:**
- Modify: `frontend/components/admin-users-table.tsx`

**Interfaces:**
- Consumes: `PUT /admin/users/:id` (Task 1).

- [ ] **Step 1: Read the file**

Read `frontend/components/admin-users-table.tsx` in full. Note the existing create-user dialog (its email/role/tenant fields + validation), the row-actions dropdown (disable + reset password), and the `load` reload callback.

- [ ] **Step 2: Add an edit action + dialog**

- Add state `const [editTarget, setEditTarget] = useState<AdminUser | null>(null);`.
- In the row-actions dropdown, add an item before/after the existing ones:
  `<DropdownMenuItem onClick={() => setEditTarget(user)}>编辑 Edit</DropdownMenuItem>` (use the file's actual row variable name and `DropdownMenuItem` import).
- Mount `<EditUserDialog target={editTarget} tenants={<the tenants list the create dialog uses>} onClose={() => setEditTarget(null)} onDone={load} />`.
- Implement `EditUserDialog` mirroring the create-user dialog in the same file: prefill email/role/tenant from `target`; the same role→tenant validation (customer needs a tenant; admin/sales hide/clear tenant); submit → `api.put("/admin/users/" + target.id, { email, role, tenant_id })` → success toast → `onClose()` + `onDone()`; `ApiError` toast on failure (409 → "邮箱已存在"); busy-guard + disabled-until-valid. Reset fields when `target` changes.

Use the exact primitives the file already imports (`Dialog*`, `Input`, `Button`, `toast`, `api`, `ApiError`). Match the create dialog's markup.

- [ ] **Step 3: Build + lint**

Run (from `frontend/`): `npm run build && npx eslint components/admin-users-table.tsx`
Expected: build clean; only the accepted `react-hooks/set-state-in-effect`, no new error type / unused var.

- [ ] **Step 4: Commit**

```bash
git add frontend/components/admin-users-table.tsx
git commit -m "feat(admin): users table edit dialog

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Tenants table — edit name + suspend/activate

**Files:**
- Modify: `frontend/components/admin-tenants-table.tsx`

**Interfaces:**
- Consumes: `PUT /admin/tenants/:id` and `POST /admin/tenants/:id/status` (Task 2).

- [ ] **Step 1: Read the file**

Read `frontend/components/admin-tenants-table.tsx` (SP1 built it; it has customer + placeholder rows, a row-actions dropdown gated to customer/placeholder rows with 充值/设单价/指派销售, and a `load` reload). Note the `AdminTenant` shape (`id,name,status,...`).

- [ ] **Step 2: Add edit-name + status actions**

- Add state `const [editNameTarget, setEditNameTarget] = useState<Row | null>(null);`.
- In the customer/placeholder row-actions dropdown add:
  - `编辑名称` → `setEditNameTarget(r)`.
  - `挂起`/`恢复` (label from `r.tenant?.status`: show "挂起" when active, "恢复" when suspended) → calls a `toggleStatus(r)` async that `api.post("/admin/tenants/" + r.tenant_id + "/status", { status: r.tenant?.status === "suspended" ? "active" : "suspended" })` → toast → `load()`.
- Mount an `EditTenantDialog` (single name input, prefilled from `target.tenant?.name`) → `api.put("/admin/tenants/" + target.tenant_id, { name })` → toast → `onClose()`+`onDone()`, mirroring the existing `CreateTenantDialog` in the same file. Reset on target change; busy-guard.

- [ ] **Step 3: Build + lint**

Run: `npm run build && npx eslint components/admin-tenants-table.tsx`
Expected: build clean; only accepted lint, no new error / unused var.

- [ ] **Step 4: Commit**

```bash
git add frontend/components/admin-tenants-table.tsx
git commit -m "feat(admin): tenants table edit-name + suspend/activate

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Proxies table — row actions edit + delete

**Files:**
- Modify: `frontend/components/admin-proxies.tsx`

**Interfaces:**
- Consumes: `PUT /admin/resources/proxies/:id` + `DELETE /admin/resources/proxies/:id` (Task 3).

- [ ] **Step 1: Read the file**

Read `frontend/components/admin-proxies.tsx` (SP3a made it server-mode: `load` refetches the current page; `Proxy` type has `id, proxy_type, country_code, max_bindings, current_bindings, is_alive, ...`). It currently passes NO `rowActions` to `ProDataTable`.

- [ ] **Step 2: Add a `rowActions` dropdown with edit + delete**

- Add state `editTarget`/`deleteTarget` (`Proxy | null`).
- Pass `rowActions={(p) => (<DropdownMenu>…编辑 / 删除…</DropdownMenu>)}` to `ProDataTable` (import `DropdownMenu*`, `Button`, `MoreHorizontal` from lucide as the other tables do). 删除 opens a confirm dialog.
- `EditProxyDialog`: fields proxy_type (`<select>` socks5/http/https), country_code (2-char), max_bindings (number ≥1). Prefill from `target`. Submit → `api.put("/admin/resources/proxies/" + target.id, { proxy_type, country_code, max_bindings })` → toast (400 "max_bindings below current bindings" surfaced from `ApiError.message`) → `onClose()` + `load()`.
- `DeleteProxyDialog` (confirm): "确认删除代理 <url>?此操作不可撤销,已绑定的设备将自动解绑。" → `api.delete("/admin/resources/proxies/" + target.id)` → toast → `onClose()` + `load()`.
- Both busy-guarded; mirror the existing `ImportProxiesDialog` structure for markup.

- [ ] **Step 3: Build + lint**

Run: `npm run build && npx eslint components/admin-proxies.tsx`
Expected: build clean; only accepted lint, no new error / unused var.

- [ ] **Step 4: Commit**

```bash
git add frontend/components/admin-proxies.tsx
git commit -m "feat(admin): proxies table row actions — edit + delete

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Devices table — row actions edit + delete

**Files:**
- Modify: `frontend/components/admin-devices.tsx`

**Interfaces:**
- Consumes: `PUT /admin/resources/devices/:id` + `DELETE /admin/resources/devices/:id` (Task 4).

- [ ] **Step 1: Read the file**

Read `frontend/components/admin-devices.tsx` (SP3a made it server-mode; it already has a row-actions menu with "配置网络" and a `load` that refetches the current page; the device row type has `id, tenant_id, account_jid, phone_number, tags, proxy_id, …`).

- [ ] **Step 2: Add edit + delete to the row-actions menu**

- Add state `editTarget`/`deleteTarget`.
- In the existing row-actions dropdown, add `编辑 Edit` and `删除 Delete` items alongside "配置网络".
- `EditDeviceDialog`: fields tenant_id (number), phone_number (text), tags (comma/space-separated → string[]). Prefill from target. Submit only the changed/provided fields → `api.put("/admin/resources/devices/" + target.id, { tenant_id?, phone_number?, tags? })` → toast (400 surfaced) → `onClose()` + `load()`. Keep it simple: send all three fields (tenant_id as number, phone as string, tags as array) — the backend updates the provided ones; empty tags array is allowed.
- `DeleteDeviceDialog` (confirm): "确认删除设备 <account_jid>?此操作不可撤销,若已绑定代理会自动释放。" → `api.delete("/admin/resources/devices/" + target.id)` → toast → `onClose()` + `load()`.
- Busy-guarded; mirror the file's existing dialog markup.

- [ ] **Step 3: Build + lint**

Run: `npm run build && npx eslint components/admin-devices.tsx`
Expected: build clean; only accepted lint, no new error / unused var.

- [ ] **Step 4: Manual smoke (if a live API is available; else note it)**

Across all four tables: edit dialogs prefill + save + reload; deletes confirm + remove + reload; tenant suspend/activate toggles the badge; a device delete releases its proxy's binding count. If no live API, state the gap in the report.

- [ ] **Step 5: Commit**

```bash
git add frontend/components/admin-devices.tsx
git commit -m "feat(admin): devices table row actions — edit + delete

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review (completed by author)

**Spec coverage:** User edit → Task 1; tenant edit+status → Task 2; proxy edit+delete → Task 3; device edit+delete (tx binding release) → Task 4; users/tenants/proxies/devices row-action UI → Tasks 5/6/7/8. Every mutation audits (Tasks 1-4). Delete semantics: users/tenants none (tenant status action), proxies/devices hard (Tasks 3/4). `max_bindings ≥ current_bindings` (Task 3), device tenant-exists check + tx binding release (Task 4), user role/tenant rule + 409 dup email (Task 1). Suspended is a marker (no enforcement anywhere). ✓

**Placeholder scan:** backend steps have full code. Frontend Tasks 5-8 give targeted dialog specs + direct reading each file (reproducing 200-540-line files verbatim would be error-prone); each dialog's endpoint, fields, validation, and reload are fully specified, mirroring an existing dialog in the same file.

**Type consistency:** all handlers use `s.systemPool()` + `recordAudit`/`recordAuditTx` + `auditEvent{...}` (SP2), `isUniqueViolation` defined in Task 1 and reused conceptually only there, `errDeviceNotFound` (existing) in Task 4. Test helpers `newCrudServer`/`doJSON`/`itoa` defined in Task 1 and reused in Tasks 2-4; `seedTenantUser` (SP2), `seedDevice`/`seedProxy` (SP3a) reused. Routes match the frontend calls (`PUT /admin/users/:id`, `PUT /admin/tenants/:id`, `POST /admin/tenants/:id/status`, `PUT|DELETE /admin/resources/proxies/:id`, `PUT|DELETE /admin/resources/devices/:id`). `api.put`/`api.delete` exist in `lib/api.ts`.
