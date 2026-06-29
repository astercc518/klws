// Package riskbreaker is a side-car supervisor that auto-pauses campaigns whose
// ban rate exceeds the admin-configured threshold (system_risk_config).
//
// It is deliberately decoupled from the dispatch/sendgate engine: it talks to
// the dispatcher ONLY through campaign state in the database — it sets
// campaigns.state='paused', which the dispatcher already honors (it dispatches
// only state='running'). It therefore imports and modifies NO red-line engine
// package; it reads/writes existing tables via the pool exactly like the admin
// API does. See docs/RISK-CIRCUIT-BREAKER-DESIGN-zh.md.
package riskbreaker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey elects a single evaluator across nodes (pg advisory lock is
// per-session). Arbitrary fixed app constant.
const advisoryLockKey int64 = 0x7269736b6201 // "riskb\x01"

// defaultEvalInterval is used before/without a valid config row.
const defaultEvalInterval = 20 * time.Second

type config struct {
	threshold    float64
	enabled      bool
	dryRun       bool
	minSample    int
	windowSec    int
	evalInterval time.Duration
}

type campaign struct {
	ID       int64
	TenantID int64
}

// Breaker periodically evaluates running campaigns and pauses those whose ban
// rate is at or above the configured threshold.
type Breaker struct {
	pool *pgxpool.Pool
	log  *log.Logger
}

// New builds a Breaker. Pass the BYPASSRLS SystemPool so it can see campaigns
// across all tenants and append to the append-only audit_log.
func New(pool *pgxpool.Pool) *Breaker {
	return &Breaker{pool: pool, log: log.Default()}
}

// Run blocks until ctx is cancelled, evaluating on the cadence configured in
// system_risk_config.eval_interval_seconds (re-read every cycle, so threshold,
// enable/dry-run and cadence all hot-reload without a restart).
func (b *Breaker) Run(ctx context.Context) error {
	delay := 5 * time.Second // start soon after boot
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		next, err := b.tick(ctx)
		if err != nil && ctx.Err() == nil {
			b.log.Printf("riskbreaker: %v", err)
		}
		delay = next
	}
}

// tick reads config and, when enabled, evaluates once under a cross-node lock.
// It always returns the next delay (even when disabled) so cadence stays live.
func (b *Breaker) tick(ctx context.Context) (time.Duration, error) {
	cfg, err := b.readConfig(ctx)
	if err != nil {
		return defaultEvalInterval, err
	}
	if !cfg.enabled {
		return cfg.evalInterval, nil
	}
	locked, unlock, err := b.tryLock(ctx)
	if err != nil {
		return cfg.evalInterval, err
	}
	if !locked {
		return cfg.evalInterval, nil // another node owns this cycle
	}
	defer unlock()
	if _, err := b.EvaluateOnce(ctx, cfg); err != nil {
		return cfg.evalInterval, err
	}
	return cfg.evalInterval, nil
}

// EvaluateOnce scores every running campaign and trips the offenders. Returns
// the number actually paused (0 in dry-run). Exported for tests.
func (b *Breaker) EvaluateOnce(ctx context.Context, cfg config) (tripped int, err error) {
	camps, err := b.runningCampaigns(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range camps {
		rate, attempted, banFailures, err := b.banRate(ctx, c.ID, cfg.windowSec)
		if err != nil {
			b.log.Printf("riskbreaker: ban rate campaign %d: %v", c.ID, err)
			continue
		}
		if attempted < cfg.minSample || rate < cfg.threshold {
			continue
		}
		if cfg.dryRun {
			b.log.Printf("riskbreaker: DRY-RUN would pause campaign %d (ban_rate=%.3f attempted=%d ban_failures=%d threshold=%.3f window=%ds)",
				c.ID, rate, attempted, banFailures, cfg.threshold, cfg.windowSec)
			continue
		}
		paused, err := b.trip(ctx, c, rate, attempted, banFailures, cfg)
		if err != nil {
			b.log.Printf("riskbreaker: trip campaign %d: %v", c.ID, err)
			continue
		}
		if paused {
			tripped++
			b.log.Printf("riskbreaker: PAUSED campaign %d (ban_rate=%.3f attempted=%d threshold=%.3f)",
				c.ID, rate, attempted, cfg.threshold)
		}
	}
	return tripped, nil
}

func (b *Breaker) runningCampaigns(ctx context.Context) ([]campaign, error) {
	rows, err := b.pool.Query(ctx, `SELECT id, tenant_id FROM campaigns WHERE state='running' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list running campaigns: %w", err)
	}
	defer rows.Close()
	var out []campaign
	for rows.Next() {
		var c campaign
		if err := rows.Scan(&c.ID, &c.TenantID); err != nil {
			return nil, fmt.Errorf("scan campaign: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// banRate computes definition A (send-outcome attribution) over the recent
// window: of recipients attempted in the last windowSec seconds, the fraction
// whose failure carried a ban signal. Mirrors dispatch's isBanSignal patterns
// (wa_warning | banned | 403) — keep the two in sync (see design doc §10).
func (b *Breaker) banRate(ctx context.Context, campaignID int64, windowSec int) (rate float64, attempted, banFailures int, err error) {
	window := fmt.Sprintf("%d seconds", windowSec)
	err = b.pool.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE state IN ('sent','failed') AND updated_at >= now() - $2::interval),
  count(*) FILTER (WHERE state = 'failed' AND updated_at >= now() - $2::interval AND (
        last_error ILIKE '%wa_warning%' OR last_error ILIKE '%banned%' OR last_error ILIKE '%403%'))
  FROM campaign_recipients
 WHERE campaign_id = $1`, campaignID, window).Scan(&attempted, &banFailures)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("query ban rate: %w", err)
	}
	if attempted > 0 {
		rate = float64(banFailures) / float64(attempted)
	}
	return rate, attempted, banFailures, nil
}

// trip pauses the campaign (idempotent: only if still running) and appends an
// audit row, in one transaction. Returns false if it was already not running.
func (b *Breaker) trip(ctx context.Context, c campaign, rate float64, attempted, banFailures int, cfg config) (bool, error) {
	var paused bool
	err := pgx.BeginTxFunc(ctx, b.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE campaigns SET state='paused' WHERE id=$1 AND state='running'`, c.ID)
		if err != nil {
			return fmt.Errorf("pause: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil // already paused/stopped by an operator or another node
		}
		paused = true
		_, err = tx.Exec(ctx, `
INSERT INTO audit_log (tenant_id, actor_id, action, resource_type, resource_id, details)
VALUES ($1, NULL, 'campaign.circuit_break', 'campaign', $2,
        jsonb_build_object(
          'ban_rate', $3::float8, 'threshold', $4::float8,
          'attempted', $5::int, 'ban_failures', $6::int, 'window_seconds', $7::int))`,
			c.TenantID, c.ID, rate, cfg.threshold, attempted, banFailures, cfg.windowSec)
		if err != nil {
			return fmt.Errorf("audit: %w", err)
		}
		return nil
	})
	return paused, err
}

func (b *Breaker) readConfig(ctx context.Context) (config, error) {
	var c config
	var evalSec int
	err := b.pool.QueryRow(ctx, `
SELECT ban_rate_circuit_breaker, circuit_breaker_enabled, circuit_breaker_dry_run,
       min_sample, window_seconds, eval_interval_seconds
  FROM system_risk_config WHERE id = 1`).
		Scan(&c.threshold, &c.enabled, &c.dryRun, &c.minSample, &c.windowSec, &evalSec)
	if errors.Is(err, pgx.ErrNoRows) {
		return config{enabled: false, evalInterval: defaultEvalInterval}, nil
	}
	if err != nil {
		return config{evalInterval: defaultEvalInterval}, fmt.Errorf("read risk config: %w", err)
	}
	if evalSec < 1 {
		evalSec = int(defaultEvalInterval / time.Second)
	}
	c.evalInterval = time.Duration(evalSec) * time.Second
	return c, nil
}

// tryLock attempts a session-level advisory lock on a dedicated connection so a
// single node evaluates per cycle. The returned unlock releases both the lock
// and the connection; it is a no-op-safe closure when the lock wasn't acquired.
func (b *Breaker) tryLock(ctx context.Context) (bool, func(), error) {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return false, func() {}, fmt.Errorf("acquire conn: %w", err)
	}
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, advisoryLockKey).Scan(&got); err != nil {
		conn.Release()
		return false, func() {}, fmt.Errorf("advisory lock: %w", err)
	}
	if !got {
		conn.Release()
		return false, func() {}, nil
	}
	unlock := func() {
		// Use a fresh context: ctx may already be cancelled on shutdown.
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryLockKey)
		conn.Release()
	}
	return true, unlock, nil
}
