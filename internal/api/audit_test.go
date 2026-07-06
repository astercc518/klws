package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestToAuditEntry(t *testing.T) {
	e := toAuditEntry(auditEvent{
		TenantID: 7, ActorID: 3, Action: "finance.topup",
		ResourceType: "tenant", ResourceID: 7,
		Details: map[string]any{"amount": 100, "ref": "x"},
	})
	if e.TenantID != 7 || e.ActorID != 3 || e.ResourceID != 7 {
		t.Errorf("ids not passed through: %+v", e)
	}
	if e.Action != "finance.topup" || e.ResourceType != "tenant" {
		t.Errorf("action/resource_type wrong: %+v", e)
	}
	var m map[string]any
	if err := json.Unmarshal(e.Details, &m); err != nil || m["ref"] != "x" {
		t.Errorf("details json = %s (err %v)", e.Details, err)
	}

	// nil Details → nil bytes (AuditWriter maps nil → SQL NULL).
	e = toAuditEntry(auditEvent{Action: "user.password_reset"})
	if e.Details != nil {
		t.Errorf("nil details should stay nil, got %s", e.Details)
	}
}

func TestBuildAuditWhere_empty(t *testing.T) {
	where, args := buildAuditWhere(auditFilter{})
	if where != "" || len(args) != 0 {
		t.Errorf("empty filter: where=%q args=%v", where, args)
	}
}

func TestBuildAuditWhere_filters(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	where, args := buildAuditWhere(auditFilter{Action: "finance.topup", ActorID: 3, Since: since})
	// three conditions, joined by AND, params $1..$3 in add-order
	if !strings.Contains(where, "a.action = $1") ||
		!strings.Contains(where, "a.actor_id = $2") ||
		!strings.Contains(where, "a.occurred_at >= $3") {
		t.Errorf("where missing conditions: %q", where)
	}
	if strings.Count(where, " AND ") != 2 {
		t.Errorf("want 2 ANDs, got %q", where)
	}
	if len(args) != 3 || args[0] != "finance.topup" || args[1] != int64(3) || args[2] != since {
		t.Errorf("args = %v", args)
	}
}

func TestBuildAuditListSQL_clampAndPaging(t *testing.T) {
	// limit 0 → 100; offset default 0; limit/offset are the last two args.
	q, args := buildAuditListSQL(auditFilter{})
	if !strings.Contains(q, "ORDER BY a.id DESC") {
		t.Errorf("missing order: %q", q)
	}
	if len(args) != 2 || args[0] != 100 || args[1] != 0 {
		t.Errorf("default limit/offset args = %v", args)
	}
	// limit > 500 → 500
	_, args = buildAuditListSQL(auditFilter{Limit: 999, Offset: 40})
	if args[0] != 500 || args[1] != 40 {
		t.Errorf("clamp args = %v", args)
	}
	// with a filter, limit/offset params come after the filter param
	q, args = buildAuditListSQL(auditFilter{Action: "x", Limit: 10})
	if !strings.Contains(q, "LIMIT $2 OFFSET $3") {
		t.Errorf("param numbering wrong: %q", q)
	}
	if len(args) != 3 || args[0] != "x" || args[1] != 10 || args[2] != 0 {
		t.Errorf("args = %v", args)
	}
}

func TestBuildAuditCountSQL(t *testing.T) {
	q, args := buildAuditCountSQL(auditFilter{TenantID: 9})
	if !strings.HasPrefix(q, "SELECT count(*) FROM audit_log a") || !strings.Contains(q, "a.tenant_id = $1") {
		t.Errorf("count sql = %q", q)
	}
	if len(args) != 1 || args[0] != int64(9) {
		t.Errorf("args = %v", args)
	}
}
