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
//   - capacity:   with only one single-slot proxy in a country, a second
//     account misses because the ring is emptied (pick ZREMs the last slot).
//   - cooldown:   a proxy that STILL has a free slot is nonetheless skipped
//     within its cooldown window (its ZSET score is in the future) — isolated
//     from capacity using a 2-slot proxy.
//   - stickiness: GetBoundProxy reflects the PG durable binding.
//   - dead-proxy exclusion: a proxy killed via ReportProxyFailure stays out
//     of the ring even after the bound account releases it.
func TestProxyRedis_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)

	seedAccount(t, ctx, m.BizPool(), 1, "a1", "15550000001")
	seedAccount(t, ctx, m.BizPool(), 1, "a2", "15550000002")
	seedAccount(t, ctx, m.BizPool(), 1, "gb1", "15550000011")
	seedAccount(t, ctx, m.BizPool(), 1, "gb2", "15550000012")
	seedProxy(t, ctx, m.BizPool(), "socks5://only", "US", 1)
	// A second proxy in a DIFFERENT country with TWO slots, used to isolate
	// cooldown from capacity: after one bind it still has a free slot, so a
	// miss on it can only be explained by its (future) cooldown score.
	gbID := seedProxy(t, ctx, m.BizPool(), "socks5://gb", "GB", 2)

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

	// capacity exhaustion (ring empty, only proxy consumed): the single US
	// proxy has max_bindings=1, so after a1's bind pick's free<=0 branch
	// ZREM'd it from the ring entirely. a2's miss here is PURE CAPACITY — it
	// does NOT prove cooldown (the GB case below isolates that).
	if _, err := m.BindProxy(ctx, "a2", "US"); err != ErrNoProxyAvailable {
		t.Fatalf("a2 bind should miss (capacity exhaustion, ring empty, only proxy consumed); got %v", err)
	}

	// cooldown, ISOLATED from capacity: gb1 binds the 2-slot GB proxy. After
	// that pick the proxy STILL HAS A FREE SLOT (free 2->1, >0, so it stays
	// in the ring) but its score was re-armed to bindTime+cooldown (60s in
	// the future). gb2's immediate bind therefore misses — and capacity
	// cannot explain it, because a free slot exists. This is the genuine
	// cooldown proof (deterministic: default 60s cooldown >> test runtime,
	// no sleeps).
	if _, err := m.BindProxy(ctx, "gb1", "GB"); err != nil {
		t.Fatalf("gb1 bind: %v", err)
	}
	if _, err := m.BindProxy(ctx, "gb2", "GB"); err != ErrNoProxyAvailable {
		t.Fatalf("gb2 bind should miss (cooldown: free slot exists but proxy is cooling); got %v", err)
	}
	// Make the cooldown-vs-capacity distinction unambiguous via direct state
	// inspection: the GB proxy is still IN the ring with a score strictly in
	// the future (cooling), and it still has a free slot (free==1). A
	// capacity/dead miss would instead show the member ABSENT from the ring.
	score, err := m.proxyAlloc.rdb.ZScore(ctx, availKey("GB"), itoa(gbID)).Result()
	if err != nil {
		t.Fatalf("GB proxy should still be in ring (cooling); ZScore err = %v", err)
	}
	if now := float64(nowMsForTest()); score <= now {
		t.Fatalf("GB proxy score %v not strictly in the future (now=%v); cooldown not armed", score, now)
	}
	if free, err := m.proxyAlloc.rdb.HGet(ctx, proxyFreeKey, itoa(gbID)).Result(); err != nil || free != "1" {
		t.Fatalf("GB proxy free slots = %q,%v; want \"1\" (free slot exists → the miss was cooldown, not capacity)", free, err)
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
