// internal/dispatch/selectaccount.go
package dispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// selectAccount picks the active, non-quarantined, proxy-bound account in the
// given country with the most remaining daily quota (effective_quota - sent_today),
// health-weighted via effective_quota. Country is matched through the bound proxy.
// random() breaks near-ties to reduce multi-worker collisions.
func (d *Dispatcher) selectAccount(ctx context.Context, tx pgx.Tx, tenantID int64, country string) (string, error) {
	var jid string
	err := tx.QueryRow(ctx, `
SELECT a.account_jid
  FROM account_devices a
  JOIN proxy_pool p ON p.id = a.proxy_id
 WHERE a.tenant_id = $1
   AND a.ban_status = 'active'
   AND (a.quarantined_until IS NULL OR a.quarantined_until < now())
   AND p.country_code = $2
   AND (effective_quota(a.registered_at, a.health_score) - a.sent_today) > 0
 ORDER BY (effective_quota(a.registered_at, a.health_score) - a.sent_today) DESC, random()
 LIMIT 1`, tenantID, country).Scan(&jid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoCapacity
	}
	if err != nil {
		return "", fmt.Errorf("select account: %w", err)
	}
	return jid, nil
}
