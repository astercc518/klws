// internal/store/proxy.go
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var (
	ErrNoProxyAvailable = errors.New("store: no alive proxy with free capacity")
	ErrAccountMissing   = errors.New("store: account_devices row not found")
)

// ProxyBinding is a snapshot of a successful proxy acquisition.
type ProxyBinding struct {
	ProxyID   int64
	ProxyURL  string
	ProxyType string
	Country   string
}

// BindProxy atomically: (1) returns any proxy currently bound to the account,
// (2) acquires one alive proxy with free capacity in the given country via
// FOR UPDATE SKIP LOCKED, incrementing its counters, (3) writes the binding to
// account_devices. All in one tx — any failure rolls back the counter increment.
func (m *Manager) BindProxy(ctx context.Context, accountJID, countryCode string) (*ProxyBinding, error) {
	var binding *ProxyBinding
	err := pgx.BeginTxFunc(ctx, m.bizPool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := releaseWithinTx(ctx, tx, accountJID); err != nil {
			return err
		}

		const acquireSQL = `
WITH picked AS (
    SELECT id FROM proxy_pool
     WHERE is_alive = TRUE AND current_bindings < max_bindings AND country_code = $1
     ORDER BY usage_count ASC, latency_ms ASC
     FOR UPDATE SKIP LOCKED
     LIMIT 1
)
UPDATE proxy_pool p
   SET current_bindings = p.current_bindings + 1,
       usage_count      = p.usage_count + 1
  FROM picked
 WHERE p.id = picked.id
RETURNING p.id, p.proxy_url, p.proxy_type::text, p.country_code;`

		var b ProxyBinding
		err := tx.QueryRow(ctx, acquireSQL, countryCode).
			Scan(&b.ProxyID, &b.ProxyURL, &b.ProxyType, &b.Country)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoProxyAvailable
		}
		if err != nil {
			return fmt.Errorf("acquire proxy: %w", err)
		}

		ct, err := tx.Exec(ctx,
			`UPDATE account_devices SET proxy_id=$1, proxy_url_cache=$2 WHERE account_jid=$3`,
			b.ProxyID, b.ProxyURL, accountJID)
		if err != nil {
			return fmt.Errorf("bind proxy to account: %w", err)
		}
		if ct.RowsAffected() == 0 {
			return ErrAccountMissing
		}
		binding = &b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return binding, nil
}

// releaseWithinTx returns the account's current proxy (if any) within an open tx.
// Idempotent: no-op when unbound. GREATEST guards against a negative counter.
func releaseWithinTx(ctx context.Context, tx pgx.Tx, accountJID string) error {
	var oldProxyID *int64
	err := tx.QueryRow(ctx,
		`SELECT proxy_id FROM account_devices WHERE account_jid=$1 FOR UPDATE`,
		accountJID).Scan(&oldProxyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccountMissing
	}
	if err != nil {
		return fmt.Errorf("lock account row: %w", err)
	}
	if oldProxyID == nil {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE proxy_pool SET current_bindings = GREATEST(current_bindings-1, 0) WHERE id=$1`,
		*oldProxyID); err != nil {
		return fmt.Errorf("decrement old proxy: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE account_devices SET proxy_id=NULL, proxy_url_cache=NULL WHERE account_jid=$1`,
		accountJID); err != nil {
		return fmt.Errorf("clear account binding: %w", err)
	}
	return nil
}
