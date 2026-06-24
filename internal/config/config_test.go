package config

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestLoad_TakeoverIntervalDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_HEARTBEAT_INTERVAL", "")
	t.Setenv("WADIST_NODE_STALENESS", "")
	t.Setenv("WADIST_TAKEOVER_SCAN_INTERVAL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HeartbeatInterval != 10*time.Second {
		t.Fatalf("HeartbeatInterval default: got %v, want 10s", cfg.HeartbeatInterval)
	}
	if cfg.NodeStaleness != 30*time.Second {
		t.Fatalf("NodeStaleness default: got %v, want 30s", cfg.NodeStaleness)
	}
	if cfg.TakeoverScanInterval != 15*time.Second {
		t.Fatalf("TakeoverScanInterval default: got %v, want 15s", cfg.TakeoverScanInterval)
	}
}

func TestLoad_MetricsAddrDefault(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_METRICS_ADDR", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsAddr != ":9090" {
		t.Fatalf("want :9090, got %q", cfg.MetricsAddr)
	}
}

func TestLoad_ShutdownAndConcurrencyDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_SHUTDOWN_TIMEOUT", "")
	t.Setenv("WADIST_MAX_CONCURRENT_STARTS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Fatalf("shutdown default: got %v", cfg.ShutdownTimeout)
	}
	if cfg.MaxConcurrentStarts != 32 {
		t.Fatalf("starts default: got %d", cfg.MaxConcurrentStarts)
	}
}

func TestLoad_RequiresDSN(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when WADIST_POSTGRES_DSN is unset")
	}
}

func TestLoad_DefaultsAndStoreMapping(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_REDIS_ADDR", "")
	t.Setenv("WADIST_NODE_ID", "node-7")
	t.Setenv("WADIST_MAX_OPEN_CONNS", "")
	t.Setenv("WADIST_MAX_LOCK_CONNS", "200")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.RedisAddr != "localhost:6379" {
		t.Fatalf("RedisAddr default = %q", c.RedisAddr)
	}
	if c.MaxOpenConns != 50 {
		t.Fatalf("MaxOpenConns default = %d, want 50", c.MaxOpenConns)
	}
	if c.MaxLockConns != 200 {
		t.Fatalf("MaxLockConns = %d, want 200", c.MaxLockConns)
	}

	sc := c.Store()
	if sc.DSN != "postgres://x" || sc.NodeID != "node-7" || sc.MaxLockConns != 200 {
		t.Fatalf("Store() mapping wrong: %+v", sc)
	}
}

func TestLoad_DecodesKeys(t *testing.T) {
	// 32 arbitrary bytes encoded as base64-std.
	mk := make([]byte, 32)
	for i := range mk {
		mk[i] = byte(i + 1)
	}
	bik := make([]byte, 32)
	for i := range bik {
		bik[i] = byte(i + 33)
	}
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_MASTER_KEY", base64.StdEncoding.EncodeToString(mk))
	t.Setenv("WADIST_BLIND_INDEX_KEY", base64.StdEncoding.EncodeToString(bik))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.MasterKey) != 32 {
		t.Fatalf("MasterKey: expected 32 bytes, got %d", len(cfg.MasterKey))
	}
	if len(cfg.BlindIndexKey) != 32 {
		t.Fatalf("BlindIndexKey: expected 32 bytes, got %d", len(cfg.BlindIndexKey))
	}
}

func TestLoad_EmptyKeyEnv_NilNoError(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_MASTER_KEY", "")
	t.Setenv("WADIST_BLIND_INDEX_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with empty key envs: %v", err)
	}
	if cfg.MasterKey != nil {
		t.Fatalf("MasterKey should be nil when env is empty, got %v", cfg.MasterKey)
	}
	if cfg.BlindIndexKey != nil {
		t.Fatalf("BlindIndexKey should be nil when env is empty, got %v", cfg.BlindIndexKey)
	}
}

func TestLoad_InvalidKeyLength_ReturnsError(t *testing.T) {
	// Encode only 16 bytes (wrong length).
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_MASTER_KEY", short)
	t.Setenv("WADIST_BLIND_INDEX_KEY", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected error for 16-byte WADIST_MASTER_KEY")
	}
}
