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

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

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
