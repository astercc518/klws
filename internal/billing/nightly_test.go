// internal/billing/nightly_test.go
package billing

import (
	"context"
	"testing"
)

func TestRunNightlyReconciliation_HandlesAllDrifts(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, _ := newRepoWithSchema(t)
	r.Topup(ctx, 1, 1000, "s1") // healthy
	r.Topup(ctx, 2, 500, "s2")
	r.pool.Exec(ctx, `UPDATE tenant_wallets SET balance=balance+7 WHERE tenant_id=2`) // drift
	r.Topup(ctx, 3, 200, "s3")
	r.pool.Exec(ctx, `UPDATE tenant_wallets SET balance=balance+9 WHERE tenant_id=3`) // drift

	var handled int
	h := NewDriftHandler(r, false, func(_ context.Context, _ Report) { handled++ })
	n, err := RunNightlyReconciliation(ctx, r, h)
	if err != nil {
		t.Fatalf("nightly: %v", err)
	}
	if n != 2 || handled != 2 {
		t.Fatalf("drifted=%d handled=%d, want 2/2", n, handled)
	}
}
