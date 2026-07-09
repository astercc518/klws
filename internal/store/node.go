// internal/store/node.go
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrProxyNotBound is returned by GetBoundProxy when the account has no cached proxy URL.
var ErrProxyNotBound = errors.New("store: account has no bound proxy")

// ListActiveAccounts returns all account JIDs with ban_status='active', ordered.
func (m *Manager) ListActiveAccounts(ctx context.Context) ([]string, error) {
	rows, err := m.bizPool.Query(ctx,
		`SELECT account_jid FROM account_devices WHERE ban_status='active' ORDER BY account_jid`)
	if err != nil {
		return nil, fmt.Errorf("list active accounts: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		out = append(out, jid)
	}
	return out, rows.Err()
}

// ClaimAccount touches last_connected_at for an account. Ownership itself is
// no longer mirrored to PG — it lives solely in Redis (owner:{jid}, written by
// the Ownership.Acquire backend via AcquireDeviceLock).
func (m *Manager) ClaimAccount(ctx context.Context, accountJID string) error {
	_, err := m.bizPool.Exec(ctx,
		`UPDATE account_devices SET last_connected_at=now() WHERE account_jid=$1`,
		accountJID)
	if err != nil {
		return fmt.Errorf("claim account: %w", err)
	}
	return nil
}

// MarkAccountLoggedOut sets ban_status='logged_out' so the account is excluded
// from ListActiveAccounts (hence never re-warmed by the WorkingSet/Reconciler).
// Used by the Ghost Reaper on a terminal LoggedOut/StreamReplaced signal.
func (m *Manager) MarkAccountLoggedOut(ctx context.Context, jid string) error {
	_, err := m.bizPool.Exec(ctx,
		`UPDATE account_devices SET ban_status='logged_out', ban_checked_at=now() WHERE account_jid=$1`,
		jid)
	if err != nil {
		return fmt.Errorf("mark account logged out %s: %w", jid, err)
	}
	return nil
}

// GetBoundProxy reads the cached proxy binding for an account (for reconnect).
// Returns ErrProxyNotBound if no proxy URL is cached.
// Country is populated by LEFT JOINing proxy_pool on the bound proxy_id (additive
// read-only; error semantics and transaction logic are unchanged).
func (m *Manager) GetBoundProxy(ctx context.Context, accountJID string) (*ProxyBinding, error) {
	var proxyID *int64
	var proxyURL *string
	var country *string
	err := m.bizPool.QueryRow(ctx,
		`SELECT ad.proxy_id, ad.proxy_url_cache, p.country_code
		   FROM account_devices ad
		   LEFT JOIN proxy_pool p ON p.id = ad.proxy_id
		  WHERE ad.account_jid = $1`,
		accountJID).Scan(&proxyID, &proxyURL, &country)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProxyNotBound
		}
		return nil, fmt.Errorf("get bound proxy: %w", err)
	}
	if proxyURL == nil {
		return nil, ErrProxyNotBound
	}
	var id int64
	if proxyID != nil {
		id = *proxyID
	}
	var cc string
	if country != nil {
		cc = *country
	}
	return &ProxyBinding{
		ProxyID:  id,
		ProxyURL: *proxyURL,
		Country:  cc,
	}, nil
}

// ---------------------------------------------------------------------------
// Public Ownership delegates — forward to the pluggable Ownership backend.
// ---------------------------------------------------------------------------

// AcquireDeviceLock non-blockingly acquires the advisory lock for accountJID.
// Returns ErrDeviceLocked if another node currently holds it.
func (m *Manager) AcquireDeviceLock(ctx context.Context, accountJID string) (LockHandle, error) {
	return m.ownership.Acquire(ctx, accountJID, m.cfg.NodeID)
}

// UpsertNodeHeartbeat refreshes the caller's liveness record in cluster_nodes.
func (m *Manager) UpsertNodeHeartbeat(ctx context.Context, nodeID string) error {
	return m.ownership.Heartbeat(ctx, nodeID)
}

// DeregisterNode gracefully deregisters nodeID (clears its accounts, removes row).
func (m *Manager) DeregisterNode(ctx context.Context, nodeID string) error {
	return m.ownership.Deregister(ctx, nodeID)
}

// StaleOwnedAccounts returns active accounts owned by nodes whose heartbeat is
// older than staleness — takeover candidates.
func (m *Manager) StaleOwnedAccounts(ctx context.Context, staleness time.Duration) ([]string, error) {
	return m.ownership.StaleOwned(ctx, staleness)
}

// ListUnownedActiveAccounts returns active accounts with no Redis owner:{jid} claim.
func (m *Manager) ListUnownedActiveAccounts(ctx context.Context) ([]string, error) {
	return m.ownership.Unowned(ctx)
}
