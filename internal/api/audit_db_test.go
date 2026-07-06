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

// seedTenantUser inserts a tenant + an admin console user, returning their ids.
func seedTenantUser(t *testing.T, ctx context.Context, s *Server, email string) (int64, int64) {
	t.Helper()
	var tid, uid int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO tenants (name, status) VALUES ('Acme','active') RETURNING id`).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO console_users (email, password_hash, role) VALUES ($1,'x','admin') RETURNING id`, email).Scan(&uid); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return tid, uid
}

func TestHandleAdminListAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	pool := testPool(t)
	s := &Server{sysPool: pool, deps: Deps{Audit: audit.NewAuditWriter(pool)}}
	tid, uid := seedTenantUser(t, ctx, s, "admin@acme.test")

	// Two distinct audit events.
	s.recordAudit(ctx, auditEvent{TenantID: tid, ActorID: uid, Action: "finance.topup",
		ResourceType: "tenant", ResourceID: tid, Details: map[string]any{"amount": 100, "ref": "r1"}})
	s.recordAudit(ctx, auditEvent{TenantID: tid, ActorID: uid, Action: "user.disable",
		ResourceType: "user", ResourceID: uid, Details: map[string]any{"disabled": true}})

	call := func(query string) map[string]any {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/audit"+query, nil)
		s.handleAdminListAudit(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d body %s", w.Code, w.Body.String())
		}
		var env struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return env.Data
	}

	// No filter: both rows, total 2, newest first, join populated.
	all := call("")
	rows := all["rows"].([]any)
	if len(rows) != 2 || all["total"].(float64) != 2 {
		t.Fatalf("want 2 rows/total, got rows=%d total=%v", len(rows), all["total"])
	}
	first := rows[0].(map[string]any)
	if first["action"] != "user.disable" { // id DESC → last inserted first
		t.Errorf("newest-first broken: %v", first["action"])
	}
	if first["actor_email"] != "admin@acme.test" || first["tenant_name"] != "Acme" {
		t.Errorf("join missing: %v", first)
	}

	// Filter by action.
	only := call("?action=finance.topup")
	if len(only["rows"].([]any)) != 1 || only["total"].(float64) != 1 {
		t.Errorf("action filter: %v", only)
	}
	// Filter by actor.
	if call("?actor_id=999999")["total"].(float64) != 0 {
		t.Errorf("unknown actor should return 0")
	}
	// Pagination: limit 1 → 1 row but total still 2.
	pg := call("?limit=1")
	if len(pg["rows"].([]any)) != 1 || pg["total"].(float64) != 2 {
		t.Errorf("limit paging: %v", pg)
	}
}
