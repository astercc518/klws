// internal/store/proxy_dispatch_test.go
package store

import (
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// nowMsForTest returns the current time in epoch milliseconds, for tests that
// need a real "now" to hand to the redis proxy allocator (e.g. a manual
// rebuildFromPG call after seeding proxies post-construction).
func nowMsForTest() int64 { return time.Now().UnixMilli() }

// TestManager_ProxyBackendDispatch verifies that the public Manager.BindProxy
// dispatches through the redis hot-index allocator (proxyAlloc) — the sole
// proxy allocation backend.
func TestManager_ProxyBackendDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)

	seedAccount(t, ctx, m.BizPool(), 1, "acc1", "15550000001")
	seedProxy(t, ctx, m.BizPool(), "socks5://p", "US", 1)

	// newManager rebuilt the hot index at construction time; the proxy above
	// was inserted afterwards, so rebuild again manually (mirrors what a real
	// boot-then-seed sequence would need anyway).
	if m.proxyAlloc == nil {
		t.Fatalf("proxyAlloc is nil; want non-nil (redis is the sole proxy backend)")
	}
	if _, err := m.proxyAlloc.rebuildFromPG(ctx, m.bizPool, nowMsForTest()); err != nil {
		t.Fatalf("rebuildFromPG: %v", err)
	}

	b, err := m.BindProxy(ctx, "acc1", "US")
	if err != nil || b.ProxyURL != "socks5://p" {
		t.Fatalf("BindProxy via redis = %+v,%v", b, err)
	}
}

// TestReportProxyFailure_RedisMarkDead verifies the redis-backend
// ReportProxyFailure removes the proxy from proxy:avail:{cc} once it crosses
// the failure threshold (markDead), so no further account can bind it.
func TestReportProxyFailure_RedisMarkDead(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)

	pid := seedProxy(t, ctx, m.BizPool(), "socks5://p", "US", 2)
	if _, err := m.proxyAlloc.rebuildFromPG(ctx, m.bizPool, nowMsForTest()); err != nil {
		t.Fatalf("rebuildFromPG: %v", err)
	}
	// present in the ring after rebuild.
	if _, err := m.proxyAlloc.rdb.ZScore(ctx, availKey("US"), itoa(pid)).Result(); err != nil {
		t.Fatalf("proxy not in ring after rebuild: %v", err)
	}

	var dead bool
	for i := 0; i < proxyFailureThreshold; i++ {
		var err error
		dead, err = m.ReportProxyFailure(ctx, pid)
		if err != nil {
			t.Fatalf("ReportProxyFailure #%d: %v", i+1, err)
		}
	}
	if !dead {
		t.Fatalf("dead = false after %d failures; want true", proxyFailureThreshold)
	}

	// markDead removed it from the ring (ZREM'd → ZScore redis.Nil).
	if _, err := m.proxyAlloc.rdb.ZScore(ctx, availKey("US"), itoa(pid)).Result(); err != goredis.Nil {
		t.Fatalf("proxy still in ring after markDead; ZScore err = %v; want redis.Nil", err)
	}
	// a fresh bind for another account in US must now miss.
	seedAccount(t, ctx, m.BizPool(), 1, "acc1", "15550000001")
	if _, err := m.BindProxy(ctx, "acc1", "US"); err != ErrNoProxyAvailable {
		t.Fatalf("BindProxy after markDead = %v; want ErrNoProxyAvailable", err)
	}
}

// TestReportProxySuccess_RedisMarkAlive verifies the redis-backend
// ReportProxySuccess re-admits a previously-dead proxy into proxy:avail:{cc}
// (markAlive), so accounts can bind it again.
func TestReportProxySuccess_RedisMarkAlive(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)

	pid := seedProxy(t, ctx, m.BizPool(), "socks5://p", "US", 1)
	if _, err := m.proxyAlloc.rebuildFromPG(ctx, m.bizPool, nowMsForTest()); err != nil {
		t.Fatalf("rebuildFromPG: %v", err)
	}

	// fail it to death → removed from the ring.
	for i := 0; i < proxyFailureThreshold; i++ {
		if _, err := m.ReportProxyFailure(ctx, pid); err != nil {
			t.Fatalf("ReportProxyFailure #%d: %v", i+1, err)
		}
	}
	if _, err := m.proxyAlloc.rdb.ZScore(ctx, availKey("US"), itoa(pid)).Result(); err != goredis.Nil {
		t.Fatalf("proxy still in ring after failing to death; want redis.Nil, got %v", err)
	}

	// ReportProxySuccess revives it into the ring.
	if err := m.ReportProxySuccess(ctx, pid, 42); err != nil {
		t.Fatalf("ReportProxySuccess: %v", err)
	}
	if _, err := m.proxyAlloc.rdb.ZScore(ctx, availKey("US"), itoa(pid)).Result(); err != nil {
		t.Fatalf("proxy not back in ring after markAlive: %v", err)
	}

	// and an account can bind it again.
	seedAccount(t, ctx, m.BizPool(), 1, "acc1", "15550000001")
	b, err := m.BindProxy(ctx, "acc1", "US")
	if err != nil || b == nil || b.ProxyID != pid || b.ProxyURL != "socks5://p" {
		t.Fatalf("BindProxy after markAlive = %+v,%v; want proxy %d bound", b, err, pid)
	}
}
