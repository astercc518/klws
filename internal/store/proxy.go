// internal/store/proxy.go
package store

import (
	"context"
	"errors"
	"fmt"
	"time"
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

// BindProxy acquires one alive proxy with free capacity in the given country
// via the Redis ZSET cooldown allocator, and writes the binding to
// account_devices. See redisProxyAllocator.bind for the durable PG write it
// performs internally (proxy_pool counters + account_devices).
func (m *Manager) BindProxy(ctx context.Context, accountJID, countryCode string) (*ProxyBinding, error) {
	return m.proxyAlloc.bind(ctx, m.bizPool, accountJID, countryCode, time.Now().UnixMilli())
}

// ReleaseProxy returns the account's bound proxy. Idempotent. See
// redisProxyAllocator.releaseBinding for the durable PG write it performs
// internally (proxy_pool counter decrement + account_devices clear).
func (m *Manager) ReleaseProxy(ctx context.Context, accountJID string) error {
	return m.proxyAlloc.releaseBinding(ctx, m.bizPool, accountJID, time.Now().UnixMilli())
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
	if dead {
		if merr := m.proxyAlloc.markDead(ctx, proxyID, cc); merr != nil {
			m.log.Errorf("redis markDead proxy %d (cc=%s) failed (pg is authoritative): %v", proxyID, cc, merr)
		}
	}
	return dead, nil
}

// ReportProxySuccess clears the failure count, revives the proxy, refreshes latency.
func (m *Manager) ReportProxySuccess(ctx context.Context, proxyID int64, latencyMs int) error {
	// RETURNING pulls the markAlive inputs (url/type/cc/free) from the same
	// atomic UPDATE (no TOCTOU with a follow-up SELECT).
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
