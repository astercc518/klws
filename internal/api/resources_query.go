package api

import (
	"fmt"
	"strings"
)

// clampPage returns a sane page size: def when n<=0, else n capped at max.
func clampPage(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// --- devices ---

type deviceFilter struct {
	Q         string
	BanStatus string
	Online    *bool
	Limit     int
	Offset    int
}

// buildDeviceWhere returns the " WHERE ..." clause (or "") and positional args
// for account_devices (unqualified columns). Ownership truth lives in Redis
// (owner:{jid}) now, not a PG column — ownedJIDs is the caller's current
// Mgr.OwnedJIDs(ctx) snapshot, only consulted when f.Online is set.
func buildDeviceWhere(f deviceFilter, ownedJIDs []string) (string, []any) {
	var conds []string
	var args []any
	if f.Q != "" {
		args = append(args, "%"+f.Q+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf("(account_jid ILIKE $%d OR phone_number ILIKE $%d OR array_to_string(tags,' ') ILIKE $%d)", n, n, n))
	}
	if f.BanStatus != "" {
		args = append(args, f.BanStatus)
		conds = append(conds, fmt.Sprintf("ban_status::text = $%d", len(args)))
	}
	if f.Online != nil {
		if ownedJIDs == nil {
			ownedJIDs = []string{} // nil → SQL NULL, and ANY(NULL)/NOT(NULL) are both NULL (matches nothing); force empty text[] instead
		}
		args = append(args, ownedJIDs) // pgx 支持 []string → text[]
		n := len(args)
		if *f.Online {
			conds = append(conds, fmt.Sprintf("account_jid = ANY($%d)", n))
		} else {
			conds = append(conds, fmt.Sprintf("NOT (account_jid = ANY($%d))", n))
		}
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// --- proxies ---

type proxyFilter struct {
	Q         string
	ProxyType string
	Alive     *bool
	Limit     int
	Offset    int
}

// buildProxyWhere returns the " WHERE ..." clause (or "") and args for
// proxy_pool (aliased "p").
func buildProxyWhere(f proxyFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Q != "" {
		args = append(args, "%"+f.Q+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf("(p.proxy_url ILIKE $%d OR p.country_code ILIKE $%d)", n, n))
	}
	if f.Alive != nil {
		args = append(args, *f.Alive)
		conds = append(conds, fmt.Sprintf("p.is_alive = $%d", len(args)))
	}
	if f.ProxyType != "" {
		args = append(args, f.ProxyType)
		conds = append(conds, fmt.Sprintf("p.proxy_type::text = $%d", len(args)))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
