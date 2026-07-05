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

// upsertNodeHeartbeatPG inserts or updates a cluster_nodes row for nodeID,
// setting last_heartbeat_at to now(). Safe to call repeatedly.
// Underlying PG implementation; called via pgOwnership.Heartbeat.
func (m *Manager) upsertNodeHeartbeatPG(ctx context.Context, nodeID string) error {
	_, err := m.bizPool.Exec(ctx, `
INSERT INTO cluster_nodes (node_id, last_heartbeat_at)
VALUES ($1, now())
ON CONFLICT (node_id) DO UPDATE SET last_heartbeat_at = now()
`, nodeID)
	if err != nil {
		return fmt.Errorf("upsert node heartbeat %q: %w", nodeID, err)
	}
	return nil
}

// deregisterNodePG clears owner_node on all account_devices owned by nodeID,
// then deletes the cluster_nodes row. Both steps run in a single transaction.
// Underlying PG implementation; called via pgOwnership.Deregister.
func (m *Manager) deregisterNodePG(ctx context.Context, nodeID string) error {
	tx, err := m.bizPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("deregister node %q begin tx: %w", nodeID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE account_devices SET owner_node = NULL WHERE owner_node = $1`,
		nodeID,
	); err != nil {
		return fmt.Errorf("clear owner_node for %q: %w", nodeID, err)
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM cluster_nodes WHERE node_id = $1`,
		nodeID,
	); err != nil {
		return fmt.Errorf("delete cluster_nodes row %q: %w", nodeID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("deregister node %q commit: %w", nodeID, err)
	}
	return nil
}

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

// ClaimAccount sets the owner_node and last_connected_at for an account.
func (m *Manager) ClaimAccount(ctx context.Context, accountJID, nodeID string) error {
	_, err := m.bizPool.Exec(ctx,
		`UPDATE account_devices SET owner_node=$2, last_connected_at=now() WHERE account_jid=$1`,
		accountJID, nodeID)
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

// staleOwnedAccountsPG returns active accounts whose owner node's heartbeat is
// older than staleness — candidates for takeover.
// Underlying PG implementation; called via pgOwnership.StaleOwned.
func (m *Manager) staleOwnedAccountsPG(ctx context.Context, staleness time.Duration) ([]string, error) {
	rows, err := m.bizPool.Query(ctx, `
SELECT a.account_jid
  FROM account_devices a
  JOIN cluster_nodes n ON n.node_id = a.owner_node
 WHERE a.ban_status='active'
   AND n.last_heartbeat_at < now() - make_interval(secs => $1)
 ORDER BY a.account_jid`, staleness.Seconds())
	if err != nil {
		return nil, fmt.Errorf("stale owned accounts: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, fmt.Errorf("scan stale account: %w", err)
		}
		out = append(out, jid)
	}
	return out, rows.Err()
}

// listUnownedActiveAccountsPG returns active accounts with no owner_node set —
// accounts that were deregistered gracefully and not yet re-adopted.
// Underlying PG implementation; called via pgOwnership.Unowned.
func (m *Manager) listUnownedActiveAccountsPG(ctx context.Context) ([]string, error) {
	rows, err := m.bizPool.Query(ctx,
		`SELECT account_jid FROM account_devices WHERE ban_status='active' AND owner_node IS NULL ORDER BY account_jid`)
	if err != nil {
		return nil, fmt.Errorf("list unowned active accounts: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, fmt.Errorf("scan unowned account: %w", err)
		}
		out = append(out, jid)
	}
	return out, rows.Err()
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

// ListUnownedActiveAccounts returns active accounts with no owner_node set.
func (m *Manager) ListUnownedActiveAccounts(ctx context.Context) ([]string, error) {
	return m.ownership.Unowned(ctx)
}
