// Package config loads runtime configuration from the environment and maps it
// onto the lower-level store.Config used by the storage layer.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/acme/wadist/internal/store"
)

// Config is the process-level configuration assembled from environment variables.
type Config struct {
	PostgresDSN         string
	RedisAddr           string
	NodeID              string
	MaxOpenConns        int32 // business pool size
	MaxLockConns        int32 // dedicated advisory-lock pool size (per-node account ceiling)
	MetricsAddr         string
	ShutdownTimeout     time.Duration
	MaxConcurrentStarts int
	// Distributed takeover intervals.
	HeartbeatInterval   time.Duration // how often this node upserts its heartbeat (default 10s)
	NodeStaleness       time.Duration // stale threshold: nodes silent longer than this are dead (default 30s)
	TakeoverScanInterval time.Duration // how often the takeover loop runs (default 15s)
}

// Load reads configuration from the environment. PostgresDSN is required;
// everything else has a sane default.
func Load() (*Config, error) {
	dsn := os.Getenv("WADIST_POSTGRES_DSN")
	if dsn == "" {
		return nil, fmt.Errorf("config: WADIST_POSTGRES_DSN is required")
	}
	cfg := &Config{
		PostgresDSN:  dsn,
		RedisAddr:    getenv("WADIST_REDIS_ADDR", "localhost:6379"),
		NodeID:       getenv("WADIST_NODE_ID", hostnameOr("node-unknown")),
		MaxOpenConns: getenvInt32("WADIST_MAX_OPEN_CONNS", 50),
		MaxLockConns: getenvInt32("WADIST_MAX_LOCK_CONNS", 300),
		MetricsAddr:  getenv("WADIST_METRICS_ADDR", ":9090"),
	}
	cfg.ShutdownTimeout = 30 * time.Second
	if v := getenv("WADIST_SHUTDOWN_TIMEOUT", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.ShutdownTimeout = d
		}
	}
	cfg.MaxConcurrentStarts = 32
	if v := getenv("WADIST_MAX_CONCURRENT_STARTS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxConcurrentStarts = n
		}
	}
	cfg.HeartbeatInterval = 10 * time.Second
	if v := getenv("WADIST_HEARTBEAT_INTERVAL", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.HeartbeatInterval = d
		}
	}
	cfg.NodeStaleness = 30 * time.Second
	if v := getenv("WADIST_NODE_STALENESS", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.NodeStaleness = d
		}
	}
	cfg.TakeoverScanInterval = 15 * time.Second
	if v := getenv("WADIST_TAKEOVER_SCAN_INTERVAL", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.TakeoverScanInterval = d
		}
	}
	return cfg, nil
}

// Store projects the app config onto the storage layer's Config.
func (c *Config) Store() store.Config {
	return store.Config{
		DSN:          c.PostgresDSN,
		MaxOpenConns: c.MaxOpenConns,
		MaxLockConns: c.MaxLockConns,
		NodeID:       c.NodeID,
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt32(key string, def int32) int32 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return def
	}
	return int32(n)
}

func hostnameOr(def string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return def
}
