// internal/billing/topup_test.go
package billing

import (
	"context"
	"testing"
)

func ledgerSum(t *testing.T, ctx context.Context, r *Repo, tenantID int64) (sumBal, sumFrz int64) {
	t.Helper()
	if err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(delta_balance),0), COALESCE(SUM(delta_frozen),0) FROM wallet_ledger WHERE tenant_id=$1`,
		tenantID).Scan(&sumBal, &sumFrz); err != nil {
		t.Fatalf("ledger sum: %v", err)
	}
	return
}

func TestTopup_CreditsAndLedgers(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := newRepoWithSchema(t)

	if err := r.Topup(ctx, 1, 1000, "pay-1"); err != nil {
		t.Fatalf("topup: %v", err)
	}
	bal, frz := walletState(t, ctx, pool, 1)
	if bal != 1000 || frz != 0 {
		t.Fatalf("after topup bal=%d frz=%d, want 1000/0", bal, frz)
	}
	// ledger reflects the credit so balance = Σdelta_balance holds
	sb, sf := ledgerSum(t, ctx, r, 1)
	if sb != 1000 || sf != 0 {
		t.Fatalf("ledger sum bal=%d frz=%d, want 1000/0", sb, sf)
	}
}

func TestTopup_IdempotentOnRef(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := newRepoWithSchema(t)
	if err := r.Topup(ctx, 1, 1000, "pay-1"); err != nil {
		t.Fatalf("topup 1: %v", err)
	}
	if err := r.Topup(ctx, 1, 1000, "pay-1"); err != nil { // same ref
		t.Fatalf("topup 2: %v", err)
	}
	bal, _ := walletState(t, ctx, pool, 1)
	if bal != 1000 {
		t.Fatalf("after dup topup bal=%d, want 1000 (no double credit)", bal)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger WHERE idem_key='topup:pay-1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("topup ledger rows = %d, want 1", n)
	}
}

func TestTopup_RejectsNonPositive(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, _ := newRepoWithSchema(t)
	if err := r.Topup(ctx, 1, 0, "z"); err == nil {
		t.Fatal("expected error for non-positive topup")
	}
}
