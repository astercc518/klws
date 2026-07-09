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
	// AppTenantDSN is the DSN for the RLS-constrained app_tenant role.
	// Empty → falls back to bizPool (single-DSN dev mode; RLS not enforced).
	AppTenantDSN string
	// AppSystemDSN is the DSN for the app_system role (BYPASSRLS).
	// Empty → falls back to bizPool.
	AppSystemDSN string
	// PoolMode selects the PG connection strategy: "direct" (default) or
	// "pgbouncer" (transaction-pooled: simple/exec protocol).
	PoolMode string
	// QueryMode is the pgx exec mode under pgbouncer: "exec" (default) | "simple".
	QueryMode string
	// Redis is the client used by the redis ownership backend. Must be non-nil.
	Redis *goredis.Client
	// OwnershipTTL is the Redis heartbeat key TTL for the redis ownership
	// backend (a node is considered dead when its hb key expires). Should equal
	// the cluster NodeStaleness. Heartbeat interval MUST be < OwnershipTTL.
	OwnershipTTL time.Duration
	// SessionStore selects the whatsmeow store backend: "pg" (sqlstore, default) or
	// "badger" (local NVMe KV via internal/store/wabadger).
	SessionStore string
	// BadgerDir is the on-disk directory for the Badger session store (SessionStore=="badger").
	BadgerDir string
	// ProxyBackend selects proxy allocation: "pg" (FOR UPDATE SKIP LOCKED, default)
	// or "redis" (proxy:avail:{cc} ZSET cooldown ring). PG stays the durable source.
	ProxyBackend string
	// ProxyCooldown is how long a proxy is ineligible for re-selection after a
	// bind/release (anti-ban: don't reuse an IP within this window).
	ProxyCooldown time.Duration
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
	if c.SessionStore == "" {
		c.SessionStore = "pg"
	}
	if c.BadgerDir == "" {
		c.BadgerDir = "/var/lib/wadist/badger"
	}
	if c.OwnershipTTL <= 0 {
		c.OwnershipTTL = 30 * time.Second
	}
	if c.ProxyBackend == "" {
		c.ProxyBackend = "pg"
	}
	if c.ProxyCooldown <= 0 {
		c.ProxyCooldown = 60 * time.Second
	}
	if c.PoolMode == "" {
		c.PoolMode = "direct"
	}
	if c.QueryMode == "" {
		c.QueryMode = "exec"
	}
}

// validate enforces cross-field constraints after defaults are applied.
// pgbouncer transaction pooling caps MaxOpenConns at the PgBouncer
// client-conn ceiling. (Ownership is unconditionally redis now, so the
// former OwnershipBackend=="redis" requirement here is moot.)
func (c *Config) validate() error {
	if c.PoolMode == "pgbouncer" {
		if c.MaxOpenConns > 200 {
			c.MaxOpenConns = 200
		}
	}
	return nil
}
