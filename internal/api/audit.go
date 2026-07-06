package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

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

// auditFilter is the parsed, validated query for the audit list endpoint.
type auditFilter struct {
	Action   string
	ActorID  int64
	TenantID int64
	Since    time.Time // zero = unset
	Until    time.Time // zero = unset
	Limit    int
	Offset   int
}

const auditSelectBase = `
SELECT a.id, a.occurred_at::text, a.tenant_id, t.name, a.actor_id, u.email,
       a.action, a.resource_type, a.resource_id, a.details
  FROM audit_log a
  LEFT JOIN tenants t        ON t.id = a.tenant_id
  LEFT JOIN console_users u  ON u.id = a.actor_id`

// buildAuditWhere returns the " WHERE ..." clause (or "") and its positional args.
func buildAuditWhere(f auditFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(tmpl string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(tmpl, len(args)))
	}
	if f.Action != "" {
		add("a.action = $%d", f.Action)
	}
	if f.ActorID != 0 {
		add("a.actor_id = $%d", f.ActorID)
	}
	if f.TenantID != 0 {
		add("a.tenant_id = $%d", f.TenantID)
	}
	if !f.Since.IsZero() {
		add("a.occurred_at >= $%d", f.Since)
	}
	if !f.Until.IsZero() {
		add("a.occurred_at <= $%d", f.Until)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func clampLimit(n int) int {
	if n <= 0 {
		return 100
	}
	if n > 500 {
		return 500
	}
	return n
}

// buildAuditListSQL returns the full ordered/paginated list query and its args.
func buildAuditListSQL(f auditFilter) (string, []any) {
	where, args := buildAuditWhere(f)
	off := f.Offset
	if off < 0 {
		off = 0
	}
	args = append(args, clampLimit(f.Limit), off)
	q := auditSelectBase + where +
		fmt.Sprintf(" ORDER BY a.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	return q, args
}

// buildAuditCountSQL returns the total-count query for the same filter.
func buildAuditCountSQL(f auditFilter) (string, []any) {
	where, args := buildAuditWhere(f)
	return "SELECT count(*) FROM audit_log a" + where, args
}
