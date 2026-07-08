// internal/api/protected_admin_test.go — SP6 task 2: a configured set of
// "protected" super-admin identifiers is hidden from GET /admin/users AND
// rejected (403) by the user write endpoints, so the bootstrap super-admin
// cannot be listed, edited, disabled, or password-reset from the console.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/audit"
)

func TestParseProtectedAdmins(t *testing.T) {
	m := ParseProtectedAdmins(" admin@x.com , super , ,bob ")
	if len(m) != 3 || !m["admin@x.com"] || !m["super"] || !m["bob"] {
		t.Errorf("parse = %v, want {admin@x.com, super, bob}", m)
	}
	if len(ParseProtectedAdmins("")) != 0 {
		t.Errorf("empty input should yield empty set")
	}
	if len(ParseProtectedAdmins("  ,  , ")) != 0 {
		t.Errorf("blank-only input should yield empty set")
	}
}

func newProtectedServer(t *testing.T, protected ...string) (*Server, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	pool := testPool(t)
	set := map[string]bool{}
	for _, p := range protected {
		set[p] = true
	}
	return &Server{sysPool: pool, deps: Deps{Audit: audit.NewAuditWriter(pool), ProtectedAdmins: set}}, context.Background()
}

func seedAdmin(t *testing.T, ctx context.Context, s *Server, email string) int64 {
	t.Helper()
	var id int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO console_users (email, password_hash, role) VALUES ($1,'x','admin') RETURNING id`, email).Scan(&id); err != nil {
		t.Fatalf("seed admin %s: %v", email, err)
	}
	return id
}

func TestProtectedAdminHiddenAndGuarded(t *testing.T) {
	s, ctx := newProtectedServer(t, "super@wadist.test")
	superID := seedAdmin(t, ctx, s, "super@wadist.test")
	normID := seedAdmin(t, ctx, s, "norm@wadist.test")

	// --- hidden from the list ---
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	s.handleAdminListUsers(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list status %d body %s", w.Code, w.Body.String())
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	emails := map[string]bool{}
	for _, u := range env.Data {
		if e, ok := u["email"].(string); ok {
			emails[e] = true
		}
	}
	if emails["super@wadist.test"] {
		t.Errorf("protected admin must be hidden from list")
	}
	if !emails["norm@wadist.test"] {
		t.Errorf("normal admin must be listed")
	}

	// --- write endpoints reject the protected admin with 403 ---
	if got := doJSON(t, s, s.handleAdminSetUserDisabled, http.MethodPost, "/x", itoa(superID), `{"disabled":true}`).Code; got != http.StatusForbidden {
		t.Errorf("disable protected: want 403, got %d", got)
	}
	if got := doJSON(t, s, s.handleAdminResetUserPassword, http.MethodPost, "/x", itoa(superID), `{"password":"newpass123"}`).Code; got != http.StatusForbidden {
		t.Errorf("reset protected: want 403, got %d", got)
	}
	if got := doJSON(t, s, s.handleAdminUpdateUser, http.MethodPut, "/x", itoa(superID), `{"email":"super@wadist.test","role":"admin"}`).Code; got != http.StatusForbidden {
		t.Errorf("update protected: want 403, got %d", got)
	}

	// --- a normal admin is NOT protected (disable is not 403) ---
	if got := doJSON(t, s, s.handleAdminSetUserDisabled, http.MethodPost, "/x", itoa(normID), `{"disabled":true}`).Code; got == http.StatusForbidden {
		t.Errorf("normal admin disable must not be 403, got %d", got)
	}
}
