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
