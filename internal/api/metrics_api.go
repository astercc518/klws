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
	"time"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/metrics"
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

// ---------------------------------------------------------------------------
// GET /admin/reports/trend + GET /admin/reports/tenant-consumption — P3
// task-4. Both read-only, SystemPool, no red-line engine package touched.
// ---------------------------------------------------------------------------

// reportsBucketWhitelist bounds the `bucket` query param before it ever
// reaches metrics.Store.Trend. metric's whitelist lives inside Store (it maps
// to a SQL column name, so it MUST be validated there — see metrics/
// snapshot.go's metricColumns doc comment); bucket doesn't select an
// identifier, so there's no reason to make a DB round trip just to learn
// "month" isn't hour|day|week — the handler rejects it up front.
var reportsBucketWhitelist = map[string]bool{"hour": true, "day": true, "week": true}

// parseReportTrendRange parses `from`/`to` for GET /admin/reports/trend and
// GET /admin/reports/tenant-consumption. Accepts either an RFC3339 timestamp
// or a bare YYYY-MM-DD date (parsed in Asia/Shanghai, same cnLoc as
// parseBillRange in finance_query.go); a bare date for `to` is treated as
// inclusive of that whole day (advanced to the next day's midnight, mirroring
// parseBillRange's convention) so `to=2026-07-10` includes everything on
// 07-10. Empty from/to default to the trailing 30 days ending now.
func parseReportTrendRange(fromStr, toStr string) (from, to time.Time, err error) {
	parseOne := func(v string) (t time.Time, dateOnly bool, err error) {
		if t, err = time.Parse(time.RFC3339, v); err == nil {
			return t, false, nil
		}
		t, err = time.ParseInLocation("2006-01-02", v, cnLoc)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("bad time %q (want RFC3339 or YYYY-MM-DD)", v)
		}
		return t, true, nil
	}

	if toStr == "" {
		to = time.Now()
	} else {
		t, dateOnly, perr := parseOne(toStr)
		if perr != nil {
			return time.Time{}, time.Time{}, perr
		}
		if dateOnly {
			t = t.AddDate(0, 0, 1) // inclusive whole day -> next-day bound
		}
		to = t
	}

	if fromStr == "" {
		from = to.AddDate(0, 0, -30)
	} else {
		t, _, perr := parseOne(fromStr)
		if perr != nil {
			return time.Time{}, time.Time{}, perr
		}
		from = t
	}

	if !from.Before(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("`from` must be before `to`")
	}
	return from, to, nil
}

// trendPointView is the JSON shape of one metrics.TrendPoint.
type trendPointView struct {
	Bucket string  `json:"bucket"`
	Value  float64 `json:"value"`
}

// handleAdminReportsTrend: GET
// /admin/reports/trend?metric=&bucket=hour|day|week&from=&to= — reads T1's
// metric_snapshots time series via metrics.Store.Trend. A fresh Store is
// constructed per request (metrics.NewStore(s.systemPool()) — a cheap struct
// wrap around the existing SystemPool, not a new connection) rather than
// wiring it into Deps, since this handler is Trend's only caller today.
func (s *Server) handleAdminReportsTrend(c *gin.Context) {
	// Validate BOTH caller-supplied selectors at the boundary (400) so that
	// any error store.Trend returns below can only be a real backend failure
	// (pool.Query / rows.Scan / rows.Err) and is classified 500 — mixing the
	// two would return 400 for a dropped DB connection, leak raw Postgres
	// error text into a 4xx body, and suppress backend-failure alerting.
	// metric's whitelist lives in the metrics package (it maps to a SQL column
	// name); we read it through metrics.ValidMetric rather than keeping a
	// second, drift-prone copy here.
	metric := c.Query("metric")
	if !metrics.ValidMetric(metric) {
		fail(c, http.StatusBadRequest, "bad metric")
		return
	}
	bucket := c.DefaultQuery("bucket", "day")
	if !reportsBucketWhitelist[bucket] {
		fail(c, http.StatusBadRequest, "bad bucket (want hour|day|week)")
		return
	}
	from, to, err := parseReportTrendRange(c.Query("from"), c.Query("to"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	store := metrics.NewStore(s.systemPool())
	points, err := store.Trend(c.Request.Context(), metric, bucket, from, to)
	if err != nil {
		// metric + bucket are already whitelisted above, so Trend can only
		// fail on a real backend error (query / scan) here — 500, static msg.
		fail(c, http.StatusInternalServerError, "trend query")
		return
	}

	out := make([]trendPointView, 0, len(points))
	for _, p := range points {
		out = append(out, trendPointView{Bucket: p.Bucket.Format(time.RFC3339), Value: p.Value})
	}
	ok(c, out)
}

// tenantConsumptionRow is one ranked row of GET /admin/reports/tenant-consumption.
type tenantConsumptionRow struct {
	TenantID    int64  `json:"tenant_id"`
	TenantName  string `json:"tenant_name"`
	Consumption int64  `json:"consumption"`
}

// handleAdminReportsTenantConsumption: GET
// /admin/reports/tenant-consumption?from=&to=&limit=&offset= — tenant settle
// spend ranking, descending, paginated. Reuses SP4's exact 消耗 formula
// verbatim (netExpr = delta_balance+delta_frozen, defined in finance_stats.go;
// settle rows are stored negative so -SUM(...) reports a positive spend) so
// this endpoint and /admin/finance/stats' "top_tenants" can never silently
// disagree on what "消耗" means — no re-derivation here.
func (s *Server) handleAdminReportsTenantConsumption(c *gin.Context) {
	ctx := c.Request.Context()
	from, to, err := parseReportTrendRange(c.Query("from"), c.Query("to"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	var limit int
	if n, err := strconv.Atoi(c.Query("limit")); err == nil {
		limit = n
	}
	limit = clampPage(limit, 20, 200)
	var offset int
	if n, err := strconv.Atoi(c.Query("offset")); err == nil && n > 0 {
		offset = n
	}

	pool := s.systemPool()
	rows, err := pool.Query(ctx, `
SELECT l.tenant_id, COALESCE(t.name, ''),
       COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0) AS consumption
  FROM wallet_ledger l
  LEFT JOIN tenants t ON t.id = l.tenant_id
 WHERE l.created_at >= $1 AND l.created_at < $2
 GROUP BY l.tenant_id, t.name
 ORDER BY consumption DESC, l.tenant_id
 LIMIT $3 OFFSET $4`, from, to, limit, offset)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tenant consumption query")
		return
	}
	defer rows.Close()
	out := make([]tenantConsumptionRow, 0)
	for rows.Next() {
		var r tenantConsumptionRow
		if err := rows.Scan(&r.TenantID, &r.TenantName, &r.Consumption); err != nil {
			fail(c, http.StatusInternalServerError, "scan tenant consumption")
			return
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate tenant consumption")
		return
	}

	var total int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM (
  SELECT l.tenant_id FROM wallet_ledger l
   WHERE l.created_at >= $1 AND l.created_at < $2
   GROUP BY l.tenant_id
) sub`, from, to).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count tenant consumption")
		return
	}

	ok(c, gin.H{"rows": out, "total": total})
}

// ---------------------------------------------------------------------------
// GET /admin/reports/trend.csv + GET /admin/reports/tenant-consumption.csv —
// P3 task-5. CSV twins of the two JSON endpoints above: same query logic and
// handler-layer metric/bucket whitelist, output routed through SP4's shared
// writeCSV/csvSanitize (internal/api/csv.go — the finance ledger/bill export
// helper, reused verbatim here rather than re-implemented) so every cell is
// defended against spreadsheet formula injection, and audited like every
// other export endpoint (finance.ledger_export, finance.bill_export).
// ---------------------------------------------------------------------------

// handleAdminReportsTrendCSV: GET
// /admin/reports/trend.csv?metric=&bucket=hour|day|week&from=&to= — same
// metric/bucket whitelist + range parsing + metrics.Store.Trend query as
// handleAdminReportsTrend, streamed as CSV instead of JSON.
func (s *Server) handleAdminReportsTrendCSV(c *gin.Context) {
	metric := c.Query("metric")
	if !metrics.ValidMetric(metric) {
		fail(c, http.StatusBadRequest, "bad metric")
		return
	}
	bucket := c.DefaultQuery("bucket", "day")
	if !reportsBucketWhitelist[bucket] {
		fail(c, http.StatusBadRequest, "bad bucket (want hour|day|week)")
		return
	}
	from, to, err := parseReportTrendRange(c.Query("from"), c.Query("to"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	ctx := c.Request.Context()
	store := metrics.NewStore(s.systemPool())
	points, err := store.Trend(ctx, metric, bucket, from, to)
	if err != nil {
		// metric + bucket are already whitelisted above, same reasoning as
		// handleAdminReportsTrend — any error here is a real backend failure.
		fail(c, http.StatusInternalServerError, "trend query")
		return
	}

	out := make([][]string, 0, len(points))
	for _, p := range points {
		out = append(out, []string{
			p.Bucket.Format(time.RFC3339),
			strconv.FormatFloat(p.Value, 'f', -1, 64),
		})
	}
	s.recordAudit(ctx, auditEvent{
		ActorID: actorID(c), Action: "reports.trend_export", ResourceType: "report",
		Details: gin.H{"metric": metric, "bucket": bucket, "rows": len(out), "from": c.Query("from"), "to": c.Query("to")},
	})
	writeCSV(c, "trend.csv", []string{"bucket", "value"}, out)
}

// handleAdminReportsTenantConsumptionCSV: GET
// /admin/reports/tenant-consumption.csv?from=&to= — same 消耗 query/ranking as
// handleAdminReportsTenantConsumption but unpaginated (full filtered export,
// same "no limit/offset" convention as handleAdminLedgerExport), streamed as
// CSV.
func (s *Server) handleAdminReportsTenantConsumptionCSV(c *gin.Context) {
	ctx := c.Request.Context()
	from, to, err := parseReportTrendRange(c.Query("from"), c.Query("to"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	pool := s.systemPool()
	rows, err := pool.Query(ctx, `
SELECT l.tenant_id, COALESCE(t.name, ''),
       COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0) AS consumption
  FROM wallet_ledger l
  LEFT JOIN tenants t ON t.id = l.tenant_id
 WHERE l.created_at >= $1 AND l.created_at < $2
 GROUP BY l.tenant_id, t.name
 ORDER BY consumption DESC, l.tenant_id`, from, to)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tenant consumption export query")
		return
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var tenantID, consumption int64
		var name string
		if err := rows.Scan(&tenantID, &name, &consumption); err != nil {
			fail(c, http.StatusInternalServerError, "scan tenant consumption export")
			return
		}
		out = append(out, []string{
			strconv.FormatInt(tenantID, 10), name, strconv.FormatInt(consumption, 10),
		})
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate tenant consumption export")
		return
	}

	s.recordAudit(ctx, auditEvent{
		ActorID: actorID(c), Action: "reports.consumption_export", ResourceType: "report",
		Details: gin.H{"rows": len(out), "from": c.Query("from"), "to": c.Query("to")},
	})
	writeCSV(c, "tenant-consumption.csv", []string{"tenant_id", "tenant_name", "consumption"}, out)
}
