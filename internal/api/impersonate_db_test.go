// internal/api/impersonate_db_test.go — DB+Redis-backed tests for the admin
// "impersonate" endpoint (SP8 task 1): mints a live session token for a target
// sales/customer user, refuses admin targets and disabled accounts, and
// audits every use.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/audit"
	"github.com/acme/wadist/internal/console"
)

// newImpersonateServer wires a Server with a real Redis-backed SessionStore
// (like newAuthServer in auth_db_test.go) plus the AuditWriter (like
// newCrudServer in crud_db_test.go) — handleAdminImpersonate needs both.
func newImpersonateServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	pool := testPool(t)
	rdb := newTestRedis(t)
	deps := Deps{
		Sessions:   console.NewSessionStore(rdb, time.Hour),
		SessionKey: []byte("0123456789abcdef0123456789abcdef"),
		Audit:      audit.NewAuditWriter(pool),
	}
	return &Server{sysPool: pool, deps: deps}, context.Background()
}

func TestHandleAdminImpersonate(t *testing.T) {
	s, ctx := newImpersonateServer(t)
	tid, adminID := seedTenantUser(t, ctx, s, "imp-admin@acme.test")

	// a customer user under the tenant
	var custID int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO console_users (email, password_hash, role, tenant_id) VALUES ('imp-cust@acme.test','x','customer',$1) RETURNING id`, tid,
	).Scan(&custID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	w := doJSON(t, s, s.handleAdminImpersonate, http.MethodPost, "/admin/users/x/impersonate", itoa(custID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("impersonate customer: want 200, got %d body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Token    string `json:"token"`
			Role     string `json:"role"`
			TenantID *int64 `json:"tenant_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body.String())
	}
	if resp.Data.Token == "" {
		t.Errorf("want non-empty token, got empty")
	}
	if resp.Data.Role != "customer" {
		t.Errorf("want role=customer, got %q", resp.Data.Role)
	}
	if resp.Data.TenantID == nil || *resp.Data.TenantID != tid {
		t.Errorf("want tenant_id=%d, got %v", tid, resp.Data.TenantID)
	}
	// the returned token must verify against the session key and resolve to a
	// live session with the target's identity.
	sid, valid := console.VerifyCookie(s.deps.SessionKey, resp.Data.Token)
	if !valid {
		t.Fatalf("returned token failed VerifyCookie")
	}
	sd, err := s.deps.Sessions.Get(ctx, sid)
	if err != nil {
		t.Fatalf("session lookup failed: %v", err)
	}
	if sd.UserID != custID {
		t.Errorf("session UserID = %d, want %d", sd.UserID, custID)
	}

	var n int
	s.systemPool().QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action='user.impersonate' AND resource_id=$1`, custID,
	).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 user.impersonate audit row, got %d", n)
	}

	// impersonating an admin is refused.
	w = doJSON(t, s, s.handleAdminImpersonate, http.MethodPost, "/admin/users/x/impersonate", itoa(adminID), "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("impersonate admin: want 400, got %d body %s", w.Code, w.Body.String())
	}

	// impersonating a disabled user is refused.
	var disID int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO console_users (email, password_hash, role, tenant_id, disabled) VALUES ('imp-disabled@acme.test','x','customer',$1,true) RETURNING id`, tid,
	).Scan(&disID); err != nil {
		t.Fatalf("seed disabled customer: %v", err)
	}
	w = doJSON(t, s, s.handleAdminImpersonate, http.MethodPost, "/admin/users/x/impersonate", itoa(disID), "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("impersonate disabled: want 400, got %d body %s", w.Code, w.Body.String())
	}

	// a non-existent id → 404.
	w = doJSON(t, s, s.handleAdminImpersonate, http.MethodPost, "/admin/users/x/impersonate", "999999", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("impersonate missing user: want 404, got %d body %s", w.Code, w.Body.String())
	}

	// a bad id → 400.
	w = doJSON(t, s, s.handleAdminImpersonate, http.MethodPost, "/admin/users/x/impersonate", "not-a-number", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("impersonate bad id: want 400, got %d body %s", w.Code, w.Body.String())
	}
}
