// internal/store/ownership_redis_test.go
package store

import (
	"context"
	"testing"
	"time"
)

// TestOwnersFor_And_OwnedJIDs covers the Manager-level batch read/enumerate
// methods used by the admin console. Setup goes through redisOwnership
// directly (as the other tests in this file do) rather than
// Manager.AcquireDeviceLock, because AcquireDeviceLock pins nodeID to
// m.cfg.NodeID and this test needs two distinct owning nodes. Manager is
// constructed as a bare struct literal since OwnersFor/OwnedJIDs only touch
// m.cfg.Redis — no Postgres needed.
func TestOwnersFor_And_OwnedJIDs(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	o := newRedisOwnership(rdb)

	if _, err := o.Acquire(ctx, "jid-A", "node-1"); err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	if _, err := o.Acquire(ctx, "jid-B", "node-2"); err != nil {
		t.Fatalf("acquire B: %v", err)
	}

	m := &Manager{cfg: Config{Redis: rdb}}

	owners, err := m.OwnersFor(ctx, []string{"jid-A", "jid-B", "jid-missing"})
	if err != nil {
		t.Fatalf("OwnersFor: %v", err)
	}
	if owners["jid-A"] != "node-1" || owners["jid-B"] != "node-2" {
		t.Fatalf("owners = %v", owners)
	}
	if _, ok := owners["jid-missing"]; ok {
		t.Fatalf("missing jid should be absent")
	}

	all, err := m.OwnedJIDs(ctx)
	if err != nil {
		t.Fatalf("OwnedJIDs: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("OwnedJIDs len = %d, want 2", len(all))
	}
}

func TestRedisOwnership_ConfigurableTTL(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	rdb := newTestRedis(t) // existing store redis helper (redis_helper_test.go)
	o := newRedisOwnershipWithRoster(rdb, nil, 5*time.Second)
	if err := o.Heartbeat(context.Background(), "nodeX"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	ttl, err := rdb.TTL(context.Background(), hbKey("nodeX")).Result()
	if err != nil {
		t.Fatalf("ttl: %v", err)
	}
	if ttl <= 0 || ttl > 5*time.Second {
		t.Fatalf("hb TTL = %v; want ~5s (configured, not hardcoded 30s)", ttl)
	}
}

func TestRedisHeartbeatStaleUnowned(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	roster := func(context.Context) ([]string, error) { return []string{"jid-1", "jid-2"}, nil }
	const testHbTTL = 30 * time.Second
	o := newRedisOwnershipWithRoster(rdb, roster, testHbTTL)

	_ = o.Heartbeat(ctx, "node-A")
	if n, _ := rdb.Exists(ctx, hbKey("node-A")).Result(); n != 1 { t.Fatal("hb key missing") }
	ttl, _ := rdb.TTL(ctx, hbKey("node-A")).Result()
	if ttl <= 0 || ttl > testHbTTL { t.Fatalf("hb ttl=%v", ttl) }

	if _, err := o.Acquire(ctx, "jid-1", "node-A"); err != nil { t.Fatal(err) }
	// jid-2 unowned
	un, _ := o.Unowned(ctx)
	if len(un) != 1 || un[0] != "jid-2" { t.Fatalf("unowned=%v want [jid-2]", un) }

	// kill node-A heartbeat → jid-1 becomes stale
	rdb.Del(ctx, hbKey("node-A"))
	st, _ := o.StaleOwned(ctx, testHbTTL)
	if len(st) != 1 || st[0] != "jid-1" { t.Fatalf("stale=%v want [jid-1]", st) }
}

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
