// internal/api/instances_create_test.go
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/audit"
	"github.com/acme/wadist/internal/console"
	wlog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/store"
)

// fakeInstanceEvo is a fake instanceEvoAPI: records every call and can be
// told to fail specific methods, so both handleAdminCreateInstance's
// rollback path and the T4 lifecycle endpoints (qr/state/reconnect/logout/
// delete) can be exercised without a real Evolution cluster/HTTP round-trip.
type fakeInstanceEvo struct {
	mu            sync.Mutex
	created       []string
	setProxyCalls []string
	deleted       []string
	failSetProxy  bool

	// connectCalls/connectBase64/failConnect back ConnectInstance (used by
	// both the QR fetch and reconnect endpoints).
	connectCalls  []string
	connectBase64 string
	failConnect   bool

	// fetchStateCalls/fetchStateVal/failFetchState back FetchState.
	fetchStateCalls []string
	fetchStateVal   string
	failFetchState  bool

	// logoutCalls/failLogout back LogoutInstance.
	logoutCalls []string
	failLogout  bool

	// failDelete makes DeleteInstance return an error (delete endpoint's
	// Evo-fails-so-don't-touch-the-DB path); deleted is still NOT appended
	// to on failure, mirroring a real client that didn't complete the call.
	failDelete bool
}

func (f *fakeInstanceEvo) CreateInstance(ctx context.Context, instanceName, webhookURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, instanceName)
	return nil
}

func (f *fakeInstanceEvo) SetProxy(ctx context.Context, instanceName string, proxy *store.ProxyBinding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setProxyCalls = append(f.setProxyCalls, instanceName)
	if f.failSetProxy {
		return errors.New("evolution: setProxy boom")
	}
	return nil
}

func (f *fakeInstanceEvo) DeleteInstance(ctx context.Context, instanceName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDelete {
		return errors.New("evolution: deleteInstance boom")
	}
	f.deleted = append(f.deleted, instanceName)
	return nil
}

func (f *fakeInstanceEvo) ConnectInstance(ctx context.Context, instanceName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls = append(f.connectCalls, instanceName)
	if f.failConnect {
		return "", errors.New("evolution: connectInstance boom")
	}
	return f.connectBase64, nil
}

func (f *fakeInstanceEvo) FetchState(ctx context.Context, instanceName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetchStateCalls = append(f.fetchStateCalls, instanceName)
	if f.failFetchState {
		return "", errors.New("evolution: fetchState boom")
	}
	return f.fetchStateVal, nil
}

func (f *fakeInstanceEvo) LogoutInstance(ctx context.Context, instanceName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logoutCalls = append(f.logoutCalls, instanceName)
	if f.failLogout {
		return errors.New("evolution: logoutInstance boom")
	}
	return nil
}

// newCreateInstanceServer builds a Server wired for POST /admin/instances
// tests: a real store.Manager (Postgres + Redis, same as
// newAdminInstanceServer) plus Tenants/Audit (handleAdminCreateInstance needs
// both) and a single-node "node-a" topology resolving to fake. seedProxies, if
// non-nil, runs AFTER migrations but BEFORE store.NewManager is constructed —
// required because the Manager's boot-time rebuildFromPG seeds the Redis hot
// index from proxy_pool exactly once, at construction; any proxy_pool row
// inserted afterward is invisible to the allocator for the rest of the test.
func newCreateInstanceServer(t *testing.T, fake *fakeInstanceEvo, seedProxies func(pool *pgxpool.Pool)) (*Server, *store.Manager, *pgxpool.Pool) {
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
	mgr, err := store.NewManager(ctx, store.Config{DSN: dsn, Redis: rdb, NodeID: "create-instance-test-node", BadgerDir: t.TempDir()}, wlog.Noop)
	if err != nil {
		t.Fatalf("store.NewManager: %v", err)
	}
	t.Cleanup(mgr.Close)

	s := &Server{
		sysPool: pool,
		deps: Deps{
			Mgr:                 mgr,
			Tenants:             console.NewTenantRepo(pool),
			Audit:               audit.NewAuditWriter(pool),
			EvoCapPerNode:       10,
			EvolutionWebhookURL: "https://hooks.example/evo",
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

// seedInstanceProxy inserts one alive proxy_pool row. Must run BEFORE
// store.NewManager (its boot-time rebuildFromPG seeds the Redis hot index
// from proxy_pool once, at construction).
func seedInstanceProxy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, url, cc string, maxBindings int) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx,
		`INSERT INTO proxy_pool (proxy_url, proxy_type, country_code, max_bindings) VALUES ($1,'socks5',$2,$3) RETURNING id`,
		url, cc, maxBindings).Scan(&id)
	if err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	return id
}

func postCreateInstance(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/instances", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleAdminCreateInstance(c)
	var env struct {
		Data    map[string]any `json:"data"`
		Message string         `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	return w.Code, env.Data
}

func countInstanceRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM account_instances`).Scan(&n); err != nil {
		t.Fatalf("count account_instances: %v", err)
	}
	return n
}

// TestCreateInstance_Happy: tenant exists + one alive US proxy seeded → POST
// creates an account_instances row (created state, proxy_id, evo_node set),
// calls Evolution CreateInstance+SetProxy exactly once, and writes an
// instance.create audit entry.
func TestCreateInstance_Happy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	fake := &fakeInstanceEvo{}
	var proxyID int64
	s, _, pool := newCreateInstanceServer(t, fake, func(pool *pgxpool.Pool) {
		proxyID = seedInstanceProxy(t, ctx, pool, "socks5://u:p@h1:1080", "US", 1)
	})

	tid, _ := seedTenantUser(t, ctx, s, "create-instance-happy@acme.test")

	code, data := postCreateInstance(t, s, `{"tenant_id":`+strconv.FormatInt(tid, 10)+`,"country_code":"US"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; data=%v", code, data)
	}

	name, _ := data["instance_name"].(string)
	if !strings.HasPrefix(name, "inst_"+strconv.FormatInt(tid, 10)+"_") {
		t.Fatalf("instance_name = %q, want prefix inst_%d_", name, tid)
	}
	if data["evo_node"] != "node-a" {
		t.Fatalf("evo_node = %v, want node-a", data["evo_node"])
	}
	if int64(data["proxy_id"].(float64)) != proxyID {
		t.Fatalf("proxy_id = %v, want %d", data["proxy_id"], proxyID)
	}
	if data["state"] != "created" {
		t.Fatalf("state = %v, want created", data["state"])
	}

	// account_instances row persisted with the right shape.
	var gotJID *string
	var gotTenant, gotProxy int64
	var gotNode, gotState string
	err := pool.QueryRow(ctx,
		`SELECT jid, tenant_id, evo_node, proxy_id, state FROM account_instances WHERE instance_name=$1`, name).
		Scan(&gotJID, &gotTenant, &gotNode, &gotProxy, &gotState)
	if err != nil {
		t.Fatalf("query account_instances: %v", err)
	}
	if gotJID != nil {
		t.Errorf("jid = %v, want NULL (zero-jid onboarding)", *gotJID)
	}
	if gotTenant != tid || gotNode != "node-a" || gotProxy != proxyID || gotState != "created" {
		t.Errorf("row = tenant=%d node=%q proxy=%d state=%q", gotTenant, gotNode, gotProxy, gotState)
	}

	// Evolution calls: CreateInstance then SetProxy, exactly once each.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.created) != 1 || fake.created[0] != name {
		t.Errorf("CreateInstance calls = %v, want [%s]", fake.created, name)
	}
	if len(fake.setProxyCalls) != 1 || fake.setProxyCalls[0] != name {
		t.Errorf("SetProxy calls = %v, want [%s]", fake.setProxyCalls, name)
	}
	if len(fake.deleted) != 0 {
		t.Errorf("DeleteInstance calls = %v, want none (happy path)", fake.deleted)
	}

	// audit: instance.create written.
	var auditCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action='instance.create' AND tenant_id=$1`, tid).Scan(&auditCount); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("audit_log instance.create rows = %d, want 1", auditCount)
	}

	// pool counters reflect the one binding.
	var cur int
	if err := pool.QueryRow(ctx, `SELECT current_bindings FROM proxy_pool WHERE id=$1`, proxyID).Scan(&cur); err != nil {
		t.Fatalf("query proxy_pool: %v", err)
	}
	if cur != 1 {
		t.Errorf("proxy_pool.current_bindings = %d, want 1", cur)
	}
}

// TestCreateInstance_NoProxyAvailable: tenant exists but no proxy seeded for
// the requested country → 402, no account_instances row, Evolution never
// called.
func TestCreateInstance_NoProxyAvailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	fake := &fakeInstanceEvo{}
	s, _, pool := newCreateInstanceServer(t, fake, nil) // no proxies seeded at all
	tid, _ := seedTenantUser(t, ctx, s, "create-instance-noproxy@acme.test")

	code, data := postCreateInstance(t, s, `{"tenant_id":`+strconv.FormatInt(tid, 10)+`,"country_code":"ZZ"}`)
	if code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; data=%v", code, data)
	}

	if n := countInstanceRows(t, ctx, pool); n != 0 {
		t.Errorf("account_instances rows = %d, want 0", n)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.created) != 0 || len(fake.setProxyCalls) != 0 {
		t.Errorf("Evolution must not be called: created=%v setProxy=%v", fake.created, fake.setProxyCalls)
	}
}

// TestCreateInstance_SetProxyFails_RollsBack: Evolution CreateInstance
// succeeds but SetProxy fails → 502, DeleteInstance called for rollback, the
// account_instances row is never left behind, and the allocated proxy's
// current_bindings counter returns to 0 (no leaked capacity).
func TestCreateInstance_SetProxyFails_RollsBack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	fake := &fakeInstanceEvo{failSetProxy: true}
	var proxyID int64
	s, _, pool := newCreateInstanceServer(t, fake, func(pool *pgxpool.Pool) {
		proxyID = seedInstanceProxy(t, ctx, pool, "socks5://u:p@h2:1080", "US", 1)
	})
	tid, _ := seedTenantUser(t, ctx, s, "create-instance-rollback@acme.test")

	code, data := postCreateInstance(t, s, `{"tenant_id":`+strconv.FormatInt(tid, 10)+`,"country_code":"US"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; data=%v", code, data)
	}

	if n := countInstanceRows(t, ctx, pool); n != 0 {
		t.Errorf("account_instances rows = %d, want 0 (no residue after rollback)", n)
	}

	fake.mu.Lock()
	if len(fake.created) != 1 {
		t.Fatalf("CreateInstance calls = %v, want exactly 1", fake.created)
	}
	name := fake.created[0]
	if len(fake.deleted) != 1 || fake.deleted[0] != name {
		t.Errorf("DeleteInstance calls = %v, want [%s] (rollback)", fake.deleted, name)
	}
	fake.mu.Unlock()

	var cur int
	if err := pool.QueryRow(ctx, `SELECT current_bindings FROM proxy_pool WHERE id=$1`, proxyID).Scan(&cur); err != nil {
		t.Fatalf("query proxy_pool: %v", err)
	}
	if cur != 0 {
		t.Errorf("proxy_pool.current_bindings = %d, want 0 (released on rollback)", cur)
	}
}

