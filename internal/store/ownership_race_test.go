// internal/store/ownership_race_test.go
package store

import (
	"context"
	"sync"
	"testing"
)

func TestRedisAcquireRace(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	o := newRedisOwnership(rdb)
	_ = o.Heartbeat(ctx, "A"); _ = o.Heartbeat(ctx, "B")

	var wg sync.WaitGroup
	var okCount int32
	var mu sync.Mutex
	for _, node := range []string{"A", "B"} {
		node := node
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := o.Acquire(ctx, "race-jid", node); err == nil {
				mu.Lock(); okCount++; mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okCount != 1 { t.Fatalf("exactly one acquire must win, got %d", okCount) }
}

func TestRedisFencingGCPause(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	o := newRedisOwnership(rdb)
	_ = o.Heartbeat(ctx, "A")

	hA, err := o.Acquire(ctx, "jid-x", "A")
	if err != nil { t.Fatal(err) }
	if !hA.Healthy(ctx) { t.Fatal("A should be healthy") }

	// 模拟 A 卡死：hb 过期 → B 接管
	rdb.Del(ctx, hbKey("A"))
	_ = o.Heartbeat(ctx, "B")
	hB, err := o.Acquire(ctx, "jid-x", "B")
	if err != nil { t.Fatalf("B acquire after A stale: %v", err) }
	_ = hB

	// A 醒来（hb 恢复）：仍不得 Healthy（fence 已被 B 顶替）
	_ = o.Heartbeat(ctx, "A")
	if hA.Healthy(ctx) { t.Fatal("stale owner A must NOT be healthy after B took over") }
}
