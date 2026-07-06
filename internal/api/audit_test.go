package api

import (
	"encoding/json"
	"testing"
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
