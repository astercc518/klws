package api

import (
	"fmt"
	"strings"
	"time"
)

// ledgerFilter is the parsed query for the ledger list/export endpoints.
type ledgerFilter struct {
	Kind     string
	TenantID int64
	From     time.Time // zero = unset (inclusive lower bound)
	To       time.Time // zero = unset (EXCLUSIVE upper bound — caller adds +1 day)
	Limit    int
	Offset   int
}

// buildLedgerWhere returns the " WHERE ..." clause (or "") and positional args
// for wallet_ledger aliased "l".
func buildLedgerWhere(f ledgerFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(tmpl string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(tmpl, len(args)))
	}
	if f.Kind != "" {
		add("l.kind::text = $%d", f.Kind)
	}
	if f.TenantID != 0 {
		add("l.tenant_id = $%d", f.TenantID)
	}
	if !f.From.IsZero() {
		add("l.created_at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("l.created_at < $%d", f.To)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
