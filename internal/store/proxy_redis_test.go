// internal/store/proxy_redis_test.go
package store

import (
	"context"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

func TestRedisProxy_PickCoolsAndCapacity(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	rdb := newTestRedis(t) // existing store redis helper (redis_helper_test.go)
	a := newRedisProxyAllocator(rdb, 60*time.Second)

	// seed: proxy 7, cc=US, 1 free slot, eligible now, meta.
	seedProxyRedis(t, rdb, 7, "US", 1, "socks5://p7", 0)

	id, meta, err := a.pick(ctx, "US", 1000)
	if err != nil || id != 7 || meta != "socks5://p7|socks5|US" {
		t.Fatalf("pick = %d,%q,%v; want 7,meta,nil", id, meta, err)
	}
	// immediate second pick: proxy 7 is now cooling (score=1000+60000) AND had only
	// 1 slot → removed from ZSET; must miss.
	if _, _, err := a.pick(ctx, "US", 1001); err != ErrNoProxyAvailable {
		t.Fatalf("second pick err = %v; want ErrNoProxyAvailable", err)
	}
}

func TestRedisProxy_PickCoolsButKeepsSlot(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	rdb := newTestRedis(t)
	cooldown := 60 * time.Second
	cooldownMs := cooldown.Milliseconds()
	a := newRedisProxyAllocator(rdb, cooldown)

	// seed: proxy 8, cc=US, 2 free slots, eligible now, meta.
	seedProxyRedis(t, rdb, 8, "US", 2, "socks5://p8", 0)

	// first pick: consumes 1 slot → free=1>0, so proxy stays in avail with a
	// fresh cooldown score.
	id, meta, err := a.pick(ctx, "US", 1000)
	if err != nil || id != 8 || meta != "socks5://p8|socks5|US" {
		t.Fatalf("first pick = %d,%q,%v; want 8,meta,nil", id, meta, err)
	}
	score, err := rdb.ZScore(ctx, availKey("US"), itoa(8)).Result()
	if err != nil {
		t.Fatalf("zscore after pick: %v", err)
	}
	if wantScore := float64(1000 + cooldownMs); score != wantScore {
		t.Fatalf("avail score = %v; want %v (now+cooldown, still in ring)", score, wantScore)
	}
	free, err := rdb.HGet(ctx, proxyFreeKey, itoa(8)).Int()
	if err != nil {
		t.Fatalf("hget free after pick: %v", err)
	}
	if free != 1 {
		t.Fatalf("free = %d; want 1 (one slot consumed)", free)
	}

	// still cooling: 1ms before cooldown elapses → miss.
	if _, _, err := a.pick(ctx, "US", 1000+cooldownMs-1); err != ErrNoProxyAvailable {
		t.Fatalf("pick while cooling err = %v; want ErrNoProxyAvailable", err)
	}

	// cooldown elapsed: eligible again; this consumes the last slot → free=0 → ZREM.
	id, _, err = a.pick(ctx, "US", 1000+cooldownMs)
	if err != nil || id != 8 {
		t.Fatalf("post-cooldown pick = %d,%v; want 8,nil", id, err)
	}
	// last slot consumed → proxy ZREM'd from the ring (ZScore → redis.Nil).
	if _, err := rdb.ZScore(ctx, availKey("US"), itoa(8)).Result(); err != goredis.Nil {
		t.Fatalf("zscore after last-slot pick err = %v; want redis.Nil (member ZREM'd)", err)
	}
	if free, _ := rdb.HGet(ctx, proxyFreeKey, itoa(8)).Int(); free != 0 {
		t.Fatalf("free = %d after last-slot pick; want 0", free)
	}
}

// seedProxyRedis writes the hot-index entries a boot-rebuild would create.
func seedProxyRedis(t *testing.T, rdb *goredis.Client, id int64, cc string, free int, url string, nextMs int64) {
	t.Helper()
	ctx := context.Background()
	member := itoa(id)
	if err := rdb.ZAdd(ctx, availKey(cc), goredis.Z{Score: float64(nextMs), Member: member}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.HSet(ctx, proxyFreeKey, member, free).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.HSet(ctx, proxyMetaKey, member, url+"|socks5|"+cc).Err(); err != nil {
		t.Fatal(err)
	}
}
