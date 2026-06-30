package control

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeDeps struct {
	mu      sync.Mutex
	warmed  []string
	evicted []string
	bound   map[string]int
	warmSet map[string]bool
}

func newFakeDeps() *fakeDeps { return &fakeDeps{bound: map[string]int{}, warmSet: map[string]bool{}} }
func (d *fakeDeps) deps() Deps {
	return Deps{
		Warm: func(_ context.Context, jid string) (bool, error) {
			d.mu.Lock(); defer d.mu.Unlock()
			d.warmed = append(d.warmed, jid); d.warmSet[jid] = true; return true, nil
		},
		Evict: func(_ context.Context, jid string, _ time.Duration) {
			d.mu.Lock(); defer d.mu.Unlock()
			d.evicted = append(d.evicted, jid); d.warmSet[jid] = false
		},
		IsWarm:    func(jid string) bool { d.mu.Lock(); defer d.mu.Unlock(); return d.warmSet[jid] },
		BindProxy: func(_ context.Context, jid, _ string) error { d.mu.Lock(); defer d.mu.Unlock(); d.bound[jid]++; return nil },
	}
}

func TestWorkingSetRunStopsOnCancel(t *testing.T) {
	// This test does not require Redis — it only verifies that Run exits when
	// the context is cancelled. We pass a nil rdb and confirm Run returns
	// context.Canceled promptly.
	ctx, cancel := context.WithCancel(context.Background())
	d := newFakeDeps()
	// Use a nil rdb — Run will never reach Tick because we cancel immediately.
	w := &WorkingSet{deps: d.deps(), cfg: Config{Target: 1, WarmReqBatch: 1}}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, 10*time.Millisecond) }()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop within 1s after cancel")
	}
}

func TestWorkingSetActiveWarm(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	sched := NewScheduler(rdb)
	for _, j := range []string{"a", "b", "c"} { _ = sched.Enqueue(ctx, j, "US", 1000) }
	d := newFakeDeps()
	w := NewWorkingSet(rdb, sched, []string{"US"}, Config{Target: 2, WarmReqBatch: 16, KeepWarmHorizon: 90 * time.Second}, d.deps())

	if err := w.Tick(ctx, 5000); err != nil { t.Fatal(err) }
	if len(d.warmed) != 2 { t.Fatalf("warmed=%v want 2 (target)", d.warmed) }
}

func TestWorkingSetReactiveWarm(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	sched := NewScheduler(rdb)
	d := newFakeDeps()
	w := NewWorkingSet(rdb, sched, []string{"US"}, Config{Target: 10, WarmReqBatch: 16, KeepWarmHorizon: 90 * time.Second}, d.deps())
	rdb.RPush(ctx, "warm:req", "hot1|US")
	if err := w.Tick(ctx, 5000); err != nil { t.Fatal(err) }
	if len(d.warmed) != 1 || d.warmed[0] != "hot1" { t.Fatalf("reactive warmed=%v want [hot1]", d.warmed) }
}
