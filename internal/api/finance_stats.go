package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
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

type tenantBill struct {
	TenantID int64  `json:"tenant_id"`
	Name     string `json:"name"`
	Opening  int64  `json:"opening"`
	Closing  int64  `json:"closing"`
	Summary  struct {
		Topup  int64 `json:"topup"`
		Settle int64 `json:"settle"`
		Refund int64 `json:"refund"`
		Adjust int64 `json:"adjust"`
	} `json:"summary"`
	Daily []dailyPoint `json:"daily"`
}

// balanceAsOf returns balance_after+frozen_after of the last ledger row for the
// tenant strictly before `before` (0 if none).
func (s *Server) balanceAsOf(ctx context.Context, tenantID int64, before time.Time) (int64, error) {
	var v int64
	err := s.systemPool().QueryRow(ctx, `
SELECT COALESCE(balance_after + frozen_after, 0)
  FROM wallet_ledger
 WHERE tenant_id = $1 AND created_at < $2
 ORDER BY id DESC LIMIT 1`, tenantID, before).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

func (s *Server) handleAdminFinanceBill(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, err := strconv.ParseInt(c.Query("tenant_id"), 10, 64)
	if err != nil || tenantID <= 0 {
		fail(c, http.StatusBadRequest, "tenant_id required")
		return
	}
	r, err := parseBillRange(c.Query("from"), c.Query("to"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	pool := s.systemPool()
	var bill tenantBill
	bill.TenantID = tenantID
	bill.Daily = []dailyPoint{}

	_ = pool.QueryRow(ctx, `SELECT COALESCE(name,'') FROM tenants WHERE id=$1`, tenantID).Scan(&bill.Name)

	bill.Opening, err = s.balanceAsOf(ctx, tenantID, r.From)
	if err != nil {
		fail(c, http.StatusInternalServerError, "opening balance")
		return
	}
	closing, err := s.balanceAsOf(ctx, tenantID, r.To)
	if err != nil {
		fail(c, http.StatusInternalServerError, "closing balance")
		return
	}
	bill.Closing = closing

	err = pool.QueryRow(ctx, `
SELECT
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='topup'), 0),
  COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0),
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='refund'), 0),
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='adjust'), 0)
  FROM wallet_ledger l
 WHERE l.tenant_id=$1 AND l.created_at >= $2 AND l.created_at < $3`,
		tenantID, r.From, r.To).
		Scan(&bill.Summary.Topup, &bill.Summary.Settle, &bill.Summary.Refund, &bill.Summary.Adjust)
	if err != nil {
		fail(c, http.StatusInternalServerError, "bill summary")
		return
	}

	drows, err := pool.Query(ctx, `
SELECT to_char(date_trunc('day', l.created_at AT TIME ZONE 'Asia/Shanghai'), 'YYYY-MM-DD') AS day,
       COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='topup'), 0),
       COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0),
       COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='refund'), 0)
  FROM wallet_ledger l
 WHERE l.tenant_id=$1 AND l.created_at >= $2 AND l.created_at < $3
 GROUP BY day ORDER BY day`, tenantID, r.From, r.To)
	if err != nil {
		fail(c, http.StatusInternalServerError, "bill daily")
		return
	}
	defer drows.Close()
	for drows.Next() {
		var d dailyPoint
		if err := drows.Scan(&d.Day, &d.Topup, &d.Settle, &d.Refund); err != nil {
			fail(c, http.StatusInternalServerError, "scan bill daily")
			return
		}
		bill.Daily = append(bill.Daily, d)
	}
	if err := drows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate bill daily")
		return
	}

	ok(c, bill)
}

func (s *Server) handleAdminFinanceBillExport(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, err := strconv.ParseInt(c.Query("tenant_id"), 10, 64)
	if err != nil || tenantID <= 0 {
		fail(c, http.StatusBadRequest, "tenant_id required")
		return
	}
	r, err := parseBillRange(c.Query("from"), c.Query("to"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.systemPool().Query(ctx, `
SELECT l.id, l.created_at::text, l.tenant_id, COALESCE(t.name,''), l.kind::text,
       l.delta_balance, l.delta_frozen, l.balance_after, l.frozen_after
  FROM wallet_ledger l
  LEFT JOIN tenants t ON t.id = l.tenant_id
 WHERE l.tenant_id=$1 AND l.created_at >= $2 AND l.created_at < $3
 ORDER BY l.id`, tenantID, r.From, r.To)
	if err != nil {
		fail(c, http.StatusInternalServerError, "bill export query")
		return
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var id, tid, dbal, dfroz, bal, froz int64
		var created, name, kind string
		if err := rows.Scan(&id, &created, &tid, &name, &kind, &dbal, &dfroz, &bal, &froz); err != nil {
			fail(c, http.StatusInternalServerError, "scan bill export")
			return
		}
		out = append(out, []string{
			strconv.FormatInt(id, 10), created, strconv.FormatInt(tid, 10), name, kind,
			strconv.FormatInt(dbal, 10), strconv.FormatInt(dfroz, 10),
			strconv.FormatInt(bal, 10), strconv.FormatInt(froz, 10),
		})
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate bill export")
		return
	}
	s.recordAudit(ctx, auditEvent{
		TenantID: tenantID, ActorID: actorID(c), Action: "finance.bill_export",
		ResourceType: "tenant", ResourceID: tenantID,
		Details: gin.H{"from": c.Query("from"), "to": c.Query("to"), "rows": len(out)},
	})
	writeCSV(c, "bill-"+strconv.FormatInt(tenantID, 10)+".csv",
		[]string{"id", "created_at", "tenant_id", "tenant_name", "kind", "delta_balance", "delta_frozen", "balance_after", "frozen_after"},
		out)
}
