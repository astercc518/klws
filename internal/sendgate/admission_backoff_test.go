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
