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
	"github.com/acme/wadist/internal/crypto"
	wlog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/pricing"
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
	s := &Server{sysPool: pool, deps: Deps{Mgr: mgr, BlindKey: key, Audit: audit.NewAuditWriter(pool), Pricing: pricing.NewRepo(pool)}}
	return s, mgr, tid
}

// blind returns the blind index a test would need to seed for a phone to
// match what the handler computes via crypto.BlindIndex(s.deps.BlindKey, ...).
func blind(s *Server, phone string) []byte {
	return crypto.BlindIndex(s.deps.BlindKey, phone)
}

// seedPriceAndWallet sets a per-country unit price and tops up the tenant's
// wallet so handleCreateCampaign's pre-flight balance guard passes.
func seedPriceAndWallet(t *testing.T, ctx context.Context, s *Server, tid int64, country string) {
	t.Helper()
	if err := s.deps.Pricing.SetPrice(ctx, tid, country, 100); err != nil {
		t.Fatalf("set price: %v", err)
	}
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO tenant_wallets (tenant_id, balance, frozen) VALUES ($1, $2, 0)`, tid, 100000); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
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

func TestApplyTagAndSegmentPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, tid := newContactServer(t)
	// seed 2 contacts + 1 tag directly (systemPool bypasses RLS in tests)
	ctx := context.Background()
	var c1, c2, tag int64
	s.systemPool().QueryRow(ctx, `INSERT INTO contacts (tenant_id,phone,phone_bidx,country_code) VALUES ($1,'+8613800138000','\x01','CN') RETURNING id`, tid).Scan(&c1)
	s.systemPool().QueryRow(ctx, `INSERT INTO contacts (tenant_id,phone,phone_bidx,country_code) VALUES ($1,'+8613800138001','\x02','CN') RETURNING id`, tid).Scan(&c2)
	s.systemPool().QueryRow(ctx, `INSERT INTO contact_tags (tenant_id,name) VALUES ($1,'vip') RETURNING id`, tid).Scan(&tag)

	// apply tag to c1 only
	body := `{"contact_ids":[` + itoa(c1) + `]}`
	w := doJSONTenantID(t, s, s.handleApplyTag, http.MethodPost, "/contacts/tags/x/apply", tid, itoa(tag), body)
	if w.Code != http.StatusOK {
		t.Fatalf("apply %d: %s", w.Code, w.Body.String())
	}

	// preview a segment {tags:[tag]} -> expect count 1
	var segID int64
	s.systemPool().QueryRow(ctx, `INSERT INTO contact_segments (tenant_id,name,filter) VALUES ($1,'seg',$2) RETURNING id`,
		tid, `{"tags":[`+itoa(tag)+`]}`).Scan(&segID)
	w2 := doJSONTenantID(t, s, s.handleSegmentPreview, http.MethodGet, "/contacts/segments/x/preview", tid, itoa(segID), "")
	if w2.Code != http.StatusOK || !strings.Contains(w2.Body.String(), `"count":1`) {
		t.Fatalf("preview=%d %s", w2.Code, w2.Body.String())
	}
	_ = c2
}

func TestContactTagMapCrossTenantFKRejected(t *testing.T) {
	s, _, tid := newContactServer(t)
	ctx := context.Background()
	var otherTid int64
	if err := s.systemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('other') RETURNING id`).Scan(&otherTid); err != nil {
		t.Fatalf("seed other tenant: %v", err)
	}
	var cid, tagID int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO contacts (tenant_id,phone,phone_bidx,country_code) VALUES ($1,'+8613800138002','\x03','CN') RETURNING id`,
		tid).Scan(&cid); err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO contact_tags (tenant_id,name) VALUES ($1,'x') RETURNING id`, tid).Scan(&tagID); err != nil {
		t.Fatalf("seed tag: %v", err)
	}
	// tenant_id doesn't match the contact/tag's actual tenant -> composite FK must reject
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO contact_tag_map (tenant_id, contact_id, tag_id) VALUES ($1,$2,$3)`,
		otherTid, cid, tagID); err == nil {
		t.Fatalf("expected FK violation inserting cross-tenant tag_map row")
	}
}

// ---------------------------------------------------------------------------
// Suppression (manual blacklist / opt-out) management
// ---------------------------------------------------------------------------

func TestSuppressionAddAndList(t *testing.T) {
	s, _, tid := newContactServer(t)
	body := `{"country":"CN","phones":["13800138000"],"reason":"opt-out"}`
	w := doJSONTenant(t, s, s.handleAddSuppression, http.MethodPost, "/suppression", tid, body)
	if w.Code != http.StatusOK {
		t.Fatalf("add %d: %s", w.Code, w.Body.String())
	}
	w2 := doJSONTenant(t, s, s.handleListSuppression, http.MethodGet, "/suppression", tid, "")
	if !strings.Contains(w2.Body.String(), `"total":1`) {
		t.Fatalf("list=%s", w2.Body.String())
	}
	// the list must never leak the phone number — suppression_list only stores
	// the blind index, so the row can only expose id/reason/added_at.
	if strings.Contains(w2.Body.String(), "13800138000") {
		t.Fatalf("list leaked phone number: %s", w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"reason":"opt-out"`) {
		t.Fatalf("list missing reason: %s", w2.Body.String())
	}
}

func TestSuppressionAdd_DedupAndInvalid(t *testing.T) {
	s, _, tid := newContactServer(t)
	// two phones normalize to the same e164, one is invalid
	body := `{"country":"CN","phones":["13800138000","13800138000","5"],"reason":"spam"}`
	w := doJSONTenant(t, s, s.handleAddSuppression, http.MethodPost, "/suppression", tid, body)
	if w.Code != http.StatusOK {
		t.Fatalf("add %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"inserted":1`) ||
		!strings.Contains(w.Body.String(), `"duplicates":1`) ||
		!strings.Contains(w.Body.String(), `"invalid":1`) {
		t.Fatalf("report=%s", w.Body.String())
	}
	// re-adding the same phone in a second request must be a cross-batch
	// duplicate (ON CONFLICT DO NOTHING), not a second row.
	w2 := doJSONTenant(t, s, s.handleAddSuppression, http.MethodPost, "/suppression", tid,
		`{"country":"CN","phones":["13800138000"],"reason":"again"}`)
	if !strings.Contains(w2.Body.String(), `"inserted":0`) || !strings.Contains(w2.Body.String(), `"duplicates":1`) {
		t.Fatalf("second add should be all duplicates: %s", w2.Body.String())
	}
	wList := doJSONTenant(t, s, s.handleListSuppression, http.MethodGet, "/suppression", tid, "")
	if !strings.Contains(wList.Body.String(), `"total":1`) {
		t.Fatalf("list=%s", wList.Body.String())
	}
}

// TestListSuppression_TenantIsolation guards a live-reproduced bug: because
// suppression_list is NOT RLS-protected (0008 only GRANT/REVOKEs it, never
// runs it through an ENABLE/FORCE ROW LEVEL SECURITY loop), the list handler
// MUST filter tenant_id explicitly or one tenant sees every tenant's rows.
// Meaningful even under the superuser harness precisely because the fix is an
// explicit WHERE, not RLS.
func TestListSuppression_TenantIsolation(t *testing.T) {
	s, _, tid := newContactServer(t)
	ctx := context.Background()
	// second tenant with its own suppression rows
	var otherTid int64
	if err := s.systemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('other') RETURNING id`).Scan(&otherTid); err != nil {
		t.Fatalf("seed other tenant: %v", err)
	}
	// tenant 1: one row; tenant 2: two rows — all inserted with explicit tenant_id.
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO suppression_list (tenant_id, phone_bidx, reason) VALUES ($1,'\x01','mine')`, tid); err != nil {
		t.Fatalf("seed t1 row: %v", err)
	}
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO suppression_list (tenant_id, phone_bidx, reason) VALUES ($1,'\x02','theirs-a'),($1,'\x03','theirs-b')`, otherTid); err != nil {
		t.Fatalf("seed t2 rows: %v", err)
	}
	w := doJSONTenant(t, s, s.handleListSuppression, http.MethodGet, "/suppression", tid, "")
	if w.Code != http.StatusOK {
		t.Fatalf("list %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"total":1`) {
		t.Fatalf("want total:1 (only tenant 1's row), got %s", body)
	}
	if !strings.Contains(body, `"reason":"mine"`) {
		t.Fatalf("missing tenant 1's row: %s", body)
	}
	if strings.Contains(body, "theirs-a") || strings.Contains(body, "theirs-b") {
		t.Fatalf("leaked another tenant's suppression rows: %s", body)
	}
}

func TestSuppressionImport(t *testing.T) {
	s, _, tid := newContactServer(t)
	body := `{"country":"CN","text":"13800138000\n13800138000\n5","reason":"bulk import"}`
	w := doJSONTenant(t, s, s.handleImportSuppression, http.MethodPost, "/suppression/import", tid, body)
	if w.Code != http.StatusOK {
		t.Fatalf("import %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"inserted":1`) ||
		!strings.Contains(w.Body.String(), `"duplicates":1`) ||
		!strings.Contains(w.Body.String(), `"invalid":1`) {
		t.Fatalf("report=%s", w.Body.String())
	}
	wList := doJSONTenant(t, s, s.handleListSuppression, http.MethodGet, "/suppression", tid, "")
	if !strings.Contains(wList.Body.String(), `"total":1`) {
		t.Fatalf("list=%s", wList.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Build a campaign from a saved segment (Task 7).
// ---------------------------------------------------------------------------

// TestCreateCampaignFromSegment_ExcludesSuppressed: two active CN contacts
// share a tag; one of them is on the suppression list. A campaign built from
// a segment on that tag must resolve to exactly the non-suppressed contact.
func TestCreateCampaignFromSegment_ExcludesSuppressed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, tid := newContactServer(t)
	ctx := context.Background()
	// price so the balance guard passes; topup wallet
	seedPriceAndWallet(t, ctx, s, tid, "CN")

	var tag int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO contact_tags (tenant_id,name) VALUES ($1,'seg') RETURNING id`, tid).Scan(&tag); err != nil {
		t.Fatalf("seed tag: %v", err)
	}
	// two active CN contacts, one of them suppressed
	bidxA := blind(s, "+8613800138000")
	bidxB := blind(s, "+8613800138001")
	var a, b int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO contacts (tenant_id,phone,phone_bidx,country_code,status) VALUES ($1,'+8613800138000',$2,'CN','active') RETURNING id`,
		tid, bidxA).Scan(&a); err != nil {
		t.Fatalf("seed contact a: %v", err)
	}
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO contacts (tenant_id,phone,phone_bidx,country_code,status) VALUES ($1,'+8613800138001',$2,'CN','active') RETURNING id`,
		tid, bidxB).Scan(&b); err != nil {
		t.Fatalf("seed contact b: %v", err)
	}
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO contact_tag_map (tenant_id,contact_id,tag_id) VALUES ($1,$2,$3),($1,$4,$3)`,
		tid, a, tag, b); err != nil {
		t.Fatalf("seed tag map: %v", err)
	}
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO suppression_list (tenant_id, phone_bidx, reason) VALUES ($1,$2,'x')`, tid, bidxB); err != nil {
		t.Fatalf("seed suppression: %v", err)
	}
	var segID int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO contact_segments (tenant_id,name,filter) VALUES ($1,'s',$2) RETURNING id`,
		tid, `{"tags":[`+itoa(tag)+`],"status":"active"}`).Scan(&segID); err != nil {
		t.Fatalf("seed segment: %v", err)
	}

	body := `{"country":"CN","body":"hi {name}","segment_id":` + itoa(segID) + `}`
	w := doJSONTenant(t, s, s.handleCreateCampaign, http.MethodPost, "/campaigns", tid, body)
	if w.Code != http.StatusOK {
		t.Fatalf("create %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"total":1`) { // B excluded by suppression
		t.Fatalf("want total 1 (suppressed excluded), got %s", w.Body.String())
	}
}

// TestCreateCampaign_PhonesPathStillWorks guards the phones[] regression: the
// segment branch must not have disturbed the original paste-numbers flow.
func TestCreateCampaign_PhonesPathStillWorks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, tid := newContactServer(t)
	ctx := context.Background()
	seedPriceAndWallet(t, ctx, s, tid, "CN")

	body := `{"country":"CN","body":"hi {name}","phones":["+8613800138000","+8613800138001"]}`
	w := doJSONTenant(t, s, s.handleCreateCampaign, http.MethodPost, "/campaigns", tid, body)
	if w.Code != http.StatusOK {
		t.Fatalf("create %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"total":2`) {
		t.Fatalf("want total 2, got %s", w.Body.String())
	}
}

// TestCreateCampaign_RequiresExactlyOneOfPhonesOrSegment covers both the
// neither-provided and both-provided cases -> 400.
func TestCreateCampaign_RequiresExactlyOneOfPhonesOrSegment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, tid := newContactServer(t)
	ctx := context.Background()
	seedPriceAndWallet(t, ctx, s, tid, "CN")

	wNeither := doJSONTenant(t, s, s.handleCreateCampaign, http.MethodPost, "/campaigns", tid,
		`{"country":"CN","body":"hi"}`)
	if wNeither.Code != http.StatusBadRequest {
		t.Fatalf("neither: status=%d body=%s", wNeither.Code, wNeither.Body.String())
	}

	var segID int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO contact_segments (tenant_id,name,filter) VALUES ($1,'s2',$2) RETURNING id`,
		tid, `{}`).Scan(&segID); err != nil {
		t.Fatalf("seed segment: %v", err)
	}
	wBoth := doJSONTenant(t, s, s.handleCreateCampaign, http.MethodPost, "/campaigns", tid,
		`{"country":"CN","body":"hi","phones":["+8613800138000"],"segment_id":`+itoa(segID)+`}`)
	if wBoth.Code != http.StatusBadRequest {
		t.Fatalf("both: status=%d body=%s", wBoth.Code, wBoth.Body.String())
	}
}

// TestCreateCampaignFromSegment_EmptyResolvesTo400 guards against creating an
// empty campaign: a segment that resolves to nobody must 400, not create a
// zero-recipient campaign row.
func TestCreateCampaignFromSegment_EmptyResolvesTo400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, tid := newContactServer(t)
	ctx := context.Background()
	seedPriceAndWallet(t, ctx, s, tid, "CN")

	var segID int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO contact_segments (tenant_id,name,filter) VALUES ($1,'empty',$2) RETURNING id`,
		tid, `{"status":"active"}`).Scan(&segID); err != nil {
		t.Fatalf("seed segment: %v", err)
	}
	w := doJSONTenant(t, s, s.handleCreateCampaign, http.MethodPost, "/campaigns", tid,
		`{"country":"CN","body":"hi","segment_id":`+itoa(segID)+`}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var n int
	if err := s.systemPool().QueryRow(ctx, `SELECT count(*) FROM campaigns WHERE tenant_id=$1`, tid).Scan(&n); err != nil {
		t.Fatalf("count campaigns: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no campaign row created for empty segment, got %d", n)
	}
}

// seedAdminContact inserts one contacts row directly via systemPool (bypassing
// RLS/WithTenant, like a real cross-tenant seed would need to).
func seedAdminContact(t *testing.T, ctx context.Context, s *Server, tid int64, phone, cc string) {
	t.Helper()
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO contacts (tenant_id, phone, phone_bidx, country_code, source)
		 VALUES ($1,$2,$3,$4,'manual')`,
		tid, phone, []byte(phone), cc); err != nil {
		t.Fatalf("seed contact: %v", err)
	}
}

// TestHandleAdminListContacts covers the two shapes the god-view list must
// support: filtered to one tenant via ?tenant_id=, and unfiltered (all
// tenants) — proving the handler uses s.systemPool() (BYPASSRLS) rather than
// WithTenant, since the caller here never scopes a session to any tenant.
func TestHandleAdminListContacts(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid1, _ := seedTenantUser(t, ctx, s, "adminc1@acme.test")
	tid2, _ := seedTenantUser(t, ctx, s, "adminc2@acme.test")
	seedAdminContact(t, ctx, s, tid1, "+15550001", "US")
	seedAdminContact(t, ctx, s, tid1, "+15550002", "US")
	seedAdminContact(t, ctx, s, tid2, "+8613800138000", "CN")

	// scoped to tid1 -> 2 rows, both tenant_id=tid1
	w := doGET(t, s, s.handleAdminListContacts, "/admin/contacts?tenant_id="+itoa(tid1))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"total":2`) {
		t.Errorf("want total 2 for tid1, got %s", body)
	}
	if contains(body, `"tenant_id":`+itoa(tid2)) {
		t.Errorf("tid1-scoped list leaked tid2 row: %s", body)
	}

	// unscoped -> all tenants, 3 rows
	wAll := doGET(t, s, s.handleAdminListContacts, "/admin/contacts")
	if wAll.Code != http.StatusOK {
		t.Fatalf("status %d body %s", wAll.Code, wAll.Body.String())
	}
	bodyAll := wAll.Body.String()
	if !contains(bodyAll, `"total":3`) {
		t.Errorf("want total 3 unscoped, got %s", bodyAll)
	}
	if !contains(bodyAll, `"tenant_id":`+itoa(tid1)) || !contains(bodyAll, `"tenant_id":`+itoa(tid2)) {
		t.Errorf("unscoped list missing rows from one of the tenants: %s", bodyAll)
	}
}
