package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/console"
)

// doJSONAgent drives a handler with an agent session (sessionFrom(c).UserID
// is the agent id — agents are staff, not tenants, so unlike doJSONTenant
// there is no TenantID on the session) and an optional :id route param +
// JSON body. Mirrors contacts_db_test.go's doJSONTenant/doJSONTenantID.
func doJSONAgent(t *testing.T, s *Server, h gin.HandlerFunc, method, target string, agentID int64, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(sessionCtxKey, &console.SessionData{UserID: agentID})
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

// seedAgent inserts a role='sales' console_user with an optional parent_id
// and returns its id. Mirrors the other seed* helpers in this package.
func seedAgent(t *testing.T, ctx context.Context, s *Server, email string, parent *int64) int64 {
	t.Helper()
	var id int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO console_users (email, password_hash, role, tenant_id, parent_id)
		 VALUES ($1, 'x', 'sales', NULL, $2) RETURNING id`,
		email, parent).Scan(&id); err != nil {
		t.Fatalf("seed agent %s: %v", email, err)
	}
	return id
}

// seedTenantUnderAgent inserts a tenant with sales_owner_id=agentID and
// returns its id.
func seedTenantUnderAgent(t *testing.T, ctx context.Context, s *Server, agentID int64) int64 {
	t.Helper()
	var id int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO tenants (name, sales_owner_id) VALUES ($1, $2) RETURNING id`,
		"tenant-under-"+itoa(agentID), agentID).Scan(&id); err != nil {
		t.Fatalf("seed tenant under agent %d: %v", agentID, err)
	}
	return id
}

func TestTenantInSubtree_Isolation(t *testing.T) {
	s := &Server{sysPool: testPool(t)}
	ctx := context.Background()
	// tree: A(root) -> B(sub) ; C is a separate root. tenant tB under B, tC under C.
	a := seedAgent(t, ctx, s, "a@x", nil)
	b := seedAgent(t, ctx, s, "b@x", &a)
	c := seedAgent(t, ctx, s, "c@x", nil)
	tB := seedTenantUnderAgent(t, ctx, s, b)
	tC := seedTenantUnderAgent(t, ctx, s, c)

	if ok, _ := s.tenantInSubtree(ctx, a, tB); !ok {
		t.Fatal("A must see tenant under its sub-agent B")
	}
	if ok, _ := s.tenantInSubtree(ctx, a, tC); ok {
		t.Fatal("A must NOT see tenant under unrelated agent C")
	}
}

func TestAgentInSubtree(t *testing.T) {
	s := &Server{sysPool: testPool(t)}
	ctx := context.Background()
	a := seedAgent(t, ctx, s, "ra@x", nil)
	b := seedAgent(t, ctx, s, "rb@x", &a)
	c := seedAgent(t, ctx, s, "rc@x", nil)

	if in, err := s.agentInSubtree(ctx, a, b); err != nil || !in {
		t.Fatalf("B must be in A's subtree: in=%v err=%v", in, err)
	}
	if in, err := s.agentInSubtree(ctx, a, c); err != nil || in {
		t.Fatalf("C must NOT be in A's subtree: in=%v err=%v", in, err)
	}
	if in, err := s.agentInSubtree(ctx, a, a); err != nil || !in {
		t.Fatalf("A must be in its own subtree (root): in=%v err=%v", in, err)
	}
}

func TestHandleAgentCreateSubAgent(t *testing.T) {
	pool := testPool(t)
	s := &Server{sysPool: pool}
	ctx := context.Background()
	a := seedAgent(t, ctx, s, "parent@x", nil)

	w := doJSONAgent(t, s, s.handleAgentCreateSubAgent, "POST", "/sales/sub-agents", a, "",
		`{"email":"child@x","password":"password123"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create sub-agent: got %d body=%s", w.Code, w.Body.String())
	}
	var role string
	var parentID int64
	var tenantID *int64
	if err := pool.QueryRow(ctx,
		`SELECT role, parent_id, tenant_id FROM console_users WHERE email='child@x'`).Scan(&role, &parentID, &tenantID); err != nil {
		t.Fatalf("read created sub-agent: %v", err)
	}
	if role != "sales" || parentID != a || tenantID != nil {
		t.Fatalf("got role=%s parent=%d tenant=%v", role, parentID, tenantID)
	}
}

func TestHandleAdminSetAgentParent_CyclePrevented(t *testing.T) {
	pool := testPool(t)
	s := &Server{sysPool: pool}
	ctx := context.Background()
	a := seedAgent(t, ctx, s, "a2@x", nil)
	b := seedAgent(t, ctx, s, "b2@x", &a)

	// B is already under A; setting A's parent to B would create a cycle.
	w := doJSONAgent(t, s, s.handleAdminSetAgentParent, "POST", "/admin/agents/x/parent", 0, itoa(a),
		`{"parent_id":`+itoa(b)+`}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 cycle rejection, got %d body=%s", w.Code, w.Body.String())
	}

	// Detaching B (parent_id: null) is a legitimate, non-cyclic edit.
	w2 := doJSONAgent(t, s, s.handleAdminSetAgentParent, "POST", "/admin/agents/x/parent", 0, itoa(b),
		`{"parent_id":null}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 detach, got %d body=%s", w2.Code, w2.Body.String())
	}
	var parentID *int64
	if err := pool.QueryRow(ctx, `SELECT parent_id FROM console_users WHERE id=$1`, b).Scan(&parentID); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if parentID != nil {
		t.Fatalf("expected detached parent, got %v", *parentID)
	}
}

func TestHandleAdminSetAgentParent_ParentMustBeSalesAgent(t *testing.T) {
	pool := testPool(t)
	s := &Server{sysPool: pool}
	ctx := context.Background()
	agent := seedAgent(t, ctx, s, "child3@x", nil)
	// An admin console_user (NOT a sales agent) — valid FK target, wrong role.
	var adminID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO console_users (email, password_hash, role, tenant_id)
		 VALUES ('admin3@x','x','admin',NULL) RETURNING id`).Scan(&adminID); err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	// (a) non-existent parent id → clean 400, not a 500 from the FK.
	w := doJSONAgent(t, s, s.handleAdminSetAgentParent, "POST", "/admin/agents/x/parent", 0, itoa(agent),
		`{"parent_id":999999}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("nonexistent parent: expected 400, got %d body=%s", w.Code, w.Body.String())
	}

	// (b) parent id exists but is role='admin' → 400, not silently written.
	w2 := doJSONAgent(t, s, s.handleAdminSetAgentParent, "POST", "/admin/agents/x/parent", 0, itoa(agent),
		`{"parent_id":`+itoa(adminID)+`}`)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("admin parent: expected 400, got %d body=%s", w2.Code, w2.Body.String())
	}
	var parentID *int64
	if err := pool.QueryRow(ctx, `SELECT parent_id FROM console_users WHERE id=$1`, agent).Scan(&parentID); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if parentID != nil {
		t.Fatalf("parent must not have been written, got %v", *parentID)
	}

	// (c) target itself is not a sales agent → 400.
	w3 := doJSONAgent(t, s, s.handleAdminSetAgentParent, "POST", "/admin/agents/x/parent", 0, itoa(adminID),
		`{"parent_id":`+itoa(agent)+`}`)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("non-sales target: expected 400, got %d body=%s", w3.Code, w3.Body.String())
	}
}

func TestHandleAdminSetAgentTerms_TargetMustBeSalesAgent(t *testing.T) {
	pool := testPool(t)
	s := &Server{sysPool: pool}
	ctx := context.Background()
	var adminID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO console_users (email, password_hash, role, tenant_id)
		 VALUES ('admin4@x','x','admin',NULL) RETURNING id`).Scan(&adminID); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	w := doJSONAgent(t, s, s.handleAdminSetAgentTerms, "POST", "/admin/agents/x/terms", 0, itoa(adminID),
		`{"commission_rate":0.1,"credit_limit":100}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-sales target: expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleAdminSetAgentTerms(t *testing.T) {
	pool := testPool(t)
	s := &Server{sysPool: pool}
	ctx := context.Background()
	a := seedAgent(t, ctx, s, "terms@x", nil)

	w := doJSONAgent(t, s, s.handleAdminSetAgentTerms, "POST", "/admin/agents/x/terms", 0, itoa(a),
		`{"commission_rate":0.15,"credit_limit":50000}`)
	if w.Code != http.StatusOK {
		t.Fatalf("set terms: got %d body=%s", w.Code, w.Body.String())
	}
	var rate float64
	var limit int64
	if err := pool.QueryRow(ctx, `SELECT commission_rate, credit_limit FROM console_users WHERE id=$1`, a).Scan(&rate, &limit); err != nil {
		t.Fatalf("read terms: %v", err)
	}
	if rate != 0.15 || limit != 50000 {
		t.Fatalf("got rate=%v limit=%v", rate, limit)
	}
}

func TestAdminCostPricing_SetAndList(t *testing.T) {
	s, _ := newCrudServer(t)

	w := doJSON(t, s, s.handleAdminSetCostPricing, "POST", "/admin/agent/cost-pricing", "",
		`{"country_code":"CN","unit_cost":50}`)
	if w.Code != http.StatusOK {
		t.Fatalf("set cost pricing: got %d body=%s", w.Code, w.Body.String())
	}

	w2 := doJSON(t, s, s.handleAdminListCostPricing, "GET", "/admin/agent/cost-pricing", "", "")
	if w2.Code != http.StatusOK {
		t.Fatalf("list cost pricing: got %d body=%s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"CN"`) || !strings.Contains(w2.Body.String(), `50`) {
		t.Fatalf("expected CN=50 in list, got %s", w2.Body.String())
	}

	// Upsert: setting CN again with a different cost must UPDATE the existing
	// row, not insert a duplicate.
	w3 := doJSON(t, s, s.handleAdminSetCostPricing, "POST", "/admin/agent/cost-pricing", "",
		`{"country_code":"CN","unit_cost":70}`)
	if w3.Code != http.StatusOK {
		t.Fatalf("update cost pricing: got %d body=%s", w3.Code, w3.Body.String())
	}

	w4 := doJSON(t, s, s.handleAdminListCostPricing, "GET", "/admin/agent/cost-pricing", "", "")
	if w4.Code != http.StatusOK {
		t.Fatalf("relist cost pricing: got %d body=%s", w4.Code, w4.Body.String())
	}
	body := w4.Body.String()
	if !strings.Contains(body, `"CN"`) || !strings.Contains(body, `70`) {
		t.Fatalf("expected CN=70 after upsert, got %s", body)
	}
	if strings.Count(body, `"CN"`) != 1 {
		t.Fatalf("expected exactly one CN row after upsert, got %s", body)
	}
}

func TestAdminSetCostPricing_Validation(t *testing.T) {
	s, _ := newCrudServer(t)

	w := doJSON(t, s, s.handleAdminSetCostPricing, "POST", "/admin/agent/cost-pricing", "",
		`{"country_code":"USA","unit_cost":10}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad country code: expected 400, got %d body=%s", w.Code, w.Body.String())
	}

	w2 := doJSON(t, s, s.handleAdminSetCostPricing, "POST", "/admin/agent/cost-pricing", "",
		`{"country_code":"US","unit_cost":-5}`)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("negative unit cost: expected 400, got %d body=%s", w2.Code, w2.Body.String())
	}
}

func TestAgentSchemaApplies(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var tabs, cols int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_name IN ('agent_cost_pricing','agent_allocations','agent_settlements')`).Scan(&tabs); err != nil {
		t.Fatal(err)
	}
	if tabs != 3 {
		t.Fatalf("want 3 agent tables, got %d", tabs)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_name='console_users' AND column_name IN ('parent_id','credit_limit')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 2 {
		t.Fatalf("want parent_id+credit_limit, got %d", cols)
	}
}
