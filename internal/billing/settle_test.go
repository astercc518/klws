// internal/billing/settle_test.go
package billing

import (
	"context"
	"testing"
)

func chargeState(t *testing.T, ctx context.Context, r *Repo, tenantID int64, msgID string) string {
	t.Helper()
	var s string
	if err := r.pool.QueryRow(ctx, `SELECT state::text FROM billing_charges WHERE tenant_id=$1 AND message_id=$2`, tenantID, msgID).Scan(&s); err != nil {
		t.Fatalf("charge state: %v", err)
	}
	return s
}

func TestSettle_ConsumesFrozen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := newRepoWithSchema(t)
	seedWallet(t, ctx, pool, 1, 1000)
	if _, err := r.Hold(ctx, hReq(1, "m1", 300)); err != nil {
		t.Fatalf("hold: %v", err)
	}

	if err := r.Settle(ctx, 1, "m1"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if st := chargeState(t, ctx, r, 1, "m1"); st != "settled" {
		t.Fatalf("charge state = %q, want settled", st)
	}
	bal, frz := walletState(t, ctx, pool, 1)
	if bal != 700 || frz != 0 {
		t.Fatalf("after settle bal=%d frz=%d, want 700/0 (frozen consumed)", bal, frz)
	}
}

func TestSettle_IdempotentOnNonHeld(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := newRepoWithSchema(t)
	seedWallet(t, ctx, pool, 1, 1000)
	r.Hold(ctx, hReq(1, "m1", 300))
	if err := r.Settle(ctx, 1, "m1"); err != nil {
		t.Fatalf("settle 1: %v", err)
	}
	// second settle is a no-op (charge already 'settled'), must not error or move money
	if err := r.Settle(ctx, 1, "m1"); err != nil {
		t.Fatalf("settle 2: %v", err)
	}
	bal, frz := walletState(t, ctx, pool, 1)
	if bal != 700 || frz != 0 {
		t.Fatalf("after double settle bal=%d frz=%d, want 700/0", bal, frz)
	}
}
