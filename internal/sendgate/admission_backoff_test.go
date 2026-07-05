// internal/sendgate/admission_backoff_test.go
package sendgate

import (
	"context"
	"testing"
	"time"
)

func TestRecordWarning_DoublesToCap(t *testing.T) {
	rdb := redisClient(t)
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: true, Factor: 2, Max: 8, TTL: 300 * time.Second})
	key := "backoff:cc:BR"
	want := []int{2, 4, 8, 8}
	for i, w := range want {
		if err := a.RecordWarning(ctx, key); err != nil {
			t.Fatalf("warning %d: %v", i, err)
		}
		got, err := rdb.Get(ctx, key).Int()
		if err != nil {
			t.Fatalf("get after %d: %v", i, err)
		}
		if got != w {
			t.Fatalf("after warning %d mult=%d; want %d", i, got, w)
		}
	}
}

func TestRecordWarning_OffWritesNothing(t *testing.T) {
	rdb := redisClient(t)
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: false, Factor: 2, Max: 8, TTL: 300 * time.Second})
	if err := a.RecordWarning(ctx, "backoff:cc:BR"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if n, _ := rdb.Exists(ctx, "backoff:cc:BR").Result(); n != 0 {
		t.Fatalf("key exists=%d; want 0 (off writes nothing)", n)
	}
}

func TestRecordWarning_SetsTTL(t *testing.T) {
	rdb := redisClient(t)
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: true, Factor: 2, Max: 8, TTL: 300 * time.Second})
	_ = a.RecordWarning(ctx, "backoff:cc:BR")
	ttl, _ := rdb.TTL(ctx, "backoff:cc:BR").Result()
	if ttl <= 0 || ttl > 300*time.Second {
		t.Fatalf("ttl=%v; want (0,300s]", ttl)
	}
}

func TestRecordWarning_MultiSegIndependent(t *testing.T) {
	rdb := redisClient(t)
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: true, Factor: 2, Max: 8, TTL: 300 * time.Second})
	_ = a.RecordWarning(ctx, "backoff:cc:BR", "backoff:net:1.2.3.0/24")
	br, _ := rdb.Get(ctx, "backoff:cc:BR").Int()
	nt, _ := rdb.Get(ctx, "backoff:net:1.2.3.0/24").Int()
	if br != 2 || nt != 2 {
		t.Fatalf("br=%d net=%d; want both 2", br, nt)
	}
}

func TestAdmit_BackoffWidensMinGap(t *testing.T) {
	rdb := redisClient(t)
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: true, Factor: 2, Max: 8, TTL: 300 * time.Second})
	base := time.Unix(1_000_000, 0).UTC()
	minGap := 100 * time.Millisecond
	ccKey, netKey := "backoff:cc:BR", "backoff:net:x"

	// first send admits.
	if tk, _, err := a.Admit(ctx, "j1", 100, minGap, base, ccKey, netKey); err != nil || tk == nil {
		t.Fatalf("first admit: tk=%v err=%v", tk, err)
	}
	// set cc backoff mult=4 → effective gap 400ms.
	_ = a.RecordWarning(ctx, ccKey) // 2
	_ = a.RecordWarning(ctx, ccKey) // 4
	// a send 300ms later: without backoff (gap 100ms) it WOULD pass; with mult 4
	// (gap 400ms) it must be paced-rejected.
	_, reason, err := a.Admit(ctx, "j1", 100, minGap, base.Add(300*time.Millisecond), ccKey, netKey)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if reason != "pacing" {
		t.Fatalf("reason=%q; want pacing (backoff widened gap to 400ms)", reason)
	}
}

func TestAdmit_OffModeUnchanged(t *testing.T) {
	rdb := redisClient(t)
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: false})
	base := time.Unix(2_000_000, 0).UTC()
	// even with a backoff key present, off-mode admit ignores it.
	rdb.Set(ctx, "backoff:cc:BR", 8, 0)
	if tk, _, err := a.Admit(ctx, "j2", 100, 100*time.Millisecond, base, "backoff:cc:BR", "backoff:net:x"); err != nil || tk == nil {
		t.Fatalf("first: %v", err)
	}
	// 150ms later > 100ms base gap → passes (backoff ignored in off mode).
	if tk, _, err := a.Admit(ctx, "j2", 100, 100*time.Millisecond, base.Add(150*time.Millisecond), "backoff:cc:BR", "backoff:net:x"); err != nil || tk == nil {
		t.Fatalf("second should pass in off mode; tk=%v err=%v", tk, err)
	}
}
