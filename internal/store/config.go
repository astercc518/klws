// internal/store/config.go
package store

import (
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Config 连接池配置。MaxOpenConns 匹配 Postgres 承载力,而非 Goroutine 数。
type Config struct {
	DSN             string
	MaxOpenConns    int32
	MinIdleConns    int32
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	NodeID          string
	// MaxLockConns bounds the number of concurrently-held device advisory locks
	// on this node. Each held lock pins one dedicated connection for its full
	// session lifetime, so this value equals the per-node account ceiling.
	MaxLockConns int32
	// AppTenantDSN is the DSN for the RLS-constrained app_tenant role.
	// Empty → falls back to bizPool (single-DSN dev mode; RLS not enforced).
	AppTenantDSN string
	// AppSystemDSN is the DSN for the app_system role (BYPASSRLS).
	// Empty → falls back to bizPool.
	AppSystemDSN string
	// OwnershipBackend selects the ownership implementation: "pg" (default) | "redis" | "shadow".
	OwnershipBackend string
	// Redis is the client used by the redis/shadow ownership backends.
	// Must be non-nil when OwnershipBackend is "redis" or "shadow".
	Redis *goredis.Client
	// OnShadowDivergence is called when the shadow backend detects a divergence
	// between the pg and redis ownership states. Placeholder until Task 6.
	OnShadowDivergence func(op string)
	// SessionStore selects the whatsmeow store backend: "pg" (sqlstore, default) or
	// "badger" (local NVMe KV via internal/store/wabadger).
	SessionStore string
	// BadgerDir is the on-disk directory for the Badger session store (SessionStore=="badger").
	BadgerDir string
}

func (c *Config) withDefaults() {
	if c.MaxOpenConns == 0 {
		c.MaxOpenConns = 50
	}
	if c.MinIdleConns == 0 {
		c.MinIdleConns = 5
	}
	if c.ConnMaxLifetime == 0 {
		c.ConnMaxLifetime = 30 * time.Minute
	}
	if c.ConnMaxIdleTime == 0 {
		c.ConnMaxIdleTime = 5 * time.Minute
	}
	if c.MaxLockConns == 0 {
		c.MaxLockConns = 300
	}
	if c.OwnershipBackend == "" {
		c.OwnershipBackend = "pg"
	}
	if c.SessionStore == "" {
		c.SessionStore = "pg"
	}
	if c.BadgerDir == "" {
		c.BadgerDir = "/var/lib/wadist/badger"
	}
}
