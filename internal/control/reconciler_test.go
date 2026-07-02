// internal/control/reconciler_test.go
package control

import (
	"context"
	"testing"
)

func TestReconcilerTick_EnqueuesUnscheduled(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	rdb := newTestRedis(t)
	sched := NewScheduler(rdb)

	// resident（已温）：r1；已排队：q1（在 due:US）。应被跳过。
	rdb.SAdd(ctx, residentKey, "r1")
	_ = sched.Enqueue(ctx, "q1", "US", 5000)

	active := []string{"r1", "q1", "fresh1", "fresh2"} // fresh* 既非温又未排队
	cc := map[string]string{"r1": "US", "q1": "US", "fresh1": "US", "fresh2": "IN"}

	r := NewReconciler(rdb, sched, []string{"US", "IN"}, ReconcileDeps{
		ListActive: func(_ context.Context) ([]string, error) { return active, nil },
		CountryOf:  func(_ context.Context, jid string) string { return cc[jid] },
		NextDue:    func(_ string) int64 { return 9000 },
	})

	n, err := r.Tick(ctx, 1000)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n != 2 {
		t.Fatalf("reconciled n=%d want 2 (fresh1, fresh2)", n)
	}
	// fresh1 进 due:US，fresh2 进 due:IN，score=NextDue=9000。
	if s := rdb.ZScore(ctx, dueKey("US"), "fresh1").Val(); s != 9000 {
		t.Fatalf("fresh1 due:US score=%v want 9000", s)
	}
	if s := rdb.ZScore(ctx, dueKey("IN"), "fresh2").Val(); s != 9000 {
		t.Fatalf("fresh2 due:IN score=%v want 9000", s)
	}
	// r1（温）与 q1（已排队）不应被改动：q1 score 仍 5000，r1 不入 due。
	if s := rdb.ZScore(ctx, dueKey("US"), "q1").Val(); s != 5000 {
		t.Fatalf("q1 score=%v want unchanged 5000", s)
	}
	if rdb.ZScore(ctx, dueKey("US"), "r1").Err() == nil {
		t.Fatal("r1 (resident) should not be enqueued")
	}
}

func TestReconcilerTick_SkipsCountryNotInCCs(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	rdb := newTestRedis(t)
	sched := NewScheduler(rdb)

	active := []string{"us1", "x1", "unbound1"}
	cc := map[string]string{"us1": "US", "x1": "BR", "unbound1": ""}

	r := NewReconciler(rdb, sched, []string{"US"}, ReconcileDeps{
		ListActive: func(_ context.Context) ([]string, error) { return active, nil },
		CountryOf:  func(_ context.Context, jid string) string { return cc[jid] },
		NextDue:    func(_ string) int64 { return 9000 },
	})

	n, err := r.Tick(ctx, 1000)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n != 1 {
		t.Fatalf("reconciled n=%d want 1 (only us1)", n)
	}
	if s := rdb.ZScore(ctx, dueKey("US"), "us1").Val(); s != 9000 {
		t.Fatalf("us1 due:US score=%v want 9000", s)
	}
	if c := rdb.ZCard(ctx, dueKey("BR")).Val(); c != 0 {
		t.Fatalf("due:BR card=%d want 0 (BR not an active cc)", c)
	}
	if c := rdb.ZCard(ctx, dueKey("")).Val(); c != 0 {
		t.Fatalf("due: card=%d want 0 (unbound not enqueued)", c)
	}

	n2, err := r.Tick(ctx, 1000)
	if err != nil {
		t.Fatalf("tick2: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("reconciled n2=%d want 0 (idempotent, no re-count of x1/unbound1)", n2)
	}
}

func TestReconcilerTick_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	rdb := newTestRedis(t)
	sched := NewScheduler(rdb)
	r := NewReconciler(rdb, sched, []string{"US"}, ReconcileDeps{
		ListActive: func(_ context.Context) ([]string, error) { return []string{"x"}, nil },
		CountryOf:  func(_ context.Context, _ string) string { return "US" },
		NextDue:    func(_ string) int64 { return 7000 },
	})
	n1, _ := r.Tick(ctx, 1000)
	n2, _ := r.Tick(ctx, 1000) // 第二次：x 已在 due → 不再补入
	if n1 != 1 || n2 != 0 {
		t.Fatalf("n1=%d n2=%d want 1/0 (idempotent)", n1, n2)
	}
	if c := rdb.ZCard(ctx, dueKey("US")).Val(); c != 1 {
		t.Fatalf("due:US card=%d want 1 (no dup)", c)
	}
}
