package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Snapshot is one row of the platform-level metric_snapshots time series.
// tenant_id is intentionally not exposed here: the first cut only ever
// writes tenant_id=NULL (platform aggregate); the column is reserved in the
// schema for a future per-tenant slice.
type Snapshot struct {
	CapturedAt time.Time

	AccountsTotal       int
	AccountsActive      int
	AccountsBanned      int
	AccountsQuarantined int
	QueueBacklog        int
	Processed1h         int
	Delivered1h         int
	Failed1h            int
	AvgDeliveryMs       *int // nullable: no traffic in the sampling window
}

// TrendPoint is one bucketed point of a Trend() series.
type TrendPoint struct {
	Bucket time.Time
	Value  float64
}

// Store wraps the platform SystemPool (BYPASSRLS app_system) for reading and
// writing metric_snapshots. metric_snapshots has no RLS policy (platform-level
// operational data, same GRANT/REVOKE shape as audit_log) — callers MUST pass
// the SystemPool, not the RLS-constrained tenant pool.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store backed by pool (must be the SystemPool).
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const insertSnapshot = `
INSERT INTO metric_snapshots
    (captured_at, tenant_id, accounts_total, accounts_active, accounts_banned,
     accounts_quarantined, queue_backlog, processed_1h, delivered_1h, failed_1h, avg_delivery_ms)
VALUES ($1, NULL, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

// Insert writes one snapshot row. tenant_id is always written as NULL
// (platform-level; see Snapshot doc comment).
func (s *Store) Insert(ctx context.Context, snap Snapshot) error {
	capturedAt := snap.CapturedAt
	if capturedAt.IsZero() {
		capturedAt = time.Now()
	}
	_, err := s.pool.Exec(ctx, insertSnapshot,
		capturedAt,
		snap.AccountsTotal,
		snap.AccountsActive,
		snap.AccountsBanned,
		snap.AccountsQuarantined,
		snap.QueueBacklog,
		snap.Processed1h,
		snap.Delivered1h,
		snap.Failed1h,
		snap.AvgDeliveryMs,
	)
	if err != nil {
		return fmt.Errorf("metrics: insert snapshot: %w", err)
	}
	return nil
}

// Prune deletes snapshot rows older than olderThan and returns the number of
// rows removed. Callers (the sampler) run this after every sample to enforce
// the retention window.
func (s *Store) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM metric_snapshots WHERE captured_at < $1`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("metrics: prune snapshots: %w", err)
	}
	return tag.RowsAffected(), nil
}

// metricColumns whitelists the metric names Trend() may aggregate. metric
// selects a SQL column name and therefore CANNOT be passed as a bind
// parameter (Postgres has no placeholder syntax for identifiers) — every
// caller-supplied metric MUST be resolved through this map before being
// interpolated into the query. Unknown metric -> error, never fall through
// to string concatenation of the raw input.
var metricColumns = map[string]string{
	"accounts_total":       "accounts_total",
	"accounts_active":      "accounts_active",
	"accounts_banned":      "accounts_banned",
	"accounts_quarantined": "accounts_quarantined",
	"queue_backlog":        "queue_backlog",
	"processed_1h":         "processed_1h",
	"delivered_1h":         "delivered_1h",
	"failed_1h":            "failed_1h",
	"avg_delivery_ms":      "avg_delivery_ms",
}

// ValidMetric reports whether name is a whitelisted Trend() metric. Callers
// (e.g. the HTTP layer) use this to reject a bad metric at the boundary with a
// 400 BEFORE calling Trend, so that any error Trend itself returns can be
// classified as a real backend failure (500) — the whitelist is authored once
// here and read through this function to avoid a second, drifting copy.
func ValidMetric(name string) bool {
	_, ok := metricColumns[name]
	return ok
}

// bucketWhitelist bounds the date_trunc() field argument. date_trunc's field
// parameter is an ordinary text value (not an identifier), so it is safe to
// bind normally — but we still validate it against a fixed set to reject
// garbage input early with a clear error instead of a Postgres error.
var bucketWhitelist = map[string]bool{
	"hour": true,
	"day":  true,
	"week": true,
}

// Trend returns the bucketed average of metric over [from, to], grouped by
// date_trunc(bucket, captured_at AT TIME ZONE 'Asia/Shanghai'). metric must be
// a key of metricColumns and bucket must be one of hour|day|week; anything
// else returns an error rather than being interpolated into SQL.
func (s *Store) Trend(ctx context.Context, metric, bucket string, from, to time.Time) ([]TrendPoint, error) {
	col, ok := metricColumns[metric]
	if !ok {
		return nil, fmt.Errorf("metrics: trend: unknown metric %q", metric)
	}
	if !bucketWhitelist[bucket] {
		return nil, fmt.Errorf("metrics: trend: unknown bucket %q", bucket)
	}

	query := fmt.Sprintf(`
SELECT date_trunc($1, captured_at AT TIME ZONE 'Asia/Shanghai') AS b, avg(%s)::float8 AS v
FROM metric_snapshots
WHERE captured_at BETWEEN $2 AND $3
GROUP BY b
ORDER BY b`, col)

	rows, err := s.pool.Query(ctx, query, bucket, from, to)
	if err != nil {
		return nil, fmt.Errorf("metrics: trend query: %w", err)
	}
	defer rows.Close()

	var points []TrendPoint
	for rows.Next() {
		var p TrendPoint
		if err := rows.Scan(&p.Bucket, &p.Value); err != nil {
			return nil, fmt.Errorf("metrics: trend scan: %w", err)
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metrics: trend rows: %w", err)
	}
	return points, nil
}
