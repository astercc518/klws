// internal/store/ownership_redis_test.go
package store

import (
	"context"
	"testing"
)

func TestRedisAcquireStillOwnerRelease(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	o := newRedisOwnership(rdb)

	if err := o.Heartbeat(ctx, "node-A"); err != nil { t.Fatal(err) }
	h, err := o.Acquire(ctx, "jid-1", "node-A")
	if err != nil { t.Fatalf("acquire: %v", err) }
	if !h.Healthy(ctx) { t.Fatal("freshly acquired must be Healthy") }

	got, _ := rdb.Get(ctx, ownerKey("jid-1")).Result()
	if got != "node-A:1" { t.Fatalf("owner=%q want node-A:1", got) }

	h.Release(ctx)
	if n, _ := rdb.Exists(ctx, ownerKey("jid-1")).Result(); n != 0 {
		t.Fatal("owner key must be gone after Release")
	}
}
