// internal/dispatch/batch.go
package dispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const maxAttempts = 30

// dispatchBatch pulls up to `batch` pending recipients (SKIP LOCKED), assigns each
// to the best account, bumps sent_today (approximate selection counter), marks the
// recipient assigned with an idempotent message_id, and enqueues a send. Recipients
// with no available account capacity are left pending (picked up next run / next day).
// Selection + bump + assign happen in one tx so a failure rolls all back.
func (d *Dispatcher) dispatchBatch(ctx context.Context, campaignID int64, batch int) (int, error) {
	tenantID, err := d.tenantOf(ctx, campaignID)
	if err != nil {
		return 0, err
	}
	assigned := 0
	err = pgx.BeginTxFunc(ctx, d.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
SELECT id, phone, country_code, vars
  FROM campaign_recipients
 WHERE campaign_id=$1 AND state='pending' AND assigned_jid IS NULL
   AND attempt < $3 -- cap denied-send churn; full backoff is future work
 FOR UPDATE SKIP LOCKED
 LIMIT $2`, campaignID, batch, maxAttempts)
		if err != nil {
			return fmt.Errorf("pull recipients: %w", err)
		}
		type rec struct {
			id             int64
			phone, country string
			vars           map[string]any
		}
		var rs []rec
		for rows.Next() {
			var r rec
			if err := rows.Scan(&r.id, &r.phone, &r.country, &r.vars); err != nil {
				rows.Close()
				return err
			}
			rs = append(rs, r)
		}
		rows.Close()

		var payloads []SendPayload
		for _, r := range rs {
			jid, err := d.selectAccount(ctx, tx, tenantID, r.country)
			if errors.Is(err, ErrNoCapacity) {
				d.m.IncNoCapacity()
				continue // leave pending
			}
			if err != nil {
				return err
			}
			msgID := fmt.Sprintf("%d:%d", campaignID, r.id)
			if _, err := tx.Exec(ctx,
				`UPDATE campaign_recipients SET assigned_jid=$2, message_id=$3, attempt=attempt+1 WHERE id=$1`,
				r.id, jid, msgID); err != nil {
				return fmt.Errorf("assign recipient: %w", err)
			}
			if _, err := tx.Exec(ctx,
				`UPDATE account_devices SET sent_today=sent_today+1 WHERE account_jid=$1`, jid); err != nil {
				return fmt.Errorf("bump sent_today: %w", err)
			}
			payloads = append(payloads, SendPayload{
				TenantID: tenantID, CampaignID: campaignID, RecipientID: r.id,
				JID: jid, Phone: r.phone, Country: r.country, MessageID: msgID, Vars: r.vars,
			})
		}
		// enqueue after the tx body builds the list; do it inside tx so a queue
		// failure rolls back the assignment (at-least-once dispatch is safe via msg_id).
		for _, p := range payloads {
			if err := d.queue.EnqueueSend(ctx, p, jitter(d.baseGap)); err != nil {
				return fmt.Errorf("enqueue: %w", err)
			}
			assigned++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	d.m.ObserveBatch(assigned)
	return assigned, nil
}

func (d *Dispatcher) tenantOf(ctx context.Context, campaignID int64) (int64, error) {
	var t int64
	if err := d.pool.QueryRow(ctx, `SELECT tenant_id FROM campaigns WHERE id=$1`, campaignID).Scan(&t); err != nil {
		return 0, fmt.Errorf("tenant of campaign: %w", err)
	}
	return t, nil
}
