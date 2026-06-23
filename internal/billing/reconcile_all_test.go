package billing

import (
	"testing"
)

func TestReconcileAll_ReturnsOnlyDrifting(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, _ := newRepoWithSchema(t)
	// tenant 1: healthy (funded + hold)
	r.Topup(ctx, 1, 1000, "s1")
	r.Hold(ctx, hReq(1, "m1", 300))
	// tenant 2: healthy
	r.Topup(ctx, 2, 500, "s2")
	// tenant 3: drifting (unledgered balance bump)
	r.Topup(ctx, 3, 800, "s3")
	if _, err := r.pool.Exec(ctx, `UPDATE tenant_wallets SET balance=balance+10 WHERE tenant_id=3`); err != nil {
		t.Fatalf("inject: %v", err)
	}

	drifts, err := r.ReconcileAll(ctx)
	if err != nil {
		t.Fatalf("reconcile all: %v", err)
	}
	if len(drifts) != 1 {
		t.Fatalf("drifting tenants = %d, want 1", len(drifts))
	}
	if drifts[0].TenantID != 3 || drifts[0].DriftBalance != 10 {
		t.Fatalf("drift = %+v, want tenant 3 drift_balance 10", drifts[0])
	}
}

func TestReconcileAll_AllHealthyReturnsEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, _ := newRepoWithSchema(t)
	r.Topup(ctx, 1, 1000, "s1")
	r.Hold(ctx, hReq(1, "m1", 300))
	r.Settle(ctx, 1, "m1")
	drifts, err := r.ReconcileAll(ctx)
	if err != nil {
		t.Fatalf("reconcile all: %v", err)
	}
	if len(drifts) != 0 {
		t.Fatalf("drifts = %d, want 0 (all healthy)", len(drifts))
	}
}
