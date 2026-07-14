// internal/api/metrics_api.go — P3 task-3: GET /admin/risk/overview + GET
// /admin/risk/accounts. Both are read-only REAL-TIME DB aggregations over
// account_devices / campaign_recipients (NOT the metric_snapshots table T1/T2
// populate) — the risk dashboard's live pane. SystemPool (BYPASSRLS): these
// are cross-tenant admin reads with no tenant request-context, same reasoning
// as internal/metrics.Sampler and handleAdminStats. No red-line package
// (billing/dispatch/store/sendgate/cluster) is touched.
package api

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// RiskHealthThreshold: health_score below this is the "low_health" anomaly
	// reason (spec §4) and also the health_buckets high/mid boundary — an
	// account just below the line that's otherwise fine still reads as "mid",
	// not "high".
	RiskHealthThreshold = 70
	// riskHealthLowMax is the health_buckets low/mid boundary: health_score <
	// riskHealthLowMax => low; [riskHealthLowMax, RiskHealthThreshold) => mid;
	// >= RiskHealthThreshold => high. Boundaries are inclusive-low per spec's
	// "0-40=low / 40-70=mid / 70-100=high" wording.
	riskHealthLowMax = 40
)

// ---------------------------------------------------------------------------
// GET /admin/risk/overview
// ---------------------------------------------------------------------------

type riskHealthBuckets struct {
	Low  int `json:"low"`
	Mid  int `json:"mid"`
	High int `json:"high"`
}

type riskOverview struct {
	BanRate             float64           `json:"ban_rate"`
	AccountsTotal       int               `json:"accounts_total"`
	AccountsActive      int               `json:"accounts_active"`
	AccountsQuarantined int               `json:"accounts_quarantined"`
	HealthBuckets       riskHealthBuckets `json:"health_buckets"`
	QueueBacklog        int               `json:"queue_backlog"`
	DeliveredRate1h     float64           `json:"delivered_rate_1h"`
	FailedRate1h        float64           `json:"failed_rate_1h"`
	AvgDeliveryMs       *int              `json:"avg_delivery_ms"`
	Processed1h         int               `json:"processed_1h"`
}

// handleAdminRiskOverview: GET /admin/risk/overview. Two aggregate queries —
// account_devices (counts + health buckets) and campaign_recipients (1h
// throughput, copied verbatim in shape from internal/metrics.Sampler.
// SampleOnce so the two "1h" pictures — the periodic snapshot and this live
// endpoint — never silently drift apart). Both queries are single round-trips
// with FILTER (WHERE ...); no per-row loop.
func (s *Server) handleAdminRiskOverview(c *gin.Context) {
	ctx := c.Request.Context()
	pool := s.systemPool()

	var ov riskOverview
	var accountsBanned int
	if err := pool.QueryRow(ctx, fmt.Sprintf(`
SELECT count(*),
       count(*) FILTER (WHERE ban_status='active'),
       count(*) FILTER (WHERE ban_status IN ('banned','flagged')),
       count(*) FILTER (WHERE quarantined_until > now()),
       count(*) FILTER (WHERE health_score < %d),
       count(*) FILTER (WHERE health_score >= %d AND health_score < %d),
       count(*) FILTER (WHERE health_score >= %d)
  FROM account_devices`, riskHealthLowMax, riskHealthLowMax, RiskHealthThreshold, RiskHealthThreshold),
	).Scan(&ov.AccountsTotal, &ov.AccountsActive, &accountsBanned, &ov.AccountsQuarantined,
		&ov.HealthBuckets.Low, &ov.HealthBuckets.Mid, &ov.HealthBuckets.High); err != nil {
		fail(c, http.StatusInternalServerError, "risk overview: accounts")
		return
	}
	if ov.AccountsTotal > 0 {
		ov.BanRate = float64(accountsBanned) / float64(ov.AccountsTotal)
	}

	// campaign_recipients has no created_at column (see sampler.go's doc
	// comment); avg_delivery_ms is computed against campaigns.created_at as a
	// tight proxy, same reasoning as the sampler.
	var delivered1h, failed1h int
	var avgMs *float64
	if err := pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE cr.state='pending'),
       count(*) FILTER (WHERE cr.state IN ('sent','failed','skipped') AND cr.updated_at > now() - interval '1 hour'),
       count(*) FILTER (WHERE cr.delivered_at > now() - interval '1 hour'),
       count(*) FILTER (WHERE cr.state='failed' AND cr.updated_at > now() - interval '1 hour'),
       avg(EXTRACT(EPOCH FROM (cr.delivered_at - c.created_at)) * 1000)
         FILTER (WHERE cr.delivered_at > now() - interval '1 hour')
  FROM campaign_recipients cr
  JOIN campaigns c ON c.id = cr.campaign_id`,
	).Scan(&ov.QueueBacklog, &ov.Processed1h, &delivered1h, &failed1h, &avgMs); err != nil {
		fail(c, http.StatusInternalServerError, "risk overview: recipients")
		return
	}
	if ov.Processed1h > 0 {
		ov.DeliveredRate1h = float64(delivered1h) / float64(ov.Processed1h)
		ov.FailedRate1h = float64(failed1h) / float64(ov.Processed1h)
	}
	if avgMs != nil {
		v := int(math.Round(*avgMs))
		ov.AvgDeliveryMs = &v
	}

	ok(c, ov)
}

// ---------------------------------------------------------------------------
// GET /admin/risk/accounts
// ---------------------------------------------------------------------------

// riskAccountRow is the JSON view of one anomalous account_devices row.
type riskAccountRow struct {
	JID              string  `json:"jid"`
	TenantID         int64   `json:"tenant_id"`
	TenantName       string  `json:"tenant_name"`
	BanStatus        string  `json:"ban_status"`
	HealthScore      int     `json:"health_score"`
	QuarantinedUntil *string `json:"quarantined_until"`
	Reason           string  `json:"reason"`
}

// riskAccountFilter is the parsed query for GET /admin/risk/accounts.
type riskAccountFilter struct {
	Reason string // "ban" | "quarantine" | "low_health"
	Q      string // ILIKE substring against account_jid
	Limit  int
	Offset int
}

func parseRiskAccountFilter(c *gin.Context) riskAccountFilter {
	f := riskAccountFilter{Reason: c.Query("reason"), Q: c.Query("q")}
	if n, err := strconv.Atoi(c.Query("limit")); err == nil {
		f.Limit = n
	}
	if n, err := strconv.Atoi(c.Query("offset")); err == nil && n > 0 {
		f.Offset = n
	}
	return f
}

// riskAccountBaseCTE is the shared "anomalous accounts + derived reason"
// projection every risk/accounts query (list, count, stats) builds on top of,
// so the reason-priority CASE logic lives in exactly one place instead of
// being duplicated (and risking drift) across list/filter/stats SQL. The
// base predicate is spec §4's anomaly definition:
//
//	ban_status IN ('banned','flagged') OR quarantined_until > now() OR health_score < RiskHealthThreshold
//
// reason priority (highest first, self-consistent and stable, brief leaves
// exact ranking to the implementer): ban > quarantine > low_health — an
// account that is simultaneously banned AND quarantined shows as "ban", the
// more actionable/severe state. The final ELSE branch is safe: the outer
// WHERE guarantees at least one of the three predicates holds, so if ban and
// quarantine are both false, health_score < RiskHealthThreshold must be true.
const riskAccountBaseCTE = `
WITH risk_base AS (
  SELECT ad.account_jid AS jid, ad.tenant_id, COALESCE(t.name,'') AS tenant_name,
         ad.ban_status::text AS ban_status, ad.health_score, ad.quarantined_until::text AS quarantined_until,
         CASE
           WHEN ad.ban_status IN ('banned','flagged') THEN 'ban'
           WHEN ad.quarantined_until > now() THEN 'quarantine'
           ELSE 'low_health'
         END AS reason
    FROM account_devices ad
    LEFT JOIN tenants t ON t.id = ad.tenant_id
   WHERE ad.ban_status IN ('banned','flagged')
      OR ad.quarantined_until > now()
      OR ad.health_score < %d
)
`

// buildRiskAccountWhere returns the " WHERE ..." clause (or "") and
// positional args to apply on top of risk_base — reason exact match + q
// ILIKE against jid. Mirrors buildInstanceWhere/buildDeviceWhere's shape.
func buildRiskAccountWhere(f riskAccountFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Reason != "" {
		args = append(args, f.Reason)
		conds = append(conds, fmt.Sprintf("reason = $%d", len(args)))
	}
	if f.Q != "" {
		args = append(args, "%"+f.Q+"%")
		conds = append(conds, fmt.Sprintf("jid ILIKE $%d", len(args)))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// handleAdminRiskAccounts: GET /admin/risk/accounts. Anomalous accounts only
// (ban_status IN(banned,flagged) OR quarantined_until>now() OR health_score <
// RiskHealthThreshold), with a derived "reason" column, reason/q filtering,
// pagination, and a reason→count stats breakdown — same {rows,total,stats}
// shape as store.Manager.ListInstances (P2). stats is computed over the full
// anomalous population, ignoring the current reason/q filter (same
// "unfiltered roll-up" behavior as ListInstances' state stats), so the UI can
// render reason tabs with correct counts regardless of which tab is active.
func (s *Server) handleAdminRiskAccounts(c *gin.Context) {
	ctx := c.Request.Context()
	pool := s.systemPool()
	f := parseRiskAccountFilter(c)
	base := fmt.Sprintf(riskAccountBaseCTE, RiskHealthThreshold)
	where, args := buildRiskAccountWhere(f)

	limit := clampPage(f.Limit, 20, 200)
	listArgs := append(append([]any{}, args...), limit, f.Offset)
	list := base + `
SELECT jid, tenant_id, tenant_name, ban_status, health_score, quarantined_until, reason
  FROM risk_base` + where +
		fmt.Sprintf(" ORDER BY jid LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := pool.Query(ctx, list, listArgs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list risk accounts")
		return
	}
	defer rows.Close()
	out := make([]riskAccountRow, 0)
	for rows.Next() {
		var r riskAccountRow
		var quarantinedUntil *string
		if err := rows.Scan(&r.JID, &r.TenantID, &r.TenantName, &r.BanStatus, &r.HealthScore, &quarantinedUntil, &r.Reason); err != nil {
			fail(c, http.StatusInternalServerError, "scan risk account")
			return
		}
		r.QuarantinedUntil = quarantinedUntil
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate risk accounts")
		return
	}

	var total int
	if err := pool.QueryRow(ctx, base+`SELECT count(*) FROM risk_base`+where, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count risk accounts")
		return
	}

	stats := make(map[string]int)
	srows, err := pool.Query(ctx, base+`SELECT reason, count(*) FROM risk_base GROUP BY reason`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "risk account stats")
		return
	}
	defer srows.Close()
	for srows.Next() {
		var reason string
		var n int
		if err := srows.Scan(&reason, &n); err != nil {
			fail(c, http.StatusInternalServerError, "scan risk account stats")
			return
		}
		stats[reason] = n
	}
	if err := srows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate risk account stats")
		return
	}

	ok(c, gin.H{"rows": out, "total": total, "stats": stats})
}
