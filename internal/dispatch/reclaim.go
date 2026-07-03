// internal/dispatch/reclaim.go
package dispatch

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReclaimOrphanedAssignments resets recipients that were assigned to an account
// (sent_today bumped, message_id claimed) but never reached a terminal state —
// orphans left when an in-memory pump lost buffered/in-flight payloads on crash.
// It decrements each account's sent_today by its orphan count and clears the
// assignment so dispatchBatch re-picks them (its `assigned_jid IS NULL` filter
// otherwise leaves them stuck forever). message_id is deterministic
// (campaignID:recipientID), so re-dispatch is billing-idempotent (Hold ON
// CONFLICT). Call once at pump-mode boot, before dispatchBatch starts looping.
func ReclaimOrphanedAssignments(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Decrement sent_today per account by its orphan count. Must run before
		// the recipients are cleared below, since it reads the assignments.
		if _, err := tx.Exec(ctx, `
UPDATE account_devices a
   SET sent_today = GREATEST(a.sent_today - o.cnt, 0)
  FROM (
    SELECT assigned_jid, COUNT(*) AS cnt
      FROM campaign_recipients
     WHERE state = 'pending' AND assigned_jid IS NOT NULL
     GROUP BY assigned_jid
  ) o
 WHERE a.account_jid = o.assigned_jid`); err != nil {
			return fmt.Errorf("reclaim sent_today: %w", err)
		}

		ct, err := tx.Exec(ctx, `
UPDATE campaign_recipients
   SET assigned_jid = NULL, message_id = NULL, attempt = GREATEST(attempt - 1, 0)
 WHERE state = 'pending' AND assigned_jid IS NOT NULL`)
		if err != nil {
			return fmt.Errorf("reclaim recipients: %w", err)
		}
		n = int(ct.RowsAffected())
		return nil
	})
	return n, err
}
