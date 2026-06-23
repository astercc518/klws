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
}
