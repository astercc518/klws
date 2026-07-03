// internal/store/ownership.go
package store

import (
	"context"
	"time"
)

// LockHandle 是一次账号所有权抢占的句柄。它必须满足 cluster.DeviceLockHandle
// (Healthy/Release)，从而 cluster/node 无需感知底层是 PG 锁还是 Redis 租约。
type LockHandle interface {
	Healthy(ctx context.Context) bool
	Release(ctx context.Context)
}

// Ownership 是可插拔的账号所有权后端：PG advisory 锁 或 Redis 租约。
type Ownership interface {
	// Acquire 抢占 jid 的独占权；被活跃节点持有时返回 ErrDeviceLocked。
	Acquire(ctx context.Context, jid, nodeID string) (LockHandle, error)
	// Heartbeat 刷新本节点活性。
	Heartbeat(ctx context.Context, nodeID string) error
	// Deregister 优雅注销本节点（清其所有权台账）。
	Deregister(ctx context.Context, nodeID string) error
	// StaleOwned 返回归属于已过期节点的账号（接管候选）。
	StaleOwned(ctx context.Context, staleness time.Duration) ([]string, error)
	// Unowned 返回 active 但无主的账号。
	Unowned(ctx context.Context) ([]string, error)
}

// newOwnership constructs the Ownership backend selected by m.cfg.OwnershipBackend.
func newOwnership(m *Manager) Ownership {
	switch m.cfg.OwnershipBackend {
	case "redis":
		return newRedisOwnershipWithRoster(m.cfg.Redis, m.ListActiveAccounts, m.cfg.OwnershipTTL)
	case "shadow":
		return newShadowOwnership(&pgOwnership{m: m}, newRedisOwnershipWithRoster(m.cfg.Redis, m.ListActiveAccounts, m.cfg.OwnershipTTL), m.cfg.OnShadowDivergence)
	default:
		return &pgOwnership{m: m}
	}
}

// backendName returns a short string identifying the concrete Ownership type.
// Used in tests and observability.
func backendName(o Ownership) string {
	switch o.(type) {
	case *pgOwnership:
		return "pg"
	case *redisOwnership:
		return "redis"
	case *shadowOwnership:
		return "shadow"
	default:
		return "unknown"
	}
}
