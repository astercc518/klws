// internal/store/node.go
package store

import (
	"context"
	"fmt"
)

// UpsertNodeHeartbeat inserts or updates a cluster_nodes row for nodeID,
// setting last_heartbeat_at to now(). Safe to call repeatedly.
func (m *Manager) UpsertNodeHeartbeat(ctx context.Context, nodeID string) error {
	_, err := m.bizPool.Exec(ctx, `
INSERT INTO cluster_nodes (node_id, last_heartbeat_at, registered_at, updated_at)
VALUES ($1, now(), now(), now())
ON CONFLICT (node_id) DO UPDATE
    SET last_heartbeat_at = now(),
        updated_at        = now()
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
