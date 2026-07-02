// internal/control/janitor_test.go
package control

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestJanitorTick_ReleaseEvictWarm(t *testing.T) {
	ctx := context.Background()
	dead := []DeadProxyAccount{{JID: "a", CC: "US"}, {JID: "b", CC: "IN"}}
	var released, evicted []string
	warmed := map[string]string{}
	rebinds := 0

	j := NewJanitor(JanitorConfig{Batch: 100}, JanitorDeps{
		ListDeadProxyAccounts: func(_ context.Context, _ int) ([]DeadProxyAccount, error) {
			return dead, nil
		},
		Release: func(_ context.Context, jid string) error { released = append(released, jid); return nil },
		Evict: func(_ context.Context, jid string, linger time.Duration) {
			if linger != 0 {
				t.Fatalf("evict linger=%v want 0 (drop session immediately)", linger)
			}
			evicted = append(evicted, jid)
		},
		RequestWarm: func(_ context.Context, jid, cc string) error { warmed[jid] = cc; return nil },
		OnRebind:    func() { rebinds++ },
	})

	n, err := j.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n != 2 || rebinds != 2 {
		t.Fatalf("n=%d rebinds=%d want 2/2", n, rebinds)
	}
	if len(released) != 2 || len(evicted) != 2 {
		t.Fatalf("released=%v evicted=%v want 2 each", released, evicted)
	}
	if warmed["a"] != "US" || warmed["b"] != "IN" {
		t.Fatalf("warmed=%v want a->US b->IN", warmed)
	}
}

func TestJanitorTick_ReleaseErrorSkipsButContinues(t *testing.T) {
	ctx := context.Background()
	var warmed, evicted []string
	j := NewJanitor(JanitorConfig{Batch: 100}, JanitorDeps{
		ListDeadProxyAccounts: func(_ context.Context, _ int) ([]DeadProxyAccount, error) {
			return []DeadProxyAccount{{JID: "bad", CC: "US"}, {JID: "ok", CC: "US"}}, nil
		},
		Release: func(_ context.Context, jid string) error {
			if jid == "bad" {
				return errors.New("release failed")
			}
			return nil
		},
		Evict: func(_ context.Context, jid string, _ time.Duration) {
			evicted = append(evicted, jid)
		},
		RequestWarm: func(_ context.Context, jid, _ string) error { warmed = append(warmed, jid); return nil },
	})
	n, err := j.Tick(ctx)
	if err != nil {
		t.Fatalf("tick should not fail on per-account error: %v", err)
	}
	// "bad" 释放失败 → 跳过不补热；"ok" 正常处理。
	if n != 1 || len(warmed) != 1 || warmed[0] != "ok" {
		t.Fatalf("n=%d warmed=%v want 1 / [ok]", n, warmed)
	}
	// "bad" 释放失败 → 不应调用 Evict；只有 "ok" 应被 evict。
	if len(evicted) != 1 || evicted[0] != "ok" {
		t.Fatalf("evicted=%v want [ok]", evicted)
	}
}

func TestJanitorTick_RequestWarmErrorSkips(t *testing.T) {
	ctx := context.Background()
	var evicted []string
	rebinds := 0
	j := NewJanitor(JanitorConfig{Batch: 100}, JanitorDeps{
		ListDeadProxyAccounts: func(_ context.Context, _ int) ([]DeadProxyAccount, error) {
			return []DeadProxyAccount{{JID: "warmfail", CC: "US"}, {JID: "ok", CC: "US"}}, nil
		},
		Release: func(_ context.Context, _ string) error { return nil },
		Evict: func(_ context.Context, jid string, _ time.Duration) {
			evicted = append(evicted, jid)
		},
		RequestWarm: func(_ context.Context, jid, _ string) error {
			if jid == "warmfail" {
				return errors.New("warm failed")
			}
			return nil
		},
		OnRebind: func() { rebinds++ },
	})
	n, err := j.Tick(ctx)
	if err != nil {
		t.Fatalf("tick should not fail on per-account warm error: %v", err)
	}
	// RequestWarm 失败的 "warmfail" 不计入 rebound；只有 "ok" 计入。
	if n != 1 {
		t.Fatalf("n=%d want 1", n)
	}
	// OnRebind 只为成功的账号调用一次。
	if rebinds != 1 {
		t.Fatalf("rebinds=%d want 1", rebinds)
	}
	// Release 和 Evict 都应为两个账号调用（继续处理），但只有 "ok" 的 RequestWarm 成功。
	if len(evicted) != 2 {
		t.Fatalf("evicted=%v want 2 items (both accounts)", evicted)
	}
}
