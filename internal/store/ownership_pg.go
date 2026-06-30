// internal/store/ownership_pg.go
package store

import (
	"context"
	"time"
)

// pgOwnership 是现状所有权实现：PG session 级 advisory 锁 + cluster_nodes 心跳
// + account_devices.owner_node 台账。行为与重构前字节一致。
type pgOwnership struct{ m *Manager }

func (o *pgOwnership) Acquire(ctx context.Context, jid, nodeID string) (LockHandle, error) {
	return o.m.acquirePGLock(ctx, jid)
}
func (o *pgOwnership) Heartbeat(ctx context.Context, nodeID string) error {
	return o.m.upsertNodeHeartbeatPG(ctx, nodeID)
}
func (o *pgOwnership) Deregister(ctx context.Context, nodeID string) error {
	return o.m.deregisterNodePG(ctx, nodeID)
}
func (o *pgOwnership) StaleOwned(ctx context.Context, staleness time.Duration) ([]string, error) {
	return o.m.staleOwnedAccountsPG(ctx, staleness)
}
func (o *pgOwnership) Unowned(ctx context.Context) ([]string, error) {
	return o.m.listUnownedActiveAccountsPG(ctx)
}
