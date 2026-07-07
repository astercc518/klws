package api

import (
	"testing"
	"time"
)

func TestBuildLedgerWhere(t *testing.T) {
	// empty filter → no WHERE
	if w, args := buildLedgerWhere(ledgerFilter{}); w != "" || len(args) != 0 {
		t.Errorf("empty: where=%q args=%v", w, args)
	}
	// kind + tenant
	w, args := buildLedgerWhere(ledgerFilter{Kind: "topup", TenantID: 7})
	if w != " WHERE l.kind::text = $1 AND l.tenant_id = $2" {
		t.Errorf("where = %q", w)
	}
	if len(args) != 2 || args[0] != "topup" || args[1] != int64(7) {
		t.Errorf("args = %v", args)
	}
	// date range uses >= from and < to
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	w, args = buildLedgerWhere(ledgerFilter{From: from, To: to})
	if w != " WHERE l.created_at >= $1 AND l.created_at < $2" {
		t.Errorf("range where = %q", w)
	}
	if len(args) != 2 {
		t.Errorf("range args = %v", args)
	}
}
