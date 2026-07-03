// internal/store/proxy_zset_integration_test.go
package store

import (
	"testing"

	goredis "github.com/redis/go-redis/v9"
)

// TestProxyRedis_EndToEnd exercises the redis proxy ring end-to-end through
// the PUBLIC Manager API (BindProxy/ReleaseProxy/ReportProxyFailure/
// GetBoundProxy), proving the four user-facing M2 properties:
//
//   - cooldown:   a released/bound proxy isn't reused within its cooldown.
//   - capacity:   with only one proxy in the country, a second account misses.
//   - stickiness: GetBoundProxy reflects the PG durable binding.
//   - dead-proxy exclusion: a proxy killed via ReportProxyFailure stays out
//     of the ring even after the bound account releases it.
func TestProxyRedis_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchemaProxyRedis(t)

	seedAccount(t, ctx, m.BizPool(), 1, "a1", "15550000001")
	seedAccount(t, ctx, m.BizPool(), 1, "a2", "15550000002")
	seedProxy(t, ctx, m.BizPool(), "socks5://only", "US", 1)

	// Proxies are seeded after Manager construction (boot-time rebuild ran
	// against an empty proxy_pool), so rebuild the hot index manually.
	if _, err := m.proxyAlloc.rebuildFromPG(ctx, m.BizPool(), nowMsForTest()); err != nil {
		t.Fatalf("rebuildFromPG: %v", err)
	}

	// a1 binds the only US proxy.
	b1, err := m.BindProxy(ctx, "a1", "US")
	if err != nil {
		t.Fatalf("a1 bind: %v", err)
	}

	// capacity + cooldown: a2 cannot get the same proxy immediately (it's
	// already bound at capacity 1, so there's nothing else to hand out).
	if _, err := m.BindProxy(ctx, "a2", "US"); err != ErrNoProxyAvailable {
		t.Fatalf("a2 bind should miss (cooldown+capacity); got %v", err)
	}

	// stickiness: GetBoundProxy reads the PG durable binding for a1.
	pb, err := m.GetBoundProxy(ctx, "a1")
	if err != nil || pb == nil || pb.ProxyURL != b1.ProxyURL {
		t.Fatalf("GetBoundProxy sticky mismatch: %+v,%v", pb, err)
	}

	// dead-proxy exclusion: kill the proxy, then release it. Even though the
	// only proxy in the country is now free again, it must stay excluded
	// from the ring because it was marked dead.
	for i := 0; i < proxyFailureThreshold; i++ {
		if _, err := m.ReportProxyFailure(ctx, b1.ProxyID); err != nil {
			t.Fatalf("ReportProxyFailure #%d: %v", i+1, err)
		}
	}
	if err := m.ReleaseProxy(ctx, "a1"); err != nil {
		t.Fatalf("ReleaseProxy: %v", err)
	}
	if _, err := m.BindProxy(ctx, "a2", "US"); err != ErrNoProxyAvailable {
		t.Fatalf("dead proxy must stay excluded from ring; got %v", err)
	}
	// Distinguish "dead" from mere "still cooling": a cooling-but-alive proxy
	// would still carry a (future) score in avail:{cc}; a dead proxy is
	// entirely absent from the ZSET (markDead ZREMs it). This proves the
	// miss above is exclusion, not just an artifact of the release cooldown.
	if _, err := m.proxyAlloc.rdb.ZScore(ctx, availKey("US"), itoa(b1.ProxyID)).Result(); err != goredis.Nil {
		t.Fatalf("dead proxy still present in avail ring after release; ZScore err = %v; want redis.Nil", err)
	}
}
