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

func TestRedisProxy_ReleaseAndDead(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	rdb := newTestRedis(t)
	a := newRedisProxyAllocator(rdb, 60*time.Second)
	cooldownMs := (60 * time.Second).Milliseconds()
	seedProxyRedis(t, rdb, 7, "US", 1, "socks5://p7", 0)

	id, _, err := a.pick(ctx, "US", 1000) // consumes the 1 slot + cools + ZREMs
	if err != nil || id != 7 {
		t.Fatalf("pick = %d,%v; want 7,nil", id, err)
	}

	// release makes it re-eligible after cooldown (score=1000+cooldownMs).
	if err := a.release(ctx, 7, "US", 1000); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, _, err := a.pick(ctx, "US", 1001); err != ErrNoProxyAvailable {
		t.Fatalf("still cooling, want miss; got %v", err)
	}
	if _, _, err := a.pick(ctx, "US", 1000+cooldownMs+1); err != nil {
		t.Fatalf("after cooldown pick: %v", err)
	}

	// markDead excludes it entirely.
	seedProxyRedis(t, rdb, 9, "US", 1, "socks5://p9", 0)
	if err := a.markDead(ctx, 9, "US"); err != nil {
		t.Fatalf("markDead: %v", err)
	}
	if _, _, err := a.pick(ctx, "US", 200_000); err != ErrNoProxyAvailable {
		t.Fatalf("dead proxy must be excluded; got %v", err)
	}
}

func TestRedisProxy_ReleaseAfterDeadNoResurrect(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	rdb := newTestRedis(t)
	a := newRedisProxyAllocator(rdb, 60*time.Second)

	// proxy 13 dies while bound, gets HDEL'd from meta+free and ZREM'd.
	seedProxyRedis(t, rdb, 13, "US", 1, "socks5://p13", 0)
	if err := a.markDead(ctx, 13, "US"); err != nil {
		t.Fatalf("markDead: %v", err)
	}

	// releasing the now-dead binding must NOT resurrect it (meta was deleted;
	// resurrecting would leave it in the ring with empty meta → garbage).
	if err := a.release(ctx, 13, "US", 1000); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := rdb.ZScore(ctx, availKey("US"), itoa(13)).Result(); err != goredis.Nil {
		t.Fatalf("dead proxy resurrected into ring; ZScore err = %v; want redis.Nil", err)
	}
	if _, _, err := a.pick(ctx, "US", 1000+10*60000); err != ErrNoProxyAvailable {
		t.Fatalf("dead proxy must stay excluded after release; got %v", err)
	}
}

func TestRedisProxy_MarkAliveRevives(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	rdb := newTestRedis(t)
	a := newRedisProxyAllocator(rdb, 60*time.Second)

	// proxy 11 starts dead: no entry anywhere.
	if _, _, err := a.pick(ctx, "US", 1000); err != ErrNoProxyAvailable {
		t.Fatalf("pick before markAlive err = %v; want ErrNoProxyAvailable", err)
	}

	if err := a.markAlive(ctx, 11, "US", "socks5://p11|socks5|US", 3, 1000); err != nil {
		t.Fatalf("markAlive: %v", err)
	}

	id, meta, err := a.pick(ctx, "US", 1000)
	if err != nil || id != 11 || meta != "socks5://p11|socks5|US" {
		t.Fatalf("pick after markAlive = %d,%q,%v; want 11,meta,nil", id, meta, err)
	}
	free, err := rdb.HGet(ctx, proxyFreeKey, itoa(11)).Int()
	if err != nil {
		t.Fatalf("hget free: %v", err)
	}
	if free != 2 {
		t.Fatalf("free = %d; want 2 (3 seeded - 1 consumed by pick)", free)
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
