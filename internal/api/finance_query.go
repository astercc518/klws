package api

import (
	"fmt"
	"strconv"
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

// billRange is a validated [From, To) window in Asia/Shanghai. To is exclusive.
type billRange struct {
	From time.Time
	To   time.Time
}

var cnLoc = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600) // fallback: fixed +08:00
	}
	return loc
}()

// parseBillRange parses YYYY-MM-DD `from`/`to` as Asia/Shanghai day bounds.
// `to` is inclusive of that whole day, so the returned To is start-of-next-day.
// Empty strings default to the last 30 days. Rejects reversed / >366d ranges.
func parseBillRange(fromStr, toStr string) (billRange, error) {
	const layout = "2006-01-02"
	// Anchor "now" to CN start-of-tomorrow so the default window is stable
	// within a day; we derive it from the parsed `to` when supplied.
	var to time.Time
	if toStr == "" {
		now := time.Now().In(cnLoc)
		to = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, cnLoc).AddDate(0, 0, 1)
	} else {
		d, err := time.ParseInLocation(layout, toStr, cnLoc)
		if err != nil {
			return billRange{}, fmt.Errorf("bad `to` date: %w", err)
		}
		to = d.AddDate(0, 0, 1) // inclusive → exclusive next-day bound
	}

	var from time.Time
	if fromStr == "" {
		from = to.AddDate(0, 0, -30)
	} else {
		d, err := time.ParseInLocation(layout, fromStr, cnLoc)
		if err != nil {
			return billRange{}, fmt.Errorf("bad `from` date: %w", err)
		}
		from = d
	}

	if !from.Before(to) {
		return billRange{}, fmt.Errorf("`from` must be before `to`")
	}
	if to.Sub(from) > 366*24*time.Hour {
		return billRange{}, fmt.Errorf("range too large (max 366 days)")
	}
	return billRange{From: from, To: to}, nil
}

// campaignFilter is the parsed query for the admin campaign list.
type campaignFilter struct {
	Q      string
	State  string
	Limit  int
	Offset int
}

// buildCampaignWhere returns the clause for campaigns "c" joined to tenants "t".
func buildCampaignWhere(f campaignFilter) (string, []any) {
	var conds []string
	var args []any
	if f.State != "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("c.state::text = $%d", len(args)))
	}
	if f.Q != "" {
		if id, err := strconv.ParseInt(f.Q, 10, 64); err == nil {
			args = append(args, id)
			conds = append(conds, fmt.Sprintf("(c.id = $%d OR c.tenant_id = $%d)", len(args), len(args)))
		} else {
			args = append(args, "%"+f.Q+"%")
			conds = append(conds, fmt.Sprintf("t.name ILIKE $%d", len(args)))
		}
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
