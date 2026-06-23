// internal/billing/reconcile_test.go
package billing

import (
	"context"
	"testing"
)

// fundedRepo: fresh repo + a tenant funded via ledgered Topup(amount).
func fundedRepo(t *testing.T, tenantID, amount int64) (*Repo, context.Context) {
	t.Helper()
	r, ctx, _ := newRepoWithSchema(t)
	if err := r.Topup(ctx, tenantID, amount, "seed"); err != nil {
		t.Fatalf("seed topup: %v", err)
	}
	return r, ctx
}

func TestReconcileTenant_HealthyAfterHold(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx := fundedRepo(t, 1, 1000)
	if _, err := r.Hold(ctx, hReq(1, "m1", 300)); err != nil {
		t.Fatalf("hold: %v", err)
	}
	rep, err := r.ReconcileTenant(ctx, 1)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !rep.Healthy() {
		t.Fatalf("expected healthy, got drift bal=%d frz=%d charges=%d", rep.DriftBalance, rep.DriftFrozen, rep.DriftCharges)
	}
	// the run is audited
	var n int
	r.pool.QueryRow(ctx, `SELECT count(*) FROM reconciliation_runs WHERE tenant_id=1`).Scan(&n)
	if n != 1 {
		t.Fatalf("reconciliation_runs rows = %d, want 1", n)
	}
}

func TestReconcileTenant_DetectsBalanceDrift(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx := fundedRepo(t, 1, 1000)
	// inject drift: bump balance WITHOUT a ledger row
	if _, err := r.pool.Exec(ctx, `UPDATE tenant_wallets SET balance=balance+50 WHERE tenant_id=1`); err != nil {
		t.Fatalf("inject: %v", err)
	}
	rep, err := r.ReconcileTenant(ctx, 1)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.Healthy() {
		t.Fatal("expected drift, got healthy")
	}
	if rep.DriftBalance != 50 {
		t.Fatalf("drift_balance = %d, want 50", rep.DriftBalance)
	}
	var healthy bool
	r.pool.QueryRow(ctx, `SELECT healthy FROM reconciliation_runs WHERE tenant_id=1 ORDER BY id DESC LIMIT 1`).Scan(&healthy)
	if healthy {
		t.Fatal("audited run should be healthy=false")
	}
}

func TestReconcileTenant_DetectsFrozenVsChargesDrift(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx := fundedRepo(t, 1, 1000)
	r.Hold(ctx, hReq(1, "m1", 300)) // frozen=300, one held charge=300 → invariant② holds
	// corrupt invariant②: move the charge out of held WITHOUT touching frozen
	if _, err := r.pool.Exec(ctx, `UPDATE billing_charges SET state='settled' WHERE tenant_id=1 AND message_id='m1'`); err != nil {
		t.Fatalf("inject: %v", err)
	}
	rep, err := r.ReconcileTenant(ctx, 1)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// frozen(300) no longer equals Σ open-charge amounts(0)
	if rep.DriftCharges != 300 {
		t.Fatalf("drift_charges = %d, want 300", rep.DriftCharges)
	}
	if rep.Healthy() {
		t.Fatal("expected drift via invariant②")
	}
}
