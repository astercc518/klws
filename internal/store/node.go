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

// UpsertNodeHeartbeat inserts or updates a cluster_nodes row for nodeID,
// setting last_heartbeat_at to now(). Safe to call repeatedly.
func (m *Manager) UpsertNodeHeartbeat(ctx context.Context, nodeID string) error {
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

// DeregisterNode clears owner_node on all account_devices owned by nodeID,
// then deletes the cluster_nodes row. Both steps run in a single transaction.
func (m *Manager) DeregisterNode(ctx context.Context, nodeID string) error {
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

// StaleOwnedAccounts returns active accounts whose owner node's heartbeat is
// older than staleness — candidates for takeover.
func (m *Manager) StaleOwnedAccounts(ctx context.Context, staleness time.Duration) ([]string, error) {
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

// ListUnownedActiveAccounts returns active accounts with no owner_node set —
// accounts that were deregistered gracefully and not yet re-adopted.
func (m *Manager) ListUnownedActiveAccounts(ctx context.Context) ([]string, error) {
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
func (m *Manager) GetBoundProxy(ctx context.Context, accountJID string) (*ProxyBinding, error) {
	var proxyID *int64
	var proxyURL *string
	err := m.bizPool.QueryRow(ctx,
		`SELECT proxy_id, proxy_url_cache FROM account_devices WHERE account_jid=$1`,
		accountJID).Scan(&proxyID, &proxyURL)
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
	return &ProxyBinding{
		ProxyID:  id,
		ProxyURL: *proxyURL,
	}, nil
}
