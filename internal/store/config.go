// internal/store/config.go
package store

import "time"

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
}
