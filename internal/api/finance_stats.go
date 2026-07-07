package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type dailyPoint struct {
	Day    string `json:"day"`
	Topup  int64  `json:"topup"`
	Settle int64  `json:"settle"`
	Refund int64  `json:"refund"`
}
type tenantSpend struct {
	TenantID int64  `json:"tenant_id"`
	Name     string `json:"name"`
	Settle   int64  `json:"settle"`
	Topup    int64  `json:"topup"`
}
type financeStats struct {
	Summary struct {
		Topup  int64 `json:"topup"`
		Settle int64 `json:"settle"`
		Refund int64 `json:"refund"`
		Adjust int64 `json:"adjust"`
		Net    int64 `json:"net"`
	} `json:"summary"`
	Daily      []dailyPoint  `json:"daily"`
	TopTenants []tenantSpend `json:"top_tenants"`
}

// net movement of a ledger row = delta_balance + delta_frozen.
// settle is stored negative; we report 消耗 as a positive magnitude.
const netExpr = "(l.delta_balance + l.delta_frozen)"

// handleAdminFinanceStats: GET /admin/finance/stats?from&to — platform totals,
// daily series, and tenant spend ranking, all from wallet_ledger.
func (s *Server) handleAdminFinanceStats(c *gin.Context) {
	ctx := c.Request.Context()
	r, err := parseBillRange(c.Query("from"), c.Query("to"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	pool := s.systemPool()
	var out financeStats
	out.Daily = []dailyPoint{}
	out.TopTenants = []tenantSpend{}

	// Summary (single scan of the window).
	err = pool.QueryRow(ctx, `
SELECT
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='topup'), 0),
  COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0),
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='refund'), 0),
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='adjust'), 0)
  FROM wallet_ledger l
 WHERE l.created_at >= $1 AND l.created_at < $2`, r.From, r.To).
		Scan(&out.Summary.Topup, &out.Summary.Settle, &out.Summary.Refund, &out.Summary.Adjust)
	if err != nil {
		fail(c, http.StatusInternalServerError, "stats summary")
		return
	}
	out.Summary.Net = out.Summary.Topup - out.Summary.Settle + out.Summary.Refund + out.Summary.Adjust

	// Daily series (CN days).
	drows, err := pool.Query(ctx, `
SELECT to_char(date_trunc('day', l.created_at AT TIME ZONE 'Asia/Shanghai'), 'YYYY-MM-DD') AS day,
       COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='topup'), 0),
       COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0),
       COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='refund'), 0)
  FROM wallet_ledger l
 WHERE l.created_at >= $1 AND l.created_at < $2
 GROUP BY day ORDER BY day`, r.From, r.To)
	if err != nil {
		fail(c, http.StatusInternalServerError, "stats daily")
		return
	}
	defer drows.Close()
	for drows.Next() {
		var d dailyPoint
		if err := drows.Scan(&d.Day, &d.Topup, &d.Settle, &d.Refund); err != nil {
			fail(c, http.StatusInternalServerError, "scan daily")
			return
		}
		out.Daily = append(out.Daily, d)
	}
	if err := drows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate daily")
		return
	}

	// Tenant spend ranking (top 20 by settle).
	trows, err := pool.Query(ctx, `
SELECT l.tenant_id, COALESCE(t.name, ''),
       COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0) AS settle,
       COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='topup'), 0) AS topup
  FROM wallet_ledger l
  LEFT JOIN tenants t ON t.id = l.tenant_id
 WHERE l.created_at >= $1 AND l.created_at < $2
 GROUP BY l.tenant_id, t.name
 ORDER BY settle DESC LIMIT 20`, r.From, r.To)
	if err != nil {
		fail(c, http.StatusInternalServerError, "stats tenants")
		return
	}
	defer trows.Close()
	for trows.Next() {
		var ts tenantSpend
		if err := trows.Scan(&ts.TenantID, &ts.Name, &ts.Settle, &ts.Topup); err != nil {
			fail(c, http.StatusInternalServerError, "scan tenant spend")
			return
		}
		out.TopTenants = append(out.TopTenants, ts)
	}
	if err := trows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate tenant spend")
		return
	}

	ok(c, out)
}
