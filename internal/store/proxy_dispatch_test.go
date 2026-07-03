// internal/store/proxy_dispatch_test.go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// nowMsForTest returns the current time in epoch milliseconds, for tests that
// need a real "now" to hand to the redis proxy allocator (e.g. a manual
// rebuildFromPG call after seeding proxies post-construction).
func nowMsForTest() int64 { return time.Now().UnixMilli() }

// newManagerWithSchemaProxyRedis builds a schema-applied Manager configured
// for the redis proxy backend (cfg.ProxyBackend="redis", cfg.Redis set).
// Migrations must be applied BEFORE constructing the Manager: newManager's
// boot-time rebuildFromPG queries proxy_pool immediately, and that table must
// already exist (it's fine for it to be empty at boot — proxies inserted by
// the test afterwards just require a second, manual rebuildFromPG call).
func newManagerWithSchemaProxyRedis(t *testing.T) (*Manager, context.Context) {
	t.Helper()
	ctx := context.Background()
	dsn := testDSN(t)

	migPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("migration pool: %v", err)
	}
	applyMigrations(t, ctx, migPool)
	migPool.Close()

	rdb := newTestRedis(t)
	m, err := newManager(ctx, Config{
		DSN:          dsn,
		ProxyBackend: "redis",
		Redis:        rdb,
	}, waLog.Noop)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(m.Close)
	return m, ctx
}

// TestManager_ProxyBackendDispatch verifies that when cfg.ProxyBackend=="redis"
// the public Manager.BindProxy dispatches through the redis hot-index
// allocator (proxyAlloc) rather than the default pg SKIP LOCKED path.
func TestManager_ProxyBackendDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchemaProxyRedis(t)

	seedAccount(t, ctx, m.BizPool(), 1, "acc1", "15550000001")
	seedProxy(t, ctx, m.BizPool(), "socks5://p", "US", 1)

	// newManager should have rebuilt the hot index at construction time; the
	// proxies above were inserted afterwards, so rebuild again manually
	// (mirrors what a real boot-then-seed sequence would need anyway).
	if m.proxyAlloc == nil {
		t.Fatalf("proxyAlloc is nil; want non-nil for ProxyBackend=redis")
	}
	if _, err := m.proxyAlloc.rebuildFromPG(ctx, m.bizPool, nowMsForTest()); err != nil {
		t.Fatalf("rebuildFromPG: %v", err)
	}

	b, err := m.BindProxy(ctx, "acc1", "US")
	if err != nil || b.ProxyURL != "socks5://p" {
		t.Fatalf("BindProxy via redis = %+v,%v", b, err)
	}
}

// TestManager_ProxyBackendDefaultPG verifies proxyAlloc stays nil (and
// BindProxy still works via the existing pg path) when ProxyBackend is unset.
func TestManager_ProxyBackendDefaultPG(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	if m.proxyAlloc != nil {
		t.Fatalf("proxyAlloc = %+v; want nil for default pg backend", m.proxyAlloc)
	}

	seedAccount(t, ctx, m.BizPool(), 1, "acc1", "15550000001")
	seedProxy(t, ctx, m.BizPool(), "socks5://p", "US", 1)

	b, err := m.BindProxy(ctx, "acc1", "US")
	if err != nil || b.ProxyURL != "socks5://p" {
		t.Fatalf("BindProxy via pg = %+v,%v", b, err)
	}
}
