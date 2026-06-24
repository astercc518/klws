package billing

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/audit"
)

// refundState returns the current state column of a refund_requests row.
func refundState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, rid int64) string {
	t.Helper()
	var state string
	if err := pool.QueryRow(ctx,
		`SELECT state FROM refund_requests WHERE id=$1`, rid).Scan(&state); err != nil {
		t.Fatalf("refundState: %v", err)
	}
	return state
}

func TestApproveRefund_AuditRowWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := setupPendingRefund(t)
	rid := pendingRefundID(t, ctx, r, 1)

	aw := audit.NewAuditWriter(pool)
	r.UseAudit(aw)

	if err := r.ApproveRefund(ctx, rid, 77, "approved by admin"); err != nil {
		t.Fatalf("ApproveRefund: %v", err)
	}

	// Verify audit_log has a 'refund.approve' row with correct actor_id.
	var action string
	var actorID, resourceID int64
	err := pool.QueryRow(ctx, `
		SELECT action, actor_id, resource_id FROM audit_log
		WHERE action='refund.approve'`).Scan(&action, &actorID, &resourceID)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if actorID != 77 {
		t.Fatalf("actor_id=%d, want 77", actorID)
	}
	if resourceID != rid {
		t.Fatalf("resource_id=%d, want %d", resourceID, rid)
	}
}

func TestRejectRefund_AuditRowWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := setupPendingRefund(t)
	rid := pendingRefundID(t, ctx, r, 1)

	aw := audit.NewAuditWriter(pool)
	r.UseAudit(aw)

	if err := r.RejectRefund(ctx, rid, 55, "rejected by admin"); err != nil {
		t.Fatalf("RejectRefund: %v", err)
	}

	var action string
	var actorID, resourceID int64
	err := pool.QueryRow(ctx, `
		SELECT action, actor_id, resource_id FROM audit_log
		WHERE action='refund.reject'`).Scan(&action, &actorID, &resourceID)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if actorID != 55 {
		t.Fatalf("actor_id=%d, want 55", actorID)
	}
	if resourceID != rid {
		t.Fatalf("resource_id=%d, want %d", resourceID, rid)
	}
}

func TestApproveRefund_NoAuditWriter_StillWorks(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	// Existing behavior: no UseAudit call → zero regression.
	r, ctx, pool := setupPendingRefund(t)
	rid := pendingRefundID(t, ctx, r, 1)

	// Do NOT call r.UseAudit(...)
	if err := r.ApproveRefund(ctx, rid, 99, "ok"); err != nil {
		t.Fatalf("ApproveRefund without audit: %v", err)
	}

	// Verify business effect is still correct.
	bal, frz := walletState(t, ctx, pool, 1)
	if bal != 1000 || frz != 0 {
		t.Fatalf("bal=%d frz=%d, want 1000/0", bal, frz)
	}

	// No audit_log rows.
	var count int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 audit_log rows without UseAudit, got %d", count)
	}
}

func TestApproveRefund_AuditAtomicWithRefund(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	// Verify that the audit row and the business update are in the same transaction
	// by checking both are visible after the commit.
	r, ctx, pool := setupPendingRefund(t)
	rid := pendingRefundID(t, ctx, r, 1)

	aw := audit.NewAuditWriter(pool)
	r.UseAudit(aw)

	if err := r.ApproveRefund(ctx, rid, 11, "atomic test"); err != nil {
		t.Fatalf("ApproveRefund: %v", err)
	}

	// Both the refund state and audit row should be committed together.
	var refState string
	pool.QueryRow(ctx, `SELECT state FROM refund_requests WHERE id=$1`, rid).Scan(&refState)
	if refState != "approved" {
		t.Fatalf("refund state=%q, want approved", refState)
	}

	var auditCount int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='refund.approve' AND actor_id=11`).Scan(&auditCount)
	if auditCount != 1 {
		t.Fatalf("audit_log rows=%d, want 1", auditCount)
	}

}

// TestApproveRefund_AuditFailureRollsBackRefund proves that when the in-tx audit
// INSERT fails (here: by dropping audit_log so the INSERT errors), the entire
// transaction is rolled back — the refund_requests row stays 'pending' and the
// charge state is unchanged.
//
// Poison strategy: DROP TABLE audit_log via the superuser pool right before
// calling ApproveRefund. The audit INSERT inside reviewRefund's pgx.BeginTxFunc
// then errors, causing BeginTxFunc to roll back the whole transaction. We
// recreate audit_log afterwards to leave the schema intact.
func TestApproveRefund_AuditFailureRollsBackRefund(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := setupPendingRefund(t)
	rid := pendingRefundID(t, ctx, r, 1)

	aw := audit.NewAuditWriter(pool)
	r.UseAudit(aw)

	// Record charge state before the poisoned call.
	chargeStateBefore := chargeState(t, ctx, r, 1, "m1")
	if chargeStateBefore != "refund_pending" {
		t.Fatalf("precondition: charge state=%q, want refund_pending", chargeStateBefore)
	}
	refundStateBefore := refundState(t, ctx, pool, rid)
	if refundStateBefore != "pending" {
		t.Fatalf("precondition: refund state=%q, want pending", refundStateBefore)
	}

	// Poison: drop audit_log so the in-tx INSERT into audit_log fails.
	if _, err := pool.Exec(ctx, `DROP TABLE audit_log`); err != nil {
		t.Fatalf("drop audit_log: %v", err)
	}
	// Restore at end of test so other subtests/teardown are unaffected.
	t.Cleanup(func() {
		pool.Exec(ctx, `
			CREATE TABLE IF NOT EXISTS audit_log (
				id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
				occurred_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
				tenant_id     BIGINT,
				actor_id      BIGINT,
				action        TEXT NOT NULL,
				resource_type TEXT,
				resource_id   BIGINT,
				details       JSONB
			)`)
	})

	// (a) ApproveRefund must return an error.
	err := r.ApproveRefund(ctx, rid, 42, "should rollback")
	if err == nil {
		t.Fatal("expected ApproveRefund to fail when audit_log is missing, but it succeeded")
	}
	t.Logf("ApproveRefund correctly returned error: %v", err)

	// (b) refund_requests.state must still be 'pending' (rolled back, not 'approved').
	refundStateAfter := refundState(t, ctx, pool, rid)
	if refundStateAfter != "pending" {
		t.Fatalf("refund state after failed audit = %q, want pending (rollback proof)", refundStateAfter)
	}

	// (c) charge state must be unchanged (still 'refund_pending').
	chargeStateAfter := chargeState(t, ctx, r, 1, "m1")
	if chargeStateAfter != "refund_pending" {
		t.Fatalf("charge state after failed audit = %q, want refund_pending (rollback proof)", chargeStateAfter)
	}
}

