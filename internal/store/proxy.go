// internal/store/proxy.go
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"go.mau.fi/whatsmeow"
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
	if m.proxyAlloc != nil {
		return m.proxyAlloc.bind(ctx, m.bizPool, accountJID, countryCode, time.Now().UnixMilli())
	}
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

// ReleaseProxy returns the account's bound proxy. Idempotent.
func (m *Manager) ReleaseProxy(ctx context.Context, accountJID string) error {
	if m.proxyAlloc != nil {
		return m.proxyAlloc.releaseBinding(ctx, m.bizPool, accountJID, time.Now().UnixMilli())
	}
	return pgx.BeginTxFunc(ctx, m.bizPool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return releaseWithinTx(ctx, tx, accountJID)
	})
}

// proxyFailureThreshold: consecutive failures before a proxy is taken offline.
const proxyFailureThreshold = 5

// ReportProxyFailure records one proxy-layer failure; auto-disables at threshold.
// Returns whether the proxy is now considered dead.
func (m *Manager) ReportProxyFailure(ctx context.Context, proxyID int64) (dead bool, err error) {
	// RETURNING folds the redis-sync input (country_code) into the same atomic
	// UPDATE, so the mirror reflects exactly this write (no TOCTOU with a later
	// SELECT that a concurrent report could have changed underneath us).
	const sql = `
UPDATE proxy_pool
   SET failure_count = failure_count + 1,
       is_alive = (failure_count + 1 < $2)
 WHERE id = $1
RETURNING NOT is_alive AS dead, country_code;`
	var cc string
	if err = m.bizPool.QueryRow(ctx, sql, proxyID, proxyFailureThreshold).Scan(&dead, &cc); err != nil {
		return false, fmt.Errorf("report proxy failure: %w", err)
	}
	// Redis is a best-effort MIRROR of the durable PG truth: a transient blip
	// must not fail the report (boot rebuild reconciles later). Log and continue.
	if dead && m.proxyAlloc != nil {
		if merr := m.proxyAlloc.markDead(ctx, proxyID, cc); merr != nil {
			m.log.Errorf("redis markDead proxy %d (cc=%s) failed (pg is authoritative): %v", proxyID, cc, merr)
		}
	}
	return dead, nil
}

// ReportProxySuccess clears the failure count, revives the proxy, refreshes latency.
func (m *Manager) ReportProxySuccess(ctx context.Context, proxyID int64, latencyMs int) error {
	// When the redis mirror is active, RETURNING pulls the markAlive inputs
	// (url/type/cc/free) from the same atomic UPDATE (no TOCTOU with a follow-up
	// SELECT). In pg mode we don't need those columns, so keep the plain Exec.
	if m.proxyAlloc == nil {
		if _, err := m.bizPool.Exec(ctx,
			`UPDATE proxy_pool
			    SET failure_count=0, is_alive=TRUE, latency_ms=$2, last_check_at=now()
			  WHERE id=$1`, proxyID, latencyMs); err != nil {
			return fmt.Errorf("report proxy success: %w", err)
		}
		return nil
	}

	var url, ptype, cc string
	var free int
	err := m.bizPool.QueryRow(ctx,
		`UPDATE proxy_pool
		    SET failure_count=0, is_alive=TRUE, latency_ms=$2, last_check_at=now()
		  WHERE id=$1
		RETURNING proxy_url, proxy_type::text, country_code, (max_bindings - current_bindings)`,
		proxyID, latencyMs).Scan(&url, &ptype, &cc, &free)
	if err != nil {
		return fmt.Errorf("report proxy success: %w", err)
	}
	// Best-effort mirror: a redis blip must not fail the report (boot rebuild
	// reconciles later). Log and continue; PG is already durably updated.
	meta := url + "|" + ptype + "|" + cc
	if merr := m.proxyAlloc.markAlive(ctx, proxyID, cc, meta, free, time.Now().UnixMilli()); merr != nil {
		m.log.Errorf("redis markAlive proxy %d (cc=%s) failed (pg is authoritative): %v", proxyID, cc, merr)
	}
	return nil
}

// ListAccountsByDeadProxies returns active accounts bound to dead proxies, for rebind.
func (m *Manager) ListAccountsByDeadProxies(ctx context.Context, tenantID int64, limit int) ([]string, error) {
	const sql = `
SELECT a.account_jid
  FROM account_devices a
  JOIN proxy_pool p ON p.id = a.proxy_id
 WHERE a.tenant_id = $1 AND p.is_alive = FALSE AND a.ban_status = 'active'
 LIMIT $2;`
	rows, err := m.bizPool.Query(ctx, sql, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("query dead-proxy accounts: %w", err)
	}
	defer rows.Close()

	var jids []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, fmt.Errorf("scan jid: %w", err)
		}
		jids = append(jids, jid)
	}
	return jids, rows.Err()
}

// DeadProxyAccount is an active account bound to a dead proxy, with the dead
// proxy's country_code (so the rebind can pick a same-country replacement).
type DeadProxyAccount struct {
	JID         string
	CountryCode string
}

// ListDeadProxyAccountsAll returns, across ALL tenants, active accounts bound to
// dead proxies, for automatic rebind. Read-only; mirrors ListAccountsByDeadProxies
// without the tenant filter and additionally returns the proxy country_code.
func (m *Manager) ListDeadProxyAccountsAll(ctx context.Context, limit int) ([]DeadProxyAccount, error) {
	const sql = `
SELECT a.account_jid, p.country_code
  FROM account_devices a
  JOIN proxy_pool p ON p.id = a.proxy_id
 WHERE p.is_alive = FALSE AND a.ban_status = 'active'
 LIMIT $1;`
	rows, err := m.bizPool.Query(ctx, sql, limit)
	if err != nil {
		return nil, fmt.Errorf("query dead-proxy accounts (all): %w", err)
	}
	defer rows.Close()

	var out []DeadProxyAccount
	for rows.Next() {
		var a DeadProxyAccount
		if err := rows.Scan(&a.JID, &a.CountryCode); err != nil {
			return nil, fmt.Errorf("scan dead-proxy account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ApplyProxy applies a bound proxy to a whatsmeow client. Must be called before
// client.Connect(). Rejects an empty binding (SetProxyAddress("") would silently
// UNSET the proxy, defeating per-account isolation).
func ApplyProxy(client *whatsmeow.Client, b *ProxyBinding) error {
	if b == nil || b.ProxyURL == "" {
		return errors.New("store: empty proxy binding")
	}
	if err := client.SetProxyAddress(b.ProxyURL); err != nil {
		return fmt.Errorf("apply proxy %s: %w", b.ProxyURL, err)
	}
	return nil
}
