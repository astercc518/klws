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
	if call("?tenant_id="+strconv.FormatInt(tidA, 10))["total"].(float64) != 3 {
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

