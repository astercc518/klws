// internal/api/instances_db_test.go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/audit"
	wlog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/store"
)

// newAdminInstanceServer builds a Server whose deps.Mgr is a real
// store.Manager backed by Postgres + Redis. handleAdminListInstances calls
// Mgr.ListInstances (a store.Manager method per the P2 task-2 interface), so
// the lighter &Server{sysPool: ...} pattern (nil Mgr) won't work here.
func newAdminInstanceServer(t *testing.T) (*Server, *store.Manager) {
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
	mgr, err := store.NewManager(ctx, store.Config{DSN: dsn, Redis: rdb, NodeID: "admin-instance-test-node", BadgerDir: t.TempDir()}, wlog.Noop)
	if err != nil {
		t.Fatalf("store.NewManager: %v", err)
	}
	t.Cleanup(mgr.Close)

	return &Server{sysPool: pool, deps: Deps{Mgr: mgr}}, mgr
}

// seedInstance inserts one account_instances row via the same UpsertInstance
// path production code uses (jid == "" means unpaired / NULL jid).
func seedInstance(t *testing.T, ctx context.Context, mgr *store.Manager, name, jid string, tenantID int64, node, state string) {
	t.Helper()
	if err := mgr.UpsertInstance(ctx, store.InstanceRow{
		InstanceName: name,
		JID:          jid,
		TenantID:     tenantID,
		EvoNode:      node,
		State:        state,
	}); err != nil {
		t.Fatalf("seed instance %s: %v", name, err)
	}
}

func TestListInstances_pagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	s, mgr := newAdminInstanceServer(t)

	tidA, _ := seedTenantUser(t, ctx, s, "instances-admin-a@acme.test")
	tidB, _ := seedTenantUser(t, ctx, s, "instances-admin-b@acme.test")

	// Tenant A: 3 instances, one unpaired (jid NULL).
	seedInstance(t, ctx, mgr, "inst-a1", "110@wa", tidA, "node-a", "connected")
	seedInstance(t, ctx, mgr, "inst-a2", "", tidA, "node-a", "qr")
	seedInstance(t, ctx, mgr, "inst-a3", "130@wa", tidA, "node-b", "connected")

	// Tenant B: 3 instances, one unpaired (jid NULL).
	seedInstance(t, ctx, mgr, "inst-b1", "210@wa", tidB, "node-a", "disconnected")
	seedInstance(t, ctx, mgr, "inst-b2", "", tidB, "node-b", "created")
	seedInstance(t, ctx, mgr, "inst-b3", "230@wa", tidB, "node-b", "connected")

	call := func(q string) map[string]any {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/instances"+q, nil)
		s.handleAdminListInstances(c)
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

	// no filter: total 6; stateStats is the full unfiltered breakdown.
	all := call("")
	if all["total"].(float64) != 6 {
		t.Errorf("total = %v want 6", all["total"])
	}
	stats := all["stats"].(map[string]any)
	if stats["connected"].(float64) != 3 || stats["qr"].(float64) != 1 ||
		stats["disconnected"].(float64) != 1 || stats["created"].(float64) != 1 {
		t.Errorf("stats = %v", stats)
	}
	rows := all["rows"].([]any)
	if len(rows) != 6 {
		t.Fatalf("rows len = %d want 6", len(rows))
	}
	// Spot-check the joined tenant_name and nullable jid/proxy_id shape.
	found := false
	for _, rv := range rows {
		r := rv.(map[string]any)
		if r["instance_name"] == "inst-a2" {
			found = true
			if r["jid"] != nil {
				t.Errorf("inst-a2 jid = %v want nil", r["jid"])
			}
			if r["proxy_id"] != nil {
				t.Errorf("inst-a2 proxy_id = %v want nil", r["proxy_id"])
			}
			if r["tenant_name"] != "Acme" {
				t.Errorf("inst-a2 tenant_name = %v want Acme", r["tenant_name"])
			}
		}
	}
	if !found {
		t.Fatalf("inst-a2 not found in rows")
	}

	// state filter
	if call("?state=connected")["total"].(float64) != 3 {
		t.Errorf("state filter wrong")
	}

	// node filter
	if call("?node=node-b")["total"].(float64) != 3 {
		t.Errorf("node filter wrong")
	}

	// tenant_id filter
	if call("?tenant_id=" + strconv.FormatInt(tidA, 10))["total"].(float64) != 3 {
		t.Errorf("tenant_id filter wrong")
	}

	// q filter on instance_name
	if call("?q=inst-a1")["total"].(float64) != 1 {
		t.Errorf("q instance_name filter wrong")
	}
	// q filter on jid substring
	if call("?q=210")["total"].(float64) != 1 {
		t.Errorf("q jid filter wrong")
	}

	// pagination: limit 2 → 2 rows, total still 6
	pg := call("?limit=2")
	if len(pg["rows"].([]any)) != 2 || pg["total"].(float64) != 6 {
		t.Errorf("limit paging: rows=%d total=%v", len(pg["rows"].([]any)), pg["total"])
	}
	// offset moves the window; still total 6
	pg2 := call("?limit=2&offset=4")
	if len(pg2["rows"].([]any)) != 2 || pg2["total"].(float64) != 6 {
		t.Errorf("offset paging: rows=%d total=%v", len(pg2["rows"].([]any)), pg2["total"])
	}

	// stats are UNFILTERED even under a filter
	filteredStats := call("?state=connected")["stats"].(map[string]any)
	if filteredStats["qr"].(float64) != 1 || filteredStats["created"].(float64) != 1 {
		t.Errorf("stats must be unfiltered, got %v", filteredStats)
	}
}

// ---------------------------------------------------------------------------
// T4: qr / state / reconnect / logout / delete lifecycle endpoints
// ---------------------------------------------------------------------------

// newInstanceLifecycleServer builds a Server wired for the T4 lifecycle
// endpoints: real store.Manager (Postgres + Redis, same as
// newAdminInstanceServer) plus an Audit writer (reconnect/logout/delete all
// write audit_log rows) and a single-node "node-a" topology resolving to
// fake — mirrors newCreateInstanceServer's wiring but skips Tenants, which
// these endpoints (acting on an already-seeded account_instances row, not
// the zero-jid onboarding flow) never touch.
func newInstanceLifecycleServer(t *testing.T, fake *fakeInstanceEvo, seedProxies func(pool *pgxpool.Pool)) (*Server, *store.Manager, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	dsn := testDSN(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyAllMigrations(t, ctx, pool)

	if seedProxies != nil {
		seedProxies(pool)
	}

	rdb := newTestRedis(t)
	mgr, err := store.NewManager(ctx, store.Config{DSN: dsn, Redis: rdb, NodeID: "instance-lifecycle-test-node", BadgerDir: t.TempDir()}, wlog.Noop)
	if err != nil {
		t.Fatalf("store.NewManager: %v", err)
	}
	t.Cleanup(mgr.Close)

	s := &Server{
		sysPool: pool,
		deps: Deps{
			Mgr:     mgr,
			Audit:   audit.NewAuditWriter(pool),
			QRCache: newQRCache(),
		},
		evoNodesFn: func() []string { return []string{"node-a"} },
		evoForFn: func(node string) (instanceEvoAPI, bool) {
			if node != "node-a" {
				return nil, false
			}
			return fake, true
		},
	}
	return s, mgr, pool
}

// callInstanceLifecycle invokes handler directly (bypassing the router) with
// :name bound as a gin path param, decoding the standard {code,data,message}
// envelope.
func callInstanceLifecycle(handler func(*gin.Context), method, name string) (int, map[string]any) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/admin/instances/"+name, nil)
	c.Params = gin.Params{{Key: "name", Value: name}}
	handler(c)
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w.Code, env.Data
}

func instanceState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM account_instances WHERE instance_name=$1`, name).Scan(&state); err != nil {
		t.Fatalf("query instance state %s: %v", name, err)
	}
	return state
}

func instanceRowExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM account_instances WHERE instance_name=$1`, name).Scan(&n); err != nil {
		t.Fatalf("count instance %s: %v", name, err)
	}
	return n > 0
}

func auditCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, action, instanceName string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action=$1 AND details->>'instance_name'=$2`,
		action, instanceName).Scan(&n); err != nil {
		t.Fatalf("query audit_log %s/%s: %v", action, instanceName, err)
	}
	return n
}

func proxyCurrentBindings(t *testing.T, ctx context.Context, pool *pgxpool.Pool, proxyID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT current_bindings FROM proxy_pool WHERE id=$1`, proxyID).Scan(&n); err != nil {
		t.Fatalf("query proxy_pool %d: %v", proxyID, err)
	}
	return n
}

// TestInstanceLifecycle_QRAndState_Passthrough: both read-only endpoints
// transparently return whatever the (fake) Evolution client returns, and
// neither writes an audit_log row.
func TestInstanceLifecycle_QRAndState_Passthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	fake := &fakeInstanceEvo{connectBase64: "data:image/png;base64,QRDATA", fetchStateVal: "connecting"}
	s, mgr, pool := newInstanceLifecycleServer(t, fake, nil)
	tid, _ := seedTenantUser(t, ctx, s, "lifecycle-qrstate@acme.test")
	seedInstance(t, ctx, mgr, "inst-qr1", "", tid, "node-a", "qr")

	code, data := callInstanceLifecycle(s.handleAdminInstanceQR, http.MethodGet, "inst-qr1")
	if code != http.StatusOK {
		t.Fatalf("qr status = %d, want 200; data=%v", code, data)
	}
	if data["base64"] != "data:image/png;base64,QRDATA" {
		t.Errorf("qr base64 = %v", data["base64"])
	}

	code, data = callInstanceLifecycle(s.handleAdminInstanceState, http.MethodGet, "inst-qr1")
	if code != http.StatusOK {
		t.Fatalf("state status = %d, want 200; data=%v", code, data)
	}
	if data["state"] != "connecting" {
		t.Errorf("state = %v, want connecting", data["state"])
	}

	fake.mu.Lock()
	if len(fake.connectCalls) != 1 || fake.connectCalls[0] != "inst-qr1" {
		t.Errorf("ConnectInstance calls = %v", fake.connectCalls)
	}
	if len(fake.fetchStateCalls) != 1 || fake.fetchStateCalls[0] != "inst-qr1" {
		t.Errorf("FetchState calls = %v", fake.fetchStateCalls)
	}
	fake.mu.Unlock()

	if n := auditCount(t, ctx, pool, "instance.reconnect", "inst-qr1"); n != 0 {
		t.Errorf("qr must not audit instance.reconnect, got %d", n)
	}
}

// TestInstanceLifecycle_Reconnect_Audits: reconnect re-triggers pairing
// (ConnectInstance) and writes an instance.reconnect audit row.
func TestInstanceLifecycle_Reconnect_Audits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	fake := &fakeInstanceEvo{connectBase64: "QR2"}
	s, mgr, pool := newInstanceLifecycleServer(t, fake, nil)
	tid, _ := seedTenantUser(t, ctx, s, "lifecycle-reconnect@acme.test")
	seedInstance(t, ctx, mgr, "inst-rc1", "", tid, "node-a", "disconnected")

	code, data := callInstanceLifecycle(s.handleAdminInstanceReconnect, http.MethodPost, "inst-rc1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; data=%v", code, data)
	}
	if data["base64"] != "QR2" {
		t.Errorf("base64 = %v, want QR2", data["base64"])
	}

	fake.mu.Lock()
	if len(fake.connectCalls) != 1 || fake.connectCalls[0] != "inst-rc1" {
		t.Errorf("ConnectInstance calls = %v", fake.connectCalls)
	}
	fake.mu.Unlock()

	if n := auditCount(t, ctx, pool, "instance.reconnect", "inst-rc1"); n != 1 {
		t.Errorf("instance.reconnect audit rows = %d, want 1", n)
	}
}

// TestInstanceLifecycle_Logout_UpdatesStateAndAudits: logout calls Evolution
// LogoutInstance, sets state=loggedOut via UpsertInstance, and audits.
func TestInstanceLifecycle_Logout_UpdatesStateAndAudits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	fake := &fakeInstanceEvo{}
	s, mgr, pool := newInstanceLifecycleServer(t, fake, nil)
	tid, _ := seedTenantUser(t, ctx, s, "lifecycle-logout@acme.test")
	seedInstance(t, ctx, mgr, "inst-lo1", "110@wa", tid, "node-a", "connected")

	code, data := callInstanceLifecycle(s.handleAdminInstanceLogout, http.MethodPost, "inst-lo1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; data=%v", code, data)
	}
	if data["state"] != "loggedOut" {
		t.Errorf("response state = %v, want loggedOut", data["state"])
	}

	if got := instanceState(t, ctx, pool, "inst-lo1"); got != "loggedOut" {
		t.Errorf("db state = %q, want loggedOut", got)
	}

	fake.mu.Lock()
	if len(fake.logoutCalls) != 1 || fake.logoutCalls[0] != "inst-lo1" {
		t.Errorf("LogoutInstance calls = %v", fake.logoutCalls)
	}
	fake.mu.Unlock()

	if n := auditCount(t, ctx, pool, "instance.logout", "inst-lo1"); n != 1 {
		t.Errorf("instance.logout audit rows = %d, want 1", n)
	}
}

// TestInstanceLifecycle_Delete_ReleasesProxyRemovesRowAndAudits: happy-path
// delete releases the bound proxy's pool slot, removes the account_instances
// row, and audits — after Evolution DeleteInstance succeeded.
func TestInstanceLifecycle_Delete_ReleasesProxyRemovesRowAndAudits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	fake := &fakeInstanceEvo{}
	var proxyID int64
	s, mgr, pool := newInstanceLifecycleServer(t, fake, func(pool *pgxpool.Pool) {
		if err := pool.QueryRow(ctx,
			`INSERT INTO proxy_pool (proxy_url, proxy_type, country_code, max_bindings, current_bindings)
			 VALUES ('socks5://u:p@h1:1080','socks5','US',1,1) RETURNING id`).Scan(&proxyID); err != nil {
			t.Fatalf("seed proxy: %v", err)
		}
	})
	tid, _ := seedTenantUser(t, ctx, s, "lifecycle-delete@acme.test")
	if err := mgr.UpsertInstance(ctx, store.InstanceRow{
		InstanceName: "inst-del1", TenantID: tid, EvoNode: "node-a", ProxyID: proxyID, State: "connected",
	}); err != nil {
		t.Fatalf("seed instance with proxy: %v", err)
	}

	code, data := callInstanceLifecycle(s.handleAdminInstanceDelete, http.MethodDelete, "inst-del1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; data=%v", code, data)
	}

	if instanceRowExists(t, ctx, pool, "inst-del1") {
		t.Errorf("account_instances row for inst-del1 still exists after delete")
	}

	fake.mu.Lock()
	if len(fake.deleted) != 1 || fake.deleted[0] != "inst-del1" {
		t.Errorf("DeleteInstance calls = %v, want [inst-del1]", fake.deleted)
	}
	fake.mu.Unlock()

	if cur := proxyCurrentBindings(t, ctx, pool, proxyID); cur != 0 {
		t.Errorf("proxy_pool.current_bindings = %d, want 0 (released)", cur)
	}

	if n := auditCount(t, ctx, pool, "instance.delete", "inst-del1"); n != 1 {
		t.Errorf("instance.delete audit rows = %d, want 1", n)
	}
}

// TestInstanceLifecycle_Delete_Evo404_TreatedAsSuccess: Evolution returning
// 404 (instance already gone upstream) must NOT 500 — the delete completes,
// releasing the proxy, removing the row, and auditing, same as the
// Evolution-succeeded happy path.
func TestInstanceLifecycle_Delete_Evo404_TreatedAsSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	fake := &fakeInstanceEvo{failDeleteWith404: true}
	var proxyID int64
	s, mgr, pool := newInstanceLifecycleServer(t, fake, func(pool *pgxpool.Pool) {
		if err := pool.QueryRow(ctx,
			`INSERT INTO proxy_pool (proxy_url, proxy_type, country_code, max_bindings, current_bindings)
			 VALUES ('socks5://u:p@h3:1080','socks5','US',1,1) RETURNING id`).Scan(&proxyID); err != nil {
			t.Fatalf("seed proxy: %v", err)
		}
	})
	tid, _ := seedTenantUser(t, ctx, s, "lifecycle-delete-404@acme.test")
	if err := mgr.UpsertInstance(ctx, store.InstanceRow{
		InstanceName: "inst-del3", TenantID: tid, EvoNode: "node-a", ProxyID: proxyID, State: "connected",
	}); err != nil {
		t.Fatalf("seed instance with proxy: %v", err)
	}

	code, data := callInstanceLifecycle(s.handleAdminInstanceDelete, http.MethodDelete, "inst-del3")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (Evo 404 treated as success); data=%v", code, data)
	}

	if instanceRowExists(t, ctx, pool, "inst-del3") {
		t.Errorf("account_instances row for inst-del3 still exists after delete")
	}
	if cur := proxyCurrentBindings(t, ctx, pool, proxyID); cur != 0 {
		t.Errorf("proxy_pool.current_bindings = %d, want 0 (released)", cur)
	}
	if n := auditCount(t, ctx, pool, "instance.delete", "inst-del3"); n != 1 {
		t.Errorf("instance.delete audit rows = %d, want 1", n)
	}
}

// TestInstanceLifecycle_Delete_EvoFails_NoRowRemovedNoProxyReleased: when
// Evolution's DeleteInstance fails, the endpoint must 500 WITHOUT deleting
// the account_instances row or releasing the proxy — an Evolution-side
// instance the DB no longer references would be orphaned otherwise (the
// exact failure mode the brief's ordering requirement guards against).
func TestInstanceLifecycle_Delete_EvoFails_NoRowRemovedNoProxyReleased(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	fake := &fakeInstanceEvo{failDelete: true}
	var proxyID int64
	s, mgr, pool := newInstanceLifecycleServer(t, fake, func(pool *pgxpool.Pool) {
		if err := pool.QueryRow(ctx,
			`INSERT INTO proxy_pool (proxy_url, proxy_type, country_code, max_bindings, current_bindings)
			 VALUES ('socks5://u:p@h2:1080','socks5','US',1,1) RETURNING id`).Scan(&proxyID); err != nil {
			t.Fatalf("seed proxy: %v", err)
		}
	})
	tid, _ := seedTenantUser(t, ctx, s, "lifecycle-delete-fail@acme.test")
	if err := mgr.UpsertInstance(ctx, store.InstanceRow{
		InstanceName: "inst-del2", TenantID: tid, EvoNode: "node-a", ProxyID: proxyID, State: "connected",
	}); err != nil {
		t.Fatalf("seed instance with proxy: %v", err)
	}

	code, _ := callInstanceLifecycle(s.handleAdminInstanceDelete, http.MethodDelete, "inst-del2")
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}

	if !instanceRowExists(t, ctx, pool, "inst-del2") {
		t.Errorf("account_instances row for inst-del2 was removed despite Evolution failure")
	}
	if cur := proxyCurrentBindings(t, ctx, pool, proxyID); cur != 1 {
		t.Errorf("proxy_pool.current_bindings = %d, want 1 (not released)", cur)
	}
	if n := auditCount(t, ctx, pool, "instance.delete", "inst-del2"); n != 0 {
		t.Errorf("instance.delete audit rows = %d, want 0 (delete never committed)", n)
	}
}

// TestInstanceLifecycle_NotFound: all five endpoints 404 on an unknown
// instance_name — none of them may reach the (fake) Evolution client.
func TestInstanceLifecycle_NotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &fakeInstanceEvo{}
	s, _, _ := newInstanceLifecycleServer(t, fake, nil)

	cases := []struct {
		name    string
		method  string
		handler func(*gin.Context)
	}{
		{"qr", http.MethodGet, s.handleAdminInstanceQR},
		{"state", http.MethodGet, s.handleAdminInstanceState},
		{"reconnect", http.MethodPost, s.handleAdminInstanceReconnect},
		{"logout", http.MethodPost, s.handleAdminInstanceLogout},
		{"delete", http.MethodDelete, s.handleAdminInstanceDelete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := callInstanceLifecycle(tc.handler, tc.method, "no-such-instance")
			if code != http.StatusNotFound {
				t.Errorf("%s status = %d, want 404", tc.name, code)
			}
		})
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.connectCalls)+len(fake.fetchStateCalls)+len(fake.logoutCalls)+len(fake.deleted) != 0 {
		t.Errorf("Evolution must never be reached on 404: connect=%v state=%v logout=%v delete=%v",
			fake.connectCalls, fake.fetchStateCalls, fake.logoutCalls, fake.deleted)
	}
}

// ---------------------------------------------------------------------------
// T5: GET /admin/nodes — per-node capacity view
// ---------------------------------------------------------------------------

// callListNodes invokes handleAdminListNodes directly and decodes the
// {code,data,message} envelope's data as a []any (the endpoint returns a
// JSON array, not an object).
func callListNodes(s *Server) (int, []any) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/nodes", nil)
	s.handleAdminListNodes(c)
	var env struct {
		Data []any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w.Code, env.Data
}

// TestListNodes_AggregatesCountsAndComputesPct seeds instances across three
// nodes, injects a cap via Deps.EvoCapPerNode, and checks the handler
// aggregates per-node counts (via store.NodeCounts), attaches the shared cap,
// computes pct = count/cap*100, and returns rows sorted by node name.
func TestListNodes_AggregatesCountsAndComputesPct(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	s, mgr := newAdminInstanceServer(t)
	s.deps.EvoCapPerNode = 10

	tid, _ := seedTenantUser(t, ctx, s, "nodes-admin@acme.test")
	// node-a: 3 instances, node-b: 1, node-c: 2.
	seedInstance(t, ctx, mgr, "n-a1", "", tid, "node-a", "connected")
	seedInstance(t, ctx, mgr, "n-a2", "", tid, "node-a", "connected")
	seedInstance(t, ctx, mgr, "n-a3", "", tid, "node-a", "qr")
	seedInstance(t, ctx, mgr, "n-b1", "", tid, "node-b", "connected")
	seedInstance(t, ctx, mgr, "n-c1", "", tid, "node-c", "connected")
	seedInstance(t, ctx, mgr, "n-c2", "", tid, "node-c", "created")

	code, rows := callListNodes(s)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(rows) != 3 {
		t.Fatalf("rows len = %d, want 3: %v", len(rows), rows)
	}

	want := []struct {
		node  string
		count float64
		pct   float64
	}{
		{"node-a", 3, 30},
		{"node-b", 1, 10},
		{"node-c", 2, 20},
	}
	for i, w := range want {
		r := rows[i].(map[string]any)
		if r["node"] != w.node {
			t.Errorf("rows[%d].node = %v, want %v (sort order wrong?)", i, r["node"], w.node)
		}
		if r["count"].(float64) != w.count {
			t.Errorf("rows[%d].count = %v, want %v", i, r["count"], w.count)
		}
		if r["cap"].(float64) != 10 {
			t.Errorf("rows[%d].cap = %v, want 10", i, r["cap"])
		}
		if r["pct"].(float64) != w.pct {
			t.Errorf("rows[%d].pct = %v, want %v", i, r["pct"], w.pct)
		}
	}
}

// TestListNodes_ZeroCap_NoPanicPctZero: cap=0 (unconfigured) must not divide
// by zero — pct is 0, not NaN/Inf/a panic.
func TestListNodes_ZeroCap_NoPanicPctZero(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	s, mgr := newAdminInstanceServer(t)
	s.deps.EvoCapPerNode = 0

	tid, _ := seedTenantUser(t, ctx, s, "nodes-admin-zerocap@acme.test")
	seedInstance(t, ctx, mgr, "n-z1", "", tid, "node-z", "connected")

	code, rows := callListNodes(s)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want 1: %v", len(rows), rows)
	}
	r := rows[0].(map[string]any)
	if r["cap"].(float64) != 0 {
		t.Errorf("cap = %v, want 0", r["cap"])
	}
	if r["pct"].(float64) != 0 {
		t.Errorf("pct = %v, want 0 (cap=0 must not panic/divide by zero)", r["pct"])
	}
}

// TestListNodes_Empty: no seeded instances → empty (non-nil) array, not null.
func TestListNodes_Empty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _ := newAdminInstanceServer(t)
	s.deps.EvoCapPerNode = 10

	code, rows := callListNodes(s)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if rows == nil {
		t.Errorf("rows = nil, want empty non-nil array")
	}
	if len(rows) != 0 {
		t.Errorf("rows len = %d, want 0", len(rows))
	}
}
