package metrics

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Sampler periodically aggregates operational metrics already sitting in
// Postgres (account_devices / campaign_recipients) into a Snapshot and
// persists it via Store. First cut: DB-only metrics — no in-memory dispatch
// state. Sampling failures are logged and never panic; a broken sampler must
// never take down the send pipeline.
type Sampler struct {
	store         *Store
	pool          *pgxpool.Pool
	interval      time.Duration
	retentionDays int
}

// NewSampler constructs a Sampler. pool MUST be the SystemPool (BYPASSRLS):
// the aggregate queries read account_devices/campaign_recipients across all
// tenants — an RLS-constrained (app_tenant) pool would silently narrow the
// result to a single tenant, same reasoning as dispatch.Governor's
// FleetBanRate (see cmd/wadist/main.go).
func NewSampler(store *Store, pool *pgxpool.Pool, interval time.Duration, retentionDays int) *Sampler {
	return &Sampler{store: store, pool: pool, interval: interval, retentionDays: retentionDays}
}

// SampleOnce runs the aggregate queries and returns one Snapshot. It does
// NOT write to the store — callers (RunLoop, tests) do that separately.
func (s *Sampler) SampleOnce(ctx context.Context) (Snapshot, error) {
	var snap Snapshot
	snap.CapturedAt = time.Now()

	// account_devices: total/active/banned/quarantined in one query. The
	// active/banned predicates mirror handleAdminStats (internal/api/admin_api.go);
	// quarantined is independent of ban_status (quarantined_until > now(), spec §4).
	if err := s.pool.QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE ban_status='active'),
       count(*) FILTER (WHERE ban_status IN ('banned','flagged')),
       count(*) FILTER (WHERE quarantined_until > now())
  FROM account_devices`,
	).Scan(&snap.AccountsTotal, &snap.AccountsActive, &snap.AccountsBanned, &snap.AccountsQuarantined); err != nil {
		return Snapshot{}, fmt.Errorf("metrics: sample accounts: %w", err)
	}

	// campaign_recipients: queue backlog + near-1h throughput + avg delivery
	// latency, in one query.
	//
	// campaign_recipients has NO created_at column (created in 0006_dispatch.sql
	// with only `updated_at`; confirmed absent in every later migration too), so
	// spec's avg_delivery_ms formula (delivered_at - created_at) cannot be
	// computed against the recipient row itself. It is computed here against
	// campaigns.created_at instead: every INSERT INTO campaign_recipients call
	// (internal/api/campaign.go, internal/console/send.go, internal/store/pii.go)
	// bulk-inserts recipients synchronously as part of campaign creation — there
	// is no async enqueue gap — so campaigns.created_at is a tight proxy for
	// "recipient created_at" without altering the dispatch engine's schema.
	var avgMs *float64
	if err := s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE cr.state='pending'),
       count(*) FILTER (WHERE cr.state IN ('sent','failed','skipped') AND cr.updated_at > now() - interval '1 hour'),
       count(*) FILTER (WHERE cr.delivered_at > now() - interval '1 hour'),
       count(*) FILTER (WHERE cr.state='failed' AND cr.updated_at > now() - interval '1 hour'),
       avg(EXTRACT(EPOCH FROM (cr.delivered_at - c.created_at)) * 1000)
         FILTER (WHERE cr.delivered_at > now() - interval '1 hour')
  FROM campaign_recipients cr
  JOIN campaigns c ON c.id = cr.campaign_id`,
	).Scan(&snap.QueueBacklog, &snap.Processed1h, &snap.Delivered1h, &snap.Failed1h, &avgMs); err != nil {
		return Snapshot{}, fmt.Errorf("metrics: sample recipients: %w", err)
	}
	if avgMs != nil {
		v := int(math.Round(*avgMs))
		snap.AvgDeliveryMs = &v
	}

	return snap, nil
}

// RunLoop ticks every s.interval: sample → insert → prune rows older than
// retentionDays. Every step's error is logged and swallowed — sampling must
// never take down the worker process (it only reads existing tables and
// writes to metric_snapshots, never touches engine business logic). Returns
// ctx.Err() once ctx is done, matching the other RunLoop-style goroutines
// wired via cluster.Supervisor.Go in cmd/wadist/main.go.
func (s *Sampler) RunLoop(ctx context.Context) error {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			snap, err := s.SampleOnce(ctx)
			if err != nil {
				log.Printf("metrics sampler: sample: %v", err)
				continue
			}
			if err := s.store.Insert(ctx, snap); err != nil {
				log.Printf("metrics sampler: insert: %v", err)
				continue
			}
			if _, err := s.store.Prune(ctx, time.Now().AddDate(0, 0, -s.retentionDays)); err != nil {
				log.Printf("metrics sampler: prune: %v", err)
			}
		}
	}
}
