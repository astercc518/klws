// internal/billing/refund_request_test.go
package billing

import (
	"testing"
)

func TestRequestRefund_PendingFundsStayFrozen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := newRepoWithSchema(t)
	seedWallet(t, ctx, pool, 1, 1000)
	r.Hold(ctx, hReq(1, "m1", 300))

	if err := r.RequestRefund(ctx, 1, "m1", "send failed"); err != nil {
		t.Fatalf("request refund: %v", err)
	}
	if st := chargeState(t, ctx, r, 1, "m1"); st != "refund_pending" {
		t.Fatalf("charge state = %q, want refund_pending", st)
	}
	// funds must remain frozen (NOT returned to balance)
	bal, frz := walletState(t, ctx, pool, 1)
	if bal != 700 || frz != 300 {
		t.Fatalf("after request refund bal=%d frz=%d, want 700/300 (still frozen)", bal, frz)
	}
	// a pending refund_request exists
	var rs string
	pool.QueryRow(ctx, `SELECT state::text FROM refund_requests WHERE tenant_id=1 AND amount=300`).Scan(&rs)
	if rs != "pending" {
		t.Fatalf("refund_request state = %q, want pending", rs)
	}
}

func TestRequestRefund_IdempotentOnNonHeld(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := newRepoWithSchema(t)
	seedWallet(t, ctx, pool, 1, 1000)
	r.Hold(ctx, hReq(1, "m1", 300))
	if err := r.RequestRefund(ctx, 1, "m1", "fail"); err != nil {
		t.Fatalf("request 1: %v", err)
	}
	// second call: charge already refund_pending → no-op, single refund_request
	if err := r.RequestRefund(ctx, 1, "m1", "fail again"); err != nil {
		t.Fatalf("request 2: %v", err)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM refund_requests WHERE tenant_id=1`).Scan(&n)
	if n != 1 {
		t.Fatalf("refund_requests count = %d, want 1", n)
	}
}
