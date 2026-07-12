package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/audit"
	"github.com/acme/wadist/internal/console"
	wlog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/store"
)

func TestContactsSchemaApplies(t *testing.T) {
	pool := testPool(t) // applies all migrations incl. 0021
	ctx := context.Background()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_name IN ('contacts','contact_tags','contact_tag_map','contact_segments','contact_import_batches')`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 5 {
		t.Fatalf("want 5 contact tables, got %d", n)
	}
}

// newContactServer wires a Server with a real Manager (WithTenant/RLS) + BlindKey + Audit.
func newContactServer(t *testing.T) (*Server, *store.Manager, int64) {
	t.Helper()
	ctx := context.Background()
	dsn := testDSN(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyAllMigrations(t, ctx, pool)
	rdb := newTestRedis(t)
	mgr, err := store.NewManager(ctx, store.Config{DSN: dsn, Redis: rdb, NodeID: "contacts-test", BadgerDir: t.TempDir()}, wlog.Noop)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(mgr.Close)
	// seed a tenant to own the contacts
	var tid int64
	if err := pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('acme') RETURNING id`).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	key := make([]byte, 32)
	s := &Server{sysPool: pool, deps: Deps{Mgr: mgr, BlindKey: key, Audit: audit.NewAuditWriter(pool)}}
	return s, mgr, tid
}

// doJSONTenant drives a handler with a session (tenantID(c) reads
// sessionFrom(c).TenantID, not a raw gin context key) and an optional JSON body.
func doJSONTenant(t *testing.T, s *Server, h gin.HandlerFunc, method, target string, tid int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(sessionCtxKey, &console.SessionData{TenantID: &tid, UserID: 0})
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	c.Request = httptest.NewRequest(method, target, rdr)
	c.Request.Header.Set("Content-Type", "application/json")
	h(c)
	return w
}

// doJSONTenantID is doJSONTenant plus a gin :id route param, for the
// PUT/DELETE /contacts/:id handlers.
func doJSONTenantID(t *testing.T, s *Server, h gin.HandlerFunc, method, target string, tid int64, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(sessionCtxKey, &console.SessionData{TenantID: &tid, UserID: 0})
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	c.Request = httptest.NewRequest(method, target, rdr)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: id}}
	h(c)
	return w
}

func TestImportContacts_Dedup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, tid := newContactServer(t)
	// import 3 lines, two of which normalize to the same number
	body := `{"country":"CN","text":"13800138000\n13800138000\n555"}`
	w := doJSONTenant(t, s, s.handleImportContacts, http.MethodPost, "/contacts/import", tid, body)
	if w.Code != http.StatusOK {
		t.Fatalf("import status %d: %s", w.Code, w.Body.String())
	}
	// expect inserted=1, duplicates=1, invalid=1
	if !strings.Contains(w.Body.String(), `"inserted":1`) ||
		!strings.Contains(w.Body.String(), `"duplicates":1`) ||
		!strings.Contains(w.Body.String(), `"invalid":1`) {
		t.Fatalf("report=%s", w.Body.String())
	}
}

func TestImportContacts_CrossBatchDedup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, tid := newContactServer(t)
	body := `{"country":"CN","text":"13800138000"}`
	w1 := doJSONTenant(t, s, s.handleImportContacts, http.MethodPost, "/contacts/import", tid, body)
	if w1.Code != http.StatusOK {
		t.Fatalf("first import status %d: %s", w1.Code, w1.Body.String())
	}
	if !strings.Contains(w1.Body.String(), `"inserted":1`) {
		t.Fatalf("first report=%s", w1.Body.String())
	}
	w2 := doJSONTenant(t, s, s.handleImportContacts, http.MethodPost, "/contacts/import", tid, body)
	if w2.Code != http.StatusOK {
		t.Fatalf("second import status %d: %s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"inserted":0`) || !strings.Contains(w2.Body.String(), `"duplicates":1`) {
		t.Fatalf("second report should be all duplicates: %s", w2.Body.String())
	}
}

func TestContactsCRUD(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, tid := newContactServer(t)

	// Create
	wCreate := doJSONTenant(t, s, s.handleCreateContact, http.MethodPost, "/contacts", tid,
		`{"country":"CN","phone":"13800138000","display_name":"Alice"}`)
	if wCreate.Code != http.StatusOK {
		t.Fatalf("create status %d: %s", wCreate.Code, wCreate.Body.String())
	}
	if !strings.Contains(wCreate.Body.String(), `"phone":"+8613800138000"`) ||
		!strings.Contains(wCreate.Body.String(), `"display_name":"Alice"`) {
		t.Fatalf("create body=%s", wCreate.Body.String())
	}

	// Duplicate create -> 409
	wDup := doJSONTenant(t, s, s.handleCreateContact, http.MethodPost, "/contacts", tid,
		`{"country":"CN","phone":"13800138000"}`)
	if wDup.Code != http.StatusConflict {
		t.Fatalf("dup create status=%d body=%s", wDup.Code, wDup.Body.String())
	}

	// List
	wList := doJSONTenant(t, s, s.handleListContacts, http.MethodGet, "/contacts", tid, "")
	if wList.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", wList.Code, wList.Body.String())
	}
	if !strings.Contains(wList.Body.String(), `"total":1`) || !strings.Contains(wList.Body.String(), `"phone":"+8613800138000"`) {
		t.Fatalf("list body=%s", wList.Body.String())
	}

	// pull the id back out of the pool directly (avoids a JSON-decode round trip)
	var id int64
	if err := s.systemPool().QueryRow(context.Background(),
		`SELECT id FROM contacts WHERE tenant_id=$1`, tid).Scan(&id); err != nil {
		t.Fatalf("lookup contact id: %v", err)
	}

	// Update
	wUpd := doJSONTenantID(t, s, s.handleUpdateContact, http.MethodPut, "/contacts/"+itoa(id), tid, itoa(id),
		`{"display_name":"Bob","status":"unsubscribed"}`)
	if wUpd.Code != http.StatusOK {
		t.Fatalf("update status %d: %s", wUpd.Code, wUpd.Body.String())
	}
	var name, status string
	if err := s.systemPool().QueryRow(context.Background(),
		`SELECT display_name, status::text FROM contacts WHERE id=$1`, id).Scan(&name, &status); err != nil {
		t.Fatalf("post-update lookup: %v", err)
	}
	if name != "Bob" || status != "unsubscribed" {
		t.Fatalf("update did not apply: name=%s status=%s", name, status)
	}

	// Update with no fields -> 400
	wUpdEmpty := doJSONTenantID(t, s, s.handleUpdateContact, http.MethodPut, "/contacts/"+itoa(id), tid, itoa(id), `{}`)
	if wUpdEmpty.Code != http.StatusBadRequest {
		t.Fatalf("empty update status=%d body=%s", wUpdEmpty.Code, wUpdEmpty.Body.String())
	}

	// Delete
	wDel := doJSONTenantID(t, s, s.handleDeleteContact, http.MethodDelete, "/contacts/"+itoa(id), tid, itoa(id), "")
	if wDel.Code != http.StatusOK {
		t.Fatalf("delete status %d: %s", wDel.Code, wDel.Body.String())
	}
	var n int
	if err := s.systemPool().QueryRow(context.Background(),
		`SELECT count(*) FROM contacts WHERE id=$1`, id).Scan(&n); err != nil {
		t.Fatalf("post-delete lookup: %v", err)
	}
	if n != 0 {
		t.Fatalf("contact %d still present after delete", id)
	}

	// Delete again -> 404
	wDel2 := doJSONTenantID(t, s, s.handleDeleteContact, http.MethodDelete, "/contacts/"+itoa(id), tid, itoa(id), "")
	if wDel2.Code != http.StatusNotFound {
		t.Fatalf("re-delete status=%d body=%s", wDel2.Code, wDel2.Body.String())
	}
}

// NOTE: a cross-tenant-isolation test (seed under tid1, list as tid2, expect
// zero rows) was tried here and removed: newContactServer's store.Config only
// sets DSN (no AppTenantDSN/AppSystemDSN), so per internal/store/rls.go's own
// doc comment, WithTenant's tenantPool falls back to the superuser bizPool in
// this "single-DSN dev mode" — the SET LOCAL app.current_tenant_id still runs,
// but Postgres superuser connections bypass RLS/FORCE RLS entirely, so the
// test would fail regardless of handler correctness. Real cross-role RLS
// enforcement (via a dedicated app_tenant DSN) is exercised in
// internal/store/rls_test.go (TestRLS_TenantIsolation), which 0021's policies
// reuse verbatim from 0020.

func TestContactsExport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, tid := newContactServer(t)
	wCreate := doJSONTenant(t, s, s.handleCreateContact, http.MethodPost, "/contacts", tid,
		`{"country":"CN","phone":"13800138000","display_name":"Alice"}`)
	if wCreate.Code != http.StatusOK {
		t.Fatalf("create status %d: %s", wCreate.Code, wCreate.Body.String())
	}

	wExport := doJSONTenant(t, s, s.handleExportContacts, http.MethodGet, "/contacts/export", tid, "")
	if wExport.Code != http.StatusOK {
		t.Fatalf("export status %d: %s", wExport.Code, wExport.Body.String())
	}
	ct := wExport.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("export content-type=%q", ct)
	}
	body := wExport.Body.String()
	if !strings.Contains(body, "phone,country_code,display_name,status,created_at") {
		t.Fatalf("export missing header: %s", body)
	}
	if !strings.Contains(body, "+8613800138000") || !strings.Contains(body, "Alice") {
		t.Fatalf("export missing row: %s", body)
	}
}
