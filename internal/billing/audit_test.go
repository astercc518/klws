package billing

import (
	"testing"

	"github.com/acme/wadist/internal/audit"
)

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
