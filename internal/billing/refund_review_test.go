// internal/billing/refund_review_test.go
package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func pendingRefundID(t *testing.T, ctx context.Context, r *Repo, tenantID int64) int64 {
	t.Helper()
	var id int64
	if err := r.pool.QueryRow(ctx, `SELECT id FROM refund_requests WHERE tenant_id=$1 AND state='pending'`, tenantID).Scan(&id); err != nil {
		t.Fatalf("pending refund id: %v", err)
	}
	return id
}

func setupPendingRefund(t *testing.T) (*Repo, context.Context, *pgxpool.Pool) {
	t.Helper()
	r, ctx, pool := newRepoWithSchema(t)
	seedWallet(t, ctx, pool, 1, 1000)
	r.Hold(ctx, hReq(1, "m1", 300))
	if err := r.RequestRefund(ctx, 1, "m1", "fail"); err != nil {
		t.Fatalf("request refund: %v", err)
	}
	return r, ctx, pool
}

func TestApproveRefund_ReturnsFundsToBalance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := setupPendingRefund(t)
	rid := pendingRefundID(t, ctx, r, 1)

	if err := r.ApproveRefund(ctx, rid, 99, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if st := chargeState(t, ctx, r, 1, "m1"); st != "refunded" {
		t.Fatalf("charge state = %q, want refunded", st)
	}
	bal, frz := walletState(t, ctx, pool, 1)
	if bal != 1000 || frz != 0 {
		t.Fatalf("after approve bal=%d frz=%d, want 1000/0 (returned)", bal, frz)
	}
}

func TestRejectRefund_ConsumesFrozen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := setupPendingRefund(t)
	rid := pendingRefundID(t, ctx, r, 1)

	if err := r.RejectRefund(ctx, rid, 99, "abuse"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if st := chargeState(t, ctx, r, 1, "m1"); st != "rejected" {
		t.Fatalf("charge state = %q, want rejected", st)
	}
	bal, frz := walletState(t, ctx, pool, 1)
	if bal != 700 || frz != 0 {
		t.Fatalf("after reject bal=%d frz=%d, want 700/0 (consumed)", bal, frz)
	}
}

func TestReviewRefund_DoubleReviewConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, _ := setupPendingRefund(t)
	rid := pendingRefundID(t, ctx, r, 1)
	if err := r.ApproveRefund(ctx, rid, 99, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// second review of the same refund must conflict (already approved)
	if err := r.RejectRefund(ctx, rid, 99, "late"); !errors.Is(err, ErrRefundConflict) {
		t.Fatalf("second review err = %v, want ErrRefundConflict", err)
	}
}
