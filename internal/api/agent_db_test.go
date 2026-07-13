package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/audit"
	"github.com/acme/wadist/internal/billing"
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

	// A request that OMITS unit_cost entirely must be rejected (400), NOT
	// silently written as a free-tier 0 cost — T5/T6 join this table for money
	// math, so a forgotten field is a money footgun.
	w3 := doJSON(t, s, s.handleAdminSetCostPricing, "POST", "/admin/agent/cost-pricing", "",
		`{"country_code":"US"}`)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("omitted unit_cost: expected 400, got %d body=%s", w3.Code, w3.Body.String())
	}
	// And nothing must have been written for that country.
	var n int
	if err := s.systemPool().QueryRow(context.Background(),
		`SELECT count(*) FROM agent_cost_pricing WHERE country_code='US'`).Scan(&n); err != nil {
		t.Fatalf("count US rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("omitted unit_cost must not write a row, found %d", n)
	}
}

func TestAdminSetCostPricing_ExplicitZeroAccepted(t *testing.T) {
	s, _ := newCrudServer(t)

	// An explicit unit_cost:0 is a legitimate free-tier price and must be
	// accepted (200) and readable back as 0 — distinct from a missing field.
	w := doJSON(t, s, s.handleAdminSetCostPricing, "POST", "/admin/agent/cost-pricing", "",
		`{"country_code":"ZZ","unit_cost":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("explicit zero: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var cost int64
	cost = -1
	if err := s.systemPool().QueryRow(context.Background(),
		`SELECT unit_cost FROM agent_cost_pricing WHERE country_code='ZZ'`).Scan(&cost); err != nil {
		t.Fatalf("read ZZ cost: %v", err)
	}
	if cost != 0 {
		t.Fatalf("explicit zero must persist as 0, got %d", cost)
	}
}

// newAgentServer wires a Server for T5's credit-allocation handlers: sysPool
// (BYPASSRLS) + Deps.Billing (billing.Topup, called AFTER the allocation tx
// commits — see agent_api.go's handleAgentAllocate) + Deps.Audit. No
// store.Manager is needed since agent handlers use sysPool directly.
func newAgentServer(t *testing.T) *Server {
	t.Helper()
	pool := testPool(t)
	return &Server{sysPool: pool, deps: Deps{Billing: billing.NewRepo(pool), Audit: audit.NewAuditWriter(pool)}}
}

// setCreditLimit sets an agent's console_users.credit_limit directly.
func setCreditLimit(t *testing.T, ctx context.Context, s *Server, agentID, limit int64) {
	t.Helper()
	if _, err := s.systemPool().Exec(ctx,
		`UPDATE console_users SET credit_limit=$2 WHERE id=$1`, agentID, limit); err != nil {
		t.Fatalf("set credit limit for agent %d: %v", agentID, err)
	}
}

// assertWalletBalance asserts tenant_wallets.balance for tenantID == want. A
// tenant with no wallet row yet (no topup ever applied) reads as 0.
func assertWalletBalance(t *testing.T, ctx context.Context, s *Server, tenantID, want int64) {
	t.Helper()
	var got int64
	if err := s.systemPool().QueryRow(ctx,
		`SELECT COALESCE((SELECT balance FROM tenant_wallets WHERE tenant_id=$1),0)`,
		tenantID).Scan(&got); err != nil {
		t.Fatalf("read wallet balance for tenant %d: %v", tenantID, err)
	}
	if got != want {
		t.Fatalf("wallet balance for tenant %d: got %d want %d", tenantID, got, want)
	}
}

// seedSettledCharge inserts a 'settled' billing_charges row for tenantID, so
// tests can exercise the consumption-credits-back term of
// agentAvailableCredit (settled subtree retail consumption frees up credit
// previously tied down by an allocation).
func seedSettledCharge(t *testing.T, ctx context.Context, s *Server, tenantID, amount int64, msgID string) {
	t.Helper()
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO billing_charges (tenant_id, account_jid, message_id, country_code, amount, state)
		 VALUES ($1, 'jid@x', $2, 'US', $3, 'settled')`,
		tenantID, msgID, amount); err != nil {
		t.Fatalf("seed settled charge for tenant %d: %v", tenantID, err)
	}
}

// doAgentAllocate posts a single-item POST /sales/allocate {items:[{tenant_id,
// amount}]} as agentID's session and returns the HTTP status code. A
// single-item request gets the item's own status code back at the top level
// (200/402/403) rather than the batch per-item report — see
// handleAgentAllocate's doc comment.
func doAgentAllocate(t *testing.T, s *Server, agentID, tenantID, amount int64) int {
	t.Helper()
	body := `{"items":[{"tenant_id":` + itoa(tenantID) + `,"amount":` + itoa(amount) + `}]}`
	w := doJSONAgent(t, s, s.handleAgentAllocate, "POST", "/sales/allocate", agentID, "", body)
	return w.Code
}

// TestAgentAllocate_CreditGuardAndTopup: allocating within the agent's
// available credit succeeds (200), credits the tenant's wallet via
// billing.Topup, and writes an agent_allocations row; a second allocation
// that would exceed the now-reduced available credit is rejected (402) with
// NO wallet change and NO new allocation row (proves the locked credit check
// actually blocks the over-limit write, not just the topup).
func TestAgentAllocate_CreditGuardAndTopup(t *testing.T) {
	s := newAgentServer(t)
	ctx := context.Background()
	ag := seedAgent(t, ctx, s, "ag-guard@x", nil)
	setCreditLimit(t, ctx, s, ag, 1000)
	tn := seedTenantUnderAgent(t, ctx, s, ag)

	if code := doAgentAllocate(t, s, ag, tn, 600); code != http.StatusOK {
		t.Fatalf("first allocate: got %d", code)
	}
	assertWalletBalance(t, ctx, s, tn, 600)

	var n int
	var sum int64
	if err := s.systemPool().QueryRow(ctx,
		`SELECT count(*), COALESCE(sum(amount),0) FROM agent_allocations WHERE agent_id=$1 AND tenant_id=$2`,
		ag, tn).Scan(&n, &sum); err != nil {
		t.Fatalf("read agent_allocations: %v", err)
	}
	if n != 1 || sum != 600 {
		t.Fatalf("agent_allocations after first allocate: n=%d sum=%d, want 1/600", n, sum)
	}

	// available = 1000 - 600 = 400; 600 more must be rejected.
	if code := doAgentAllocate(t, s, ag, tn, 600); code != http.StatusPaymentRequired {
		t.Fatalf("over-limit should be 402, got %d", code)
	}
	assertWalletBalance(t, ctx, s, tn, 600) // no partial topup

	if err := s.systemPool().QueryRow(ctx,
		`SELECT count(*), COALESCE(sum(amount),0) FROM agent_allocations WHERE agent_id=$1 AND tenant_id=$2`,
		ag, tn).Scan(&n, &sum); err != nil {
		t.Fatalf("read agent_allocations after rejection: %v", err)
	}
	if n != 1 || sum != 600 {
		t.Fatalf("rejected allocate must not write a row: n=%d sum=%d, want 1/600", n, sum)
	}
}

// TestAgentAllocate_OutsideSubtree_Forbidden: an agent has enough credit, but
// the target tenant belongs to an unrelated agent's subtree — the allocation
// must be rejected (403), not silently applied.
func TestAgentAllocate_OutsideSubtree_Forbidden(t *testing.T) {
	s := newAgentServer(t)
	ctx := context.Background()
	a := seedAgent(t, ctx, s, "ag-subtree-a@x", nil)
	c := seedAgent(t, ctx, s, "ag-subtree-c@x", nil)
	setCreditLimit(t, ctx, s, a, 1000)
	tOutside := seedTenantUnderAgent(t, ctx, s, c)

	if code := doAgentAllocate(t, s, a, tOutside, 100); code != http.StatusForbidden {
		t.Fatalf("out-of-subtree allocate: got %d, want 403", code)
	}
	assertWalletBalance(t, ctx, s, tOutside, 0)

	var n int
	if err := s.systemPool().QueryRow(ctx,
		`SELECT count(*) FROM agent_allocations WHERE agent_id=$1`, a).Scan(&n); err != nil {
		t.Fatalf("read agent_allocations: %v", err)
	}
	if n != 0 {
		t.Fatalf("out-of-subtree rejection must not write a row, got %d", n)
	}

	// Precedence: authorization (403) must win over credit limit (402). An
	// out-of-subtree tenant with an amount that ALSO exceeds available credit
	// must be reported as forbidden (403), NOT payment-required (402) — the
	// subtree check runs before the credit check in allocateOne.
	setCreditLimit(t, ctx, s, a, 50)
	if code := doAgentAllocate(t, s, a, tOutside, 100); code != http.StatusForbidden {
		t.Fatalf("out-of-subtree AND over-limit: got %d, want 403 (403 must win over 402)", code)
	}
	assertWalletBalance(t, ctx, s, tOutside, 0)
	if err := s.systemPool().QueryRow(ctx,
		`SELECT count(*) FROM agent_allocations WHERE agent_id=$1`, a).Scan(&n); err != nil {
		t.Fatalf("read agent_allocations after over-limit+out-of-subtree: %v", err)
	}
	if n != 0 {
		t.Fatalf("out-of-subtree+over-limit rejection must not write a row, got %d", n)
	}
}

// TestAgentAllocate_SettledConsumptionCreditsBackAvailable proves the
// consumption-credit-back term in agentAvailableCredit: once subtree retail
// consumption settles, the freed-up amount is added back to available
// credit, so an allocation that was previously blocked by 402 later
// succeeds without any change to credit_limit itself.
func TestAgentAllocate_SettledConsumptionCreditsBackAvailable(t *testing.T) {
	s := newAgentServer(t)
	ctx := context.Background()
	ag := seedAgent(t, ctx, s, "ag-consume@x", nil)
	setCreditLimit(t, ctx, s, ag, 1000)
	tn := seedTenantUnderAgent(t, ctx, s, ag)

	if code := doAgentAllocate(t, s, ag, tn, 700); code != http.StatusOK {
		t.Fatalf("initial allocate: got %d", code)
	}
	// available = 1000 - 700 = 300; 800 must be blocked.
	if code := doAgentAllocate(t, s, ag, tn, 800); code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 before consumption credit-back, got %d", code)
	}

	// Settle 600 of subtree retail consumption -> available = 300 + 600 = 900.
	seedSettledCharge(t, ctx, s, tn, 600, "msg-consume-1")

	if code := doAgentAllocate(t, s, ag, tn, 800); code != http.StatusOK {
		t.Fatalf("expected 200 after consumption credit-back, got %d", code)
	}
	assertWalletBalance(t, ctx, s, tn, 700+800)
}

// --- T6: consumption rollup / monthly settlement ---------------------------

// seedSettledChargesCountry inserts `count` 'settled' billing_charges rows
// for tenantID in `country`, each with `amount` and an explicit createdAt (so
// tests can place charges deterministically inside a given month, avoiding
// wall-clock month-boundary flakiness) — distinct from the pre-existing
// seedSettledCharge (single-row, country fixed to 'US', created_at=now())
// used by T5's credit-back tests.
func seedSettledChargesCountry(t *testing.T, ctx context.Context, s *Server, tenantID int64, country string, amount int64, count int, createdAt time.Time) {
	t.Helper()
	nonce := time.Now().UnixNano() // keeps message_id unique across repeated calls for the same tenant/country/amount (e.g. re-seeding in an idempotency test)
	for i := 0; i < count; i++ {
		msgID := fmt.Sprintf("msg-%d-%s-%d-%d-%d", tenantID, country, amount, nonce, i)
		if _, err := s.systemPool().Exec(ctx,
			`INSERT INTO billing_charges (tenant_id, account_jid, message_id, country_code, amount, state, created_at)
			 VALUES ($1, 'jid@x', $2, $3, $4, 'settled', $5)`,
			tenantID, msgID, country, amount, createdAt); err != nil {
			t.Fatalf("seed settled charge %d for tenant %d/%s: %v", i, tenantID, country, err)
		}
	}
}

// setCommissionRate sets an agent's console_users.commission_rate directly.
func setCommissionRate(t *testing.T, ctx context.Context, s *Server, agentID int64, rate float64) {
	t.Helper()
	if _, err := s.systemPool().Exec(ctx,
		`UPDATE console_users SET commission_rate=$2 WHERE id=$1`, agentID, rate); err != nil {
		t.Fatalf("set commission rate for agent %d: %v", agentID, err)
	}
}

// setCostPrice upserts agent_cost_pricing for a country (mirrors
// handleAdminSetCostPricing's SQL, called directly to avoid HTTP plumbing in
// settlement-math tests).
func setCostPrice(t *testing.T, ctx context.Context, s *Server, country string, unitCost int64) {
	t.Helper()
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO agent_cost_pricing (country_code, unit_cost) VALUES ($1,$2)
		 ON CONFLICT (country_code) DO UPDATE SET unit_cost=$2, updated_at=now()`,
		country, unitCost); err != nil {
		t.Fatalf("set cost price for %s: %v", country, err)
	}
}

// TestAgentSettlement_MultiLevel is the money-critical trace from the task-6
// brief: agent A -> sub-agent B; B's own direct customer tB has 10 settled
// CN charges this month at retail amount=80 each (CN unit_cost=50).
//
//	tB retail=800, cost=500.
//	B (no sub-agents, commission_rate left NULL -> 0):
//	  RetailDirect=800, CostDirect=500, RetailSubtree=800 (B's own direct is
//	  its whole subtree), RebateRate=0 -> margin=300, debt=500, rebate=0,
//	  net=300+0-500=-200.
//	A (no direct customers of its own, commission_rate=0.10):
//	  RetailDirect=0, CostDirect=0, RetailSubtree=800 (tB is under B is under
//	  A), RebateRate=0.10 -> margin=0, debt=0, rebate=round(800*0.10)=80,
//	  net=0+80-0=80.
//
// This also confirms the intentional double-benefit: an agent that is both a
// direct seller and an upline (not exercised by A/B here, but by the shape of
// the subtree query) earns margin on its own direct customers AND rebate on
// its whole subtree including those same customers.
func TestAgentSettlement_MultiLevel(t *testing.T) {
	s := newAgentServer(t)
	ctx := context.Background()
	a := seedAgent(t, ctx, s, "settle-a@x", nil)
	b := seedAgent(t, ctx, s, "settle-b@x", &a)
	tB := seedTenantUnderAgent(t, ctx, s, b)

	from, to, err := parseMonth("")
	if err != nil {
		t.Fatalf("parseMonth: %v", err)
	}
	inMonth := from.Add(time.Hour)

	setCostPrice(t, ctx, s, "CN", 50)
	seedSettledChargesCountry(t, ctx, s, tB, "CN", 80, 10, inMonth)
	setCommissionRate(t, ctx, s, a, 0.10)
	// B's commission_rate is left NULL -> must read back as RebateRate=0.

	inB, err := s.agentSettlement(ctx, b, from, to)
	if err != nil {
		t.Fatalf("agentSettlement(B): %v", err)
	}
	if inB.RetailDirect != 800 || inB.CostDirect != 500 || inB.RetailSubtree != 800 || inB.RebateRate != 0 {
		t.Fatalf("B settlementInput: %+v, want RetailDirect=800 CostDirect=500 RetailSubtree=800 RebateRate=0", inB)
	}
	debtB, marginB, rebateB, netB := computeSettlement(inB)
	if debtB != 500 || marginB != 300 || rebateB != 0 || netB != -200 {
		t.Fatalf("B computeSettlement: debt=%d margin=%d rebate=%d net=%d, want 500/300/0/-200", debtB, marginB, rebateB, netB)
	}

	inA, err := s.agentSettlement(ctx, a, from, to)
	if err != nil {
		t.Fatalf("agentSettlement(A): %v", err)
	}
	if inA.RetailDirect != 0 || inA.CostDirect != 0 || inA.RetailSubtree != 800 || inA.RebateRate != 0.10 {
		t.Fatalf("A settlementInput: %+v, want RetailDirect=0 CostDirect=0 RetailSubtree=800 RebateRate=0.10", inA)
	}
	debtA, marginA, rebateA, netA := computeSettlement(inA)
	if debtA != 0 || marginA != 0 || rebateA != 80 || netA != 80 {
		t.Fatalf("A computeSettlement: debt=%d margin=%d rebate=%d net=%d, want 0/0/80/80", debtA, marginA, rebateA, netA)
	}
}

// TestHandleAgentStatement_OwnOnly proves the agent-facing endpoint returns
// the calling agent's own computed settlement (no way to request another
// agent's — there is no :id on this route, the session's own UserID is the
// only agent id ever used).
func TestHandleAgentStatement_OwnOnly(t *testing.T) {
	s := newAgentServer(t)
	ctx := context.Background()
	a := seedAgent(t, ctx, s, "stmt-a@x", nil)
	tA := seedTenantUnderAgent(t, ctx, s, a)

	from, _, err := parseMonth("")
	if err != nil {
		t.Fatalf("parseMonth: %v", err)
	}
	inMonth := from.Add(time.Hour)
	setCostPrice(t, ctx, s, "US", 10)
	seedSettledChargesCountry(t, ctx, s, tA, "US", 20, 5, inMonth) // retail=100, cost=50

	w := doJSONAgent(t, s, s.handleAgentStatement, "GET", "/sales/statement", a, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("statement: got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"debt":50`) || !strings.Contains(body, `"margin":50`) {
		t.Fatalf("expected debt=50 margin=50 in statement body, got %s", body)
	}
}

// TestHandleAdminSettlementOverview_ListsAgent proves the admin "god view"
// endpoint includes a seeded agent's computed settlement.
func TestHandleAdminSettlementOverview_ListsAgent(t *testing.T) {
	s := newAgentServer(t)
	ctx := context.Background()
	a := seedAgent(t, ctx, s, "ov-a@x", nil)
	tA := seedTenantUnderAgent(t, ctx, s, a)

	from, _, err := parseMonth("")
	if err != nil {
		t.Fatalf("parseMonth: %v", err)
	}
	inMonth := from.Add(time.Hour)
	setCostPrice(t, ctx, s, "US", 10)
	seedSettledChargesCountry(t, ctx, s, tA, "US", 20, 3, inMonth) // retail=60, cost=30

	w := doJSON(t, s, s.handleAdminSettlementOverview, "GET", "/admin/agent/settlements", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("overview: got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"agent_id":`+itoa(a)) {
		t.Fatalf("expected agent %d in overview, got %s", a, body)
	}
	if !strings.Contains(body, `"debt":30`) || !strings.Contains(body, `"margin":30`) {
		t.Fatalf("expected debt=30 margin=30 in overview, got %s", body)
	}
}

// TestHandleAdminCloseSettlement_Idempotent proves closing a month writes one
// agent_settlements row per agent, and re-closing the SAME month (even after
// the underlying consumption has changed) is a no-op: still exactly one row,
// with the ORIGINAL figures unchanged (ON CONFLICT DO NOTHING, not DO UPDATE
// — a closed month's numbers are final and must not silently drift).
func TestHandleAdminCloseSettlement_Idempotent(t *testing.T) {
	s := newAgentServer(t)
	ctx := context.Background()
	a := seedAgent(t, ctx, s, "close-a@x", nil)
	tA := seedTenantUnderAgent(t, ctx, s, a)

	from, _, err := parseMonth("")
	if err != nil {
		t.Fatalf("parseMonth: %v", err)
	}
	inMonth := from.Add(time.Hour)
	setCostPrice(t, ctx, s, "US", 10)
	seedSettledChargesCountry(t, ctx, s, tA, "US", 20, 3, inMonth) // retail=60, cost=30
	setCommissionRate(t, ctx, s, a, 0.05)                          // rebate = round(60*0.05) = 3

	w := doJSON(t, s, s.handleAdminCloseSettlement, "POST", "/admin/agent/settlements/close", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("close: got %d body=%s", w.Code, w.Body.String())
	}
	// First close created the row: response reports the fresh figures + newly_closed:true.
	firstBody := w.Body.String()
	if !strings.Contains(firstBody, `"debt":30`) || !strings.Contains(firstBody, `"margin":30`) ||
		!strings.Contains(firstBody, `"rebate":3`) || !strings.Contains(firstBody, `"newly_closed":true`) {
		t.Fatalf("first close response: want debt=30 margin=30 rebate=3 newly_closed=true, got %s", firstBody)
	}

	period := from.Format("2006-01")
	assertOneSettlement := func(wantDebt, wantMargin, wantRebate, wantNet int64) {
		t.Helper()
		var n int
		if err := s.systemPool().QueryRow(ctx,
			`SELECT count(*) FROM agent_settlements WHERE agent_id=$1 AND period=$2`, a, period).Scan(&n); err != nil {
			t.Fatalf("count settlements: %v", err)
		}
		if n != 1 {
			t.Fatalf("expected exactly 1 settlement row for agent %d period %s, got %d", a, period, n)
		}
		var debt, margin, rebate, net int64
		if err := s.systemPool().QueryRow(ctx,
			`SELECT debt, margin, rebate, net FROM agent_settlements WHERE agent_id=$1 AND period=$2`,
			a, period).Scan(&debt, &margin, &rebate, &net); err != nil {
			t.Fatalf("read settlement: %v", err)
		}
		if debt != wantDebt || margin != wantMargin || rebate != wantRebate || net != wantNet {
			t.Fatalf("settlement row: debt=%d margin=%d rebate=%d net=%d, want %d/%d/%d/%d",
				debt, margin, rebate, net, wantDebt, wantMargin, wantRebate, wantNet)
		}
	}
	assertOneSettlement(30, 30, 3, 3) // debt=30, margin=30, rebate=3, net=30+3-30=3

	// Mutate underlying consumption AFTER the first close, then re-close: the
	// already-closed row must NOT change, and no second row must appear.
	seedSettledChargesCountry(t, ctx, s, tA, "US", 20, 5, inMonth)

	w2 := doJSON(t, s, s.handleAdminCloseSettlement, "POST", "/admin/agent/settlements/close", "", "")
	if w2.Code != http.StatusOK {
		t.Fatalf("re-close: got %d body=%s", w2.Code, w2.Body.String())
	}
	assertOneSettlement(30, 30, 3, 3) // (a) persisted row unchanged despite the new charges

	// (b) the re-close RESPONSE must report the PERSISTED (frozen) figures with
	// newly_closed:false — NOT the new live numbers. After 8 total settled US
	// charges (retail=160, cost=80) the recomputed live figures would be
	// debt=80/margin=80/rebate=8; the response must NOT contain those.
	reBody := w2.Body.String()
	if !strings.Contains(reBody, `"debt":30`) || !strings.Contains(reBody, `"margin":30`) ||
		!strings.Contains(reBody, `"rebate":3`) || !strings.Contains(reBody, `"newly_closed":false`) {
		t.Fatalf("re-close response must report persisted 30/30/3 + newly_closed:false, got %s", reBody)
	}
	if strings.Contains(reBody, `"debt":80`) || strings.Contains(reBody, `"margin":80`) {
		t.Fatalf("re-close response must NOT report the new live figures (80), got %s", reBody)
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
