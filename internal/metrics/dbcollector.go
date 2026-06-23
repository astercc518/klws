package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// DBCollector exposes aggregate gauges sourced from Postgres. It implements
// prometheus.Collector and caches the snapshot for `ttl` so a scrape storm
// cannot hammer the database. All gauges use bounded labels only.
type DBCollector struct {
	pool *pgxpool.Pool
	ttl  time.Duration
	snap RegistrySnapshotProvider

	mu      sync.Mutex
	cached  []prometheus.Metric
	expires time.Time

	descAccounts   *prometheus.Desc // labels: ban_status
	descHealth     *prometheus.Desc // labels: bucket
	descWalletBal  *prometheus.Desc
	descWalletFroz *prometheus.Desc
	descWalletLock *prometheus.Desc
	descProxyUsed  *prometheus.Desc
	descProxyFree  *prometheus.Desc
	descProxyDead  *prometheus.Desc
	descRecipients *prometheus.Desc // labels: state
	descCharges    *prometheus.Desc // labels: state
	descRefundPend *prometheus.Desc
	descSessions   *prometheus.Desc
}

func NewDBCollector(pool *pgxpool.Pool, ttl time.Duration, snap RegistrySnapshotProvider) *DBCollector {
	return &DBCollector{
		pool: pool, ttl: ttl, snap: snap,
		descAccounts:   prometheus.NewDesc("wadist_accounts", "Account count by ban_status.", []string{"ban_status"}, nil),
		descHealth:     prometheus.NewDesc("wadist_accounts_health", "Active account count by health bucket.", []string{"bucket"}, nil),
		descWalletBal:  prometheus.NewDesc("wadist_wallet_balance_total", "Sum of tenant wallet available balance.", nil, nil),
		descWalletFroz: prometheus.NewDesc("wadist_wallet_frozen_total", "Sum of tenant wallet frozen funds.", nil, nil),
		descWalletLock: prometheus.NewDesc("wadist_wallet_locked", "Count of locked wallets (reconciliation drift).", nil, nil),
		descProxyUsed:  prometheus.NewDesc("wadist_proxy_slots_used", "Bound proxy slots (alive proxies).", nil, nil),
		descProxyFree:  prometheus.NewDesc("wadist_proxy_slots_free", "Free proxy slots (alive proxies).", nil, nil),
		descProxyDead:  prometheus.NewDesc("wadist_proxy_dead", "Count of dead proxies.", nil, nil),
		descRecipients: prometheus.NewDesc("wadist_recipients_state", "Campaign recipients by state.", []string{"state"}, nil),
		descCharges:    prometheus.NewDesc("wadist_charges_state", "Billing charges by state.", []string{"state"}, nil),
		descRefundPend: prometheus.NewDesc("wadist_refunds_pending", "Pending refund requests awaiting admin review.", nil, nil),
		descSessions:   prometheus.NewDesc("wadist_active_sessions", "Active account sessions (from M8 Registry; 0 until wired).", nil, nil),
	}
}

func (c *DBCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.descAccounts
	ch <- c.descHealth
	ch <- c.descWalletBal
	ch <- c.descWalletFroz
	ch <- c.descWalletLock
	ch <- c.descProxyUsed
	ch <- c.descProxyFree
	ch <- c.descProxyDead
	ch <- c.descRecipients
	ch <- c.descCharges
	ch <- c.descRefundPend
	ch <- c.descSessions
}

func (c *DBCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.cached == nil || now.After(c.expires) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		metrics := c.snapshot(ctx)
		cancel()
		if metrics != nil { // keep stale cache on transient DB error
			c.cached = metrics
			c.expires = now.Add(c.ttl)
		}
	}
	for _, m := range c.cached {
		ch <- m
	}
}

// snapshot runs the aggregate queries. On any query error it returns nil so the
// caller keeps the previous (stale) cache rather than emitting partial data.
func (c *DBCollector) snapshot(ctx context.Context) []prometheus.Metric {
	var out []prometheus.Metric
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		out = append(out, prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...))
	}

	// accounts by ban_status
	rows, err := c.pool.Query(ctx, `SELECT ban_status::text, COUNT(*) FROM account_devices GROUP BY ban_status`)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var s string
		var n float64
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil
		}
		gauge(c.descAccounts, n, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil
	}

	// active accounts by health bucket
	rows, err = c.pool.Query(ctx, `
SELECT CASE WHEN health_score >= 80 THEN 'healthy'
            WHEN health_score >= 40 THEN 'degraded'
            ELSE 'at_risk' END AS bucket, COUNT(*)
  FROM account_devices WHERE ban_status='active' GROUP BY bucket`)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var b string
		var n float64
		if err := rows.Scan(&b, &n); err != nil {
			rows.Close()
			return nil
		}
		gauge(c.descHealth, n, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil
	}

	// wallets
	var bal, froz, locked float64
	if err := c.pool.QueryRow(ctx, `
SELECT COALESCE(SUM(balance),0), COALESCE(SUM(frozen),0),
       COUNT(*) FILTER (WHERE locked=TRUE)
  FROM tenant_wallets`).Scan(&bal, &froz, &locked); err != nil {
		return nil
	}
	gauge(c.descWalletBal, bal)
	gauge(c.descWalletFroz, froz)
	gauge(c.descWalletLock, locked)

	// proxy pool (alive utilization + dead count)
	var used, free, dead float64
	if err := c.pool.QueryRow(ctx, `
SELECT COALESCE(SUM(current_bindings) FILTER (WHERE is_alive),0),
       COALESCE(SUM(max_bindings - current_bindings) FILTER (WHERE is_alive),0),
       COUNT(*) FILTER (WHERE is_alive=FALSE)
  FROM proxy_pool`).Scan(&used, &free, &dead); err != nil {
		return nil
	}
	gauge(c.descProxyUsed, used)
	gauge(c.descProxyFree, free)
	gauge(c.descProxyDead, dead)

	// recipients by state
	rows, err = c.pool.Query(ctx, `SELECT state::text, COUNT(*) FROM campaign_recipients GROUP BY state`)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var s string
		var n float64
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil
		}
		gauge(c.descRecipients, n, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil
	}

	// charges by state
	rows, err = c.pool.Query(ctx, `SELECT state::text, COUNT(*) FROM billing_charges GROUP BY state`)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var s string
		var n float64
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil
		}
		gauge(c.descCharges, n, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil
	}

	// pending refunds
	var refundPend float64
	if err := c.pool.QueryRow(ctx, `SELECT COUNT(*) FROM refund_requests WHERE state='pending'`).Scan(&refundPend); err != nil {
		return nil
	}
	gauge(c.descRefundPend, refundPend)

	// active sessions from M8 Registry (nil → 0)
	sessions := 0.0
	if c.snap != nil {
		sessions = float64(c.snap.ActiveSessions())
	}
	gauge(c.descSessions, sessions)

	return out
}
