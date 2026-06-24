// Package audit provides append-only audit logging.
package audit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditEntry describes a single audit event to record.
type AuditEntry struct {
	TenantID     int64
	ActorID      int64
	ResourceID   int64
	Action       string
	ResourceType string
	Details      []byte // raw JSON; nil → NULL
}

// AuditWriter inserts rows into audit_log. The pool must have SELECT+INSERT
// on audit_log (app_system or superuser); it does not need to be literally
// superuser, but must not be a role that only holds SELECT (e.g. app_tenant
// without the explicit INSERT grant).
type AuditWriter struct {
	pool *pgxpool.Pool
}

// NewAuditWriter constructs an AuditWriter backed by pool.
func NewAuditWriter(pool *pgxpool.Pool) *AuditWriter {
	return &AuditWriter{pool: pool}
}

const insertAudit = `
INSERT INTO audit_log (tenant_id, actor_id, action, resource_type, resource_id, details)
VALUES ($1, $2, $3, $4, $5, $6)`

// Record inserts an audit entry. Details nil → SQL NULL.
func (w *AuditWriter) Record(ctx context.Context, e AuditEntry) error {
	var details interface{}
	if e.Details != nil {
		details = e.Details
	}
	var tenantID, actorID, resourceID interface{}
	if e.TenantID != 0 {
		tenantID = e.TenantID
	}
	if e.ActorID != 0 {
		actorID = e.ActorID
	}
	if e.ResourceID != 0 {
		resourceID = e.ResourceID
	}
	_, err := w.pool.Exec(ctx, insertAudit,
		tenantID, actorID, e.Action, nullableStr(e.ResourceType), resourceID, details)
	if err != nil {
		return fmt.Errorf("audit: record: %w", err)
	}
	return nil
}

// RecordTx inserts an audit entry within an existing transaction (same-tx atomicity).
func (w *AuditWriter) RecordTx(ctx context.Context, tx pgx.Tx, e AuditEntry) error {
	var details interface{}
	if e.Details != nil {
		details = e.Details
	}
	var tenantID, actorID, resourceID interface{}
	if e.TenantID != 0 {
		tenantID = e.TenantID
	}
	if e.ActorID != 0 {
		actorID = e.ActorID
	}
	if e.ResourceID != 0 {
		resourceID = e.ResourceID
	}
	_, err := tx.Exec(ctx, insertAudit,
		tenantID, actorID, e.Action, nullableStr(e.ResourceType), resourceID, details)
	if err != nil {
		return fmt.Errorf("audit: record tx: %w", err)
	}
	return nil
}

func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
