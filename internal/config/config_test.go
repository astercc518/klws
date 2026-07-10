package config

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestLoad_PreStopDelayDefault(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_PRESTOP_DELAY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PreStopDelay != 5*time.Second {
		t.Fatalf("PreStopDelay default: got %v, want 5s", cfg.PreStopDelay)
	}
}

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

	sc := c.Store()
	if sc.DSN != "postgres://x" || sc.NodeID != "node-7" {
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

func TestLoad_NodeRegionDefault(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_NODE_REGION", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NodeRegion != "default" {
		t.Fatalf("NodeRegion default: got %q, want %q", cfg.NodeRegion, "default")
	}
}

func TestLoad_NodeRegionCustom(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_NODE_REGION", "eu-west-1")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NodeRegion != "eu-west-1" {
		t.Fatalf("NodeRegion: got %q, want %q", cfg.NodeRegion, "eu-west-1")
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

func TestLoad_CanaryPercentDefault(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_CANARY_PERCENT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CanaryPercent != 0 {
		t.Fatalf("CanaryPercent default: got %d, want 0", cfg.CanaryPercent)
	}
}

func TestLoad_CanaryPercentValid(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_CANARY_PERCENT", "25")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CanaryPercent != 25 {
		t.Fatalf("CanaryPercent: got %d, want 25", cfg.CanaryPercent)
	}
}

func TestLoad_CanaryPercentOutOfRange(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_CANARY_PERCENT", "150")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CanaryPercent != 0 {
		t.Fatalf("CanaryPercent out-of-range should be 0, got %d", cfg.CanaryPercent)
	}
}

func TestLoad_ControlDefaults(t *testing.T) {
	t.Setenv("WADIST_REDIS_ADDR", "x:1")
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WarmTarget != 1500 {
		t.Fatalf("WarmTarget=%d want 1500", cfg.WarmTarget)
	}
	if cfg.WSTick != 500*time.Millisecond {
		t.Fatalf("WSTick=%v", cfg.WSTick)
	}
}

func TestLoad_ProxyJanitorDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_REDIS_ADDR", "x:1")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProxyJanitorInterval != 30*time.Second {
		t.Fatalf("ProxyJanitorInterval=%v want 30s", cfg.ProxyJanitorInterval)
	}
	if cfg.ProxyJanitorBatch != 256 {
		t.Fatalf("ProxyJanitorBatch=%d want 256", cfg.ProxyJanitorBatch)
	}
}

func TestLoad_ReconcileDefault(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_REDIS_ADDR", "x:1")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReconcileInterval != 5*time.Minute {
		t.Fatalf("ReconcileInterval=%v want 5m", cfg.ReconcileInterval)
	}
}

func TestConfig_DispatchDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PumpBuffer != 512 {
		t.Fatalf("PumpBuffer default = %d; want 512", cfg.PumpBuffer)
	}
	if cfg.SendWorkers != 32 {
		t.Fatalf("SendWorkers default = %d; want 32", cfg.SendWorkers)
	}
	if cfg.SendRate != 160.0 {
		t.Fatalf("SendRate default = %v; want 160.0", cfg.SendRate)
	}
}

func TestLoad_AntifpDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_REDIS_ADDR", "x:1")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.FenceOnSend {
		t.Fatal("FenceOnSend 默认应为 true")
	}
	if cfg.TypingMin != 1200*time.Millisecond || cfg.TypingMax != 3500*time.Millisecond {
		t.Fatalf("typing 默认 = %v/%v", cfg.TypingMin, cfg.TypingMax)
	}
	if cfg.Linger != 15*time.Second {
		t.Fatalf("linger 默认 = %v", cfg.Linger)
	}
}

func TestConfig_RiskGovernorDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GovSLO != 0.02 || cfg.GovFactor != 0.5 || cfg.GovStep != 5 || cfg.GovMinRate != 1 {
		t.Fatalf("AIMD defaults wrong: slo=%v step=%v factor=%v min=%v", cfg.GovSLO, cfg.GovStep, cfg.GovFactor, cfg.GovMinRate)
	}
	if cfg.GovIntervalMs != 20000 || cfg.GovWindowSec != 900 || cfg.GovMinSample != 20 {
		t.Fatalf("window defaults wrong: interval=%d window=%d sample=%d", cfg.GovIntervalMs, cfg.GovWindowSec, cfg.GovMinSample)
	}
}

func TestConfig_SegmentGovernorDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SegMult != 3 || cfg.SegSLO != 0.05 || cfg.SegSlowRate != 1 {
		t.Fatalf("seg AIMD defaults wrong: mult=%v slo=%v slow=%v", cfg.SegMult, cfg.SegSLO, cfg.SegSlowRate)
	}
	if cfg.SegMinSample != 10 {
		t.Fatalf("SegMinSample = %d; want 10", cfg.SegMinSample)
	}
}

func TestLoad_EvolutionDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_EVOLUTION_BASE_URL", "")
	t.Setenv("WADIST_EVOLUTION_NODE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EvolutionBaseURL != "http://localhost:8080" {
		t.Fatalf("base url default = %q", cfg.EvolutionBaseURL)
	}
	if cfg.EvolutionNode != "default" {
		t.Fatalf("node default = %q", cfg.EvolutionNode)
	}
}
