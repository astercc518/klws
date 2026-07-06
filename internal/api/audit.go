package api

import (
	"context"
	"encoding/json"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/audit"
)

// auditEvent is the api-layer convenience shape for one audit row. Details is
// any JSON-serializable value; it is marshaled by toAuditEntry.
type auditEvent struct {
	TenantID     int64
	ActorID      int64
	Action       string
	ResourceType string
	ResourceID   int64
	Details      any
}

// actorID returns the acting session user id, or 0 when unauthenticated.
func actorID(c *gin.Context) int64 {
	if sd := sessionFrom(c); sd != nil {
		return sd.UserID
	}
	return 0
}

// toAuditEntry converts an auditEvent to the audit package's AuditEntry,
// marshaling Details to JSON bytes (nil / marshal-error → nil → SQL NULL). The
// AuditWriter maps zero ids and empty resource_type to NULL.
func toAuditEntry(e auditEvent) audit.AuditEntry {
	var details []byte
	if e.Details != nil {
		if b, err := json.Marshal(e.Details); err == nil {
			details = b
		}
	}
	return audit.AuditEntry{
		TenantID:     e.TenantID,
		ActorID:      e.ActorID,
		ResourceID:   e.ResourceID,
		Action:       e.Action,
		ResourceType: e.ResourceType,
		Details:      details,
	}
}

// systemPool returns the BYPASSRLS pool for the audit READ endpoint. sysPool is
// a test-only override.
func (s *Server) systemPool() *pgxpool.Pool {
	if s.sysPool != nil {
		return s.sysPool
	}
	return s.deps.Mgr.SystemPool()
}

// recordAudit appends best-effort AFTER the action committed, via the shared
// AuditWriter. On failure it logs and returns — it never fails the caller's
// already-committed action. No-op if no writer is wired.
func (s *Server) recordAudit(ctx context.Context, e auditEvent) {
	if s.deps.Audit == nil {
		return
	}
	if err := s.deps.Audit.Record(ctx, toAuditEntry(e)); err != nil {
		log.Printf("[audit] record %s: %v", e.Action, err)
	}
}

// recordAuditTx appends within an existing tx (atomic with the action) via the
// shared AuditWriter. Returns the error so the caller can roll back. No-op (nil)
// if no writer is wired.
func (s *Server) recordAuditTx(ctx context.Context, tx pgx.Tx, e auditEvent) error {
	if s.deps.Audit == nil {
		return nil
	}
	return s.deps.Audit.RecordTx(ctx, tx, toAuditEntry(e))
}
