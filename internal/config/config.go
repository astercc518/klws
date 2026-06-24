// Package config loads runtime configuration from the environment and maps it
// onto the lower-level store.Config used by the storage layer.
package config

import (
	"encoding/base64"
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
	HeartbeatInterval    time.Duration // how often this node upserts its heartbeat (default 10s)
	NodeStaleness        time.Duration // stale threshold: nodes silent longer than this are dead (default 30s)
	TakeoverScanInterval time.Duration // how often the takeover loop runs (default 15s)
	// Encryption keys (optional at startup; required when crypto operations are performed).
	MasterKey     []byte // 32-byte KEK decoded from WADIST_MASTER_KEY (base64-std); nil if env unset.
	BlindIndexKey []byte // 32-byte HMAC key decoded from WADIST_BLIND_INDEX_KEY (base64-std); nil if env unset.
	// RLS role DSNs. Empty → falls back to PostgresDSN (single-DSN dev mode).
	AppTenantDSN string // WADIST_APP_TENANT_DSN — role app_tenant (RLS enforced)
	AppSystemDSN string // WADIST_APP_SYSTEM_DSN — role app_system (BYPASSRLS)
	// NodeRegion is the data-residency region for this node (M10 stub; M11 adds per-region pools).
	NodeRegion string // WADIST_NODE_REGION default "default"
	// AsynqConcurrency is the number of concurrent Asynq workers (default 32).
	AsynqConcurrency int // WADIST_ASYNQ_CONCURRENCY default 32
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
	cfg.AppTenantDSN = getenv("WADIST_APP_TENANT_DSN", "")
	cfg.AppSystemDSN = getenv("WADIST_APP_SYSTEM_DSN", "")
	cfg.NodeRegion = getenv("WADIST_NODE_REGION", "default")
	cfg.AsynqConcurrency = 32
	if v := getenv("WADIST_ASYNQ_CONCURRENCY", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.AsynqConcurrency = n
		}
	}

	mk, err := decodeKey32("WADIST_MASTER_KEY")
	if err != nil {
		return nil, err
	}
	cfg.MasterKey = mk
	bik, err := decodeKey32("WADIST_BLIND_INDEX_KEY")
	if err != nil {
		return nil, err
	}
	cfg.BlindIndexKey = bik
	return cfg, nil
}

// Store projects the app config onto the storage layer's Config.
func (c *Config) Store() store.Config {
	return store.Config{
		DSN:          c.PostgresDSN,
		MaxOpenConns: c.MaxOpenConns,
		MaxLockConns: c.MaxLockConns,
		NodeID:       c.NodeID,
		AppTenantDSN: c.AppTenantDSN,
		AppSystemDSN: c.AppSystemDSN,
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

// decodeKey32 reads an env var, base64-std-decodes it, and validates it is
// exactly 32 bytes. Returns nil, nil when the env var is empty (keyless startup).
func decodeKey32(envVar string) ([]byte, error) {
	v := os.Getenv(envVar)
	if v == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, fmt.Errorf("config: %s: base64 decode: %w", envVar, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("config: %s: must decode to exactly 32 bytes, got %d", envVar, len(key))
	}
	return key, nil
}
