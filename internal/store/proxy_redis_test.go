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
