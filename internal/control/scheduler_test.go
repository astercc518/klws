package control

import (
	"context"
	"math/rand"
	"testing"
)

func TestSchedulerPopDue(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	s := NewScheduler(newTestRedis(t))
	_ = s.Enqueue(ctx, "jid-late", "US", 10_000)
	_ = s.Enqueue(ctx, "jid-due1", "US", 1_000)
	_ = s.Enqueue(ctx, "jid-due2", "US", 2_000)

	got, err := s.PopDue(ctx, "US", 5_000, 10)
	if err != nil { t.Fatal(err) }
	if len(got) != 2 || got[0] != "jid-due1" || got[1] != "jid-due2" {
		t.Fatalf("popDue=%v want [jid-due1 jid-due2]", got)
	}
	// 未到期的仍在
	again, _ := s.PopDue(ctx, "US", 5_000, 10)
	if len(again) != 0 { t.Fatalf("second pop should be empty, got %v", again) }
}

func TestNextEligibleSpread(t *testing.T) {
	w := Window{StartHour: 9, EndHour: 22}
	rng := rand.New(rand.NewSource(1))
	now := int64(1_000_000_000_000)
	for i := 0; i < 1000; i++ {
		got := NextEligibleMs(now, 7, w, rng)
		delta := got - now
		if delta < 60_000 { t.Fatalf("delta %dms < 60s 硬下限", delta) }
	}
}
