// Package sendgate is the pre-billing admission gate that protects WhatsApp
// accounts from bans via warmup quotas, human-like pacing, and health scoring.
package sendgate

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrAccountNotFound = errors.New("sendgate: account not found")

// QuarantineThreshold: health below this triggers a cooloff quarantine.
const QuarantineThreshold = 40

// healthDelta maps a whatsmeow-derived signal to a health adjustment.
var healthDelta = map[string]int{
	"undelivered":     -2,
	"recipient_block": -10,
	"wa_warning":      -30,
	"conn_churn":      -5,
	"delivered":       +1,
}

// SendGate is the pre-billing admission gate.
type SendGate struct {
	pool    *pgxpool.Pool
	adm     *Admission
	baseGap time.Duration
}

func NewSendGate(pool *pgxpool.Pool, adm *Admission, baseGap time.Duration) *SendGate {
	return &SendGate{pool: pool, adm: adm, baseGap: baseGap}
}

// ApplyHealthSignal adjusts an account's health (clamped 0..100). Dropping below
// QuarantineThreshold quarantines the account for cooloff.
func (g *SendGate) ApplyHealthSignal(ctx context.Context, jid, signal string, cooloff time.Duration) error {
	delta, ok := healthDelta[signal]
	if !ok {
		return fmt.Errorf("sendgate: unknown health signal %q", signal)
	}
	return pgx.BeginTxFunc(ctx, g.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var score int
		err := tx.QueryRow(ctx, `
UPDATE account_devices
   SET health_score = LEAST(100, GREATEST(0, health_score + $2))
 WHERE account_jid = $1
RETURNING health_score`, jid, delta).Scan(&score)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountNotFound
		}
		if err != nil {
			return fmt.Errorf("update health: %w", err)
		}
		if score < QuarantineThreshold {
			if _, err := tx.Exec(ctx,
				`UPDATE account_devices SET quarantined_until = now() + $2::interval WHERE account_jid = $1`,
				jid, fmt.Sprintf("%d seconds", int(cooloff.Seconds()))); err != nil {
				return fmt.Errorf("quarantine: %w", err)
			}
		}
		return nil
	})
}

// Heal slowly recovers health and clears expired quarantines. Run periodically.
func (g *SendGate) Heal(ctx context.Context) error {
	_, err := g.pool.Exec(ctx, `
UPDATE account_devices
   SET health_score = LEAST(100, health_score + 5),
       quarantined_until = CASE WHEN quarantined_until < now() THEN NULL ELSE quarantined_until END
 WHERE ban_status = 'active'`)
	if err != nil {
		return fmt.Errorf("heal: %w", err)
	}
	return nil
}

// Decision is the gate verdict. On Allow, Ticket holds the consumed quota slot
// (release it if a downstream step fails).
type Decision struct {
	Allow  bool
	Reason string
	Ticket *Ticket
}

// Admit is the pre-billing gate: ban state → quarantine → quota/pacing. Any
// failed check denies WITHOUT consuming the (later) billing hold.
func (g *SendGate) Admit(ctx context.Context, jid string, now time.Time) (Decision, error) {
	var banStatus string
	var registeredAt *time.Time
	var health int
	var quarantineTo *time.Time
	err := g.pool.QueryRow(ctx, `
SELECT ban_status::text, registered_at, health_score, quarantined_until
  FROM account_devices WHERE account_jid = $1`, jid).
		Scan(&banStatus, &registeredAt, &health, &quarantineTo)
	if errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, ErrAccountNotFound
	}
	if err != nil {
		return Decision{}, fmt.Errorf("load account: %w", err)
	}

	if banStatus != "active" {
		return Decision{Reason: "not_active"}, nil
	}
	if quarantineTo != nil && quarantineTo.After(now) {
		return Decision{Reason: "quarantined"}, nil
	}

	reg := now
	if registeredAt != nil {
		reg = *registeredAt
	}
	quota := EffectiveQuota(reg, health, now)
	ticket, reason, err := g.adm.Admit(ctx, jid, quota, jitteredGap(g.baseGap), now)
	if err != nil {
		return Decision{}, err
	}
	if ticket == nil {
		return Decision{Reason: reason}, nil // daily_quota / pacing
	}
	return Decision{Allow: true, Reason: "ok", Ticket: ticket}, nil
}

// jitteredGap returns base ± up to 40% jitter, so the send cadence looks human
// rather than like a fixed timer. base==0 disables pacing.
func jitteredGap(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	span := int64(base * 4 / 5) // ±40%
	j := rand.Int64N(span) - span/2
	return base + time.Duration(j)
}
