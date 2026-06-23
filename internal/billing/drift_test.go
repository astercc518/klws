// internal/billing/drift_test.go
package billing

import (
	"context"
	"errors"
	"testing"
)

func TestHold_RejectsLockedWallet(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx := fundedRepo(t, 1, 1000)
	if _, err := r.pool.Exec(ctx, `UPDATE tenant_wallets SET locked=TRUE WHERE tenant_id=1`); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if _, err := r.Hold(ctx, hReq(1, "m1", 300)); !errors.Is(err, ErrWalletLocked) {
		t.Fatalf("hold err = %v, want ErrWalletLocked", err)
	}
}

func TestDriftHandler_AuditsLocksNotifies(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx := fundedRepo(t, 1, 1000)
	if _, err := r.pool.Exec(ctx, `UPDATE tenant_wallets SET balance=balance+10 WHERE tenant_id=1`); err != nil {
		t.Fatalf("inject: %v", err)
	}
	drifts, _ := r.ReconcileAll(ctx)
	if len(drifts) != 1 {
		t.Fatalf("expected 1 drift, got %d", len(drifts))
	}

	var notified int64
	h := NewDriftHandler(r, true /*autoLock*/, func(_ context.Context, rep Report) { notified = rep.TenantID })
	if err := h.HandleDrift(ctx, drifts[0]); err != nil {
		t.Fatalf("handle: %v", err)
	}
	// audited as unhealthy
	var n int
	r.pool.QueryRow(ctx, `SELECT count(*) FROM reconciliation_runs WHERE tenant_id=1 AND healthy=FALSE`).Scan(&n)
	if n != 1 {
		t.Fatalf("unhealthy audit rows = %d, want 1", n)
	}
	// wallet locked
	var locked bool
	r.pool.QueryRow(ctx, `SELECT locked FROM tenant_wallets WHERE tenant_id=1`).Scan(&locked)
	if !locked {
		t.Fatal("wallet should be locked after drift with autoLock")
	}
	// notified
	if notified != 1 {
		t.Fatalf("notify tenant = %d, want 1", notified)
	}
}
