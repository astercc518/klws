// Package config loads runtime configuration from the environment and maps it
// onto the lower-level store.Config used by the storage layer.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/acme/wadist/internal/store"
)

// Config is the process-level configuration assembled from environment variables.
type Config struct {
	PostgresDSN         string
	RedisAddr           string
	NodeID              string
	MaxOpenConns        int32 // business pool size
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
	// CanaryPercent is the percentage (0..100) of JIDs assigned to the canary cohort.
	// Out-of-range or unparseable values fall back to 0 (feature off).
	CanaryPercent uint8 // WADIST_CANARY_PERCENT default 0
	// PreStopDelay is the time to wait after /readyz → 503 before beginning
	// supervisor shutdown, giving k8s time to remove the endpoint (default 5s).
	PreStopDelay time.Duration // WADIST_PRESTOP_DELAY default 5s
	// Anti-fingerprint / anthropomorphic pacing.
	FenceOnSend bool          // WADIST_OWNERSHIP_FENCE_ON_SEND default "true"
	TypingMin   time.Duration // WADIST_TYPING_MIN_MS default 1200ms
	TypingMax   time.Duration // WADIST_TYPING_MAX_MS default 3500ms
	DwellMin    time.Duration // WADIST_DWELL_MIN_MS default 3000ms
	DwellMax    time.Duration // WADIST_DWELL_MAX_MS default 10000ms
	Linger      time.Duration // WADIST_LINGER_MS default 15000ms
	// Control-plane warm-set configuration.
	WarmTarget      int           // WADIST_WARM_TARGET default 1500
	WSTick          time.Duration // WADIST_WS_TICK_MS default 500ms
	KeepWarmHorizon time.Duration // WADIST_KEEP_WARM_HORIZON_MS default 90000ms
	WarmReqBatch    int           // WADIST_WARMREQ_BATCH default 64
	DailyQuotaMin   int           // WADIST_DAILY_QUOTA_MIN default 5
	DailyQuotaMax   int           // WADIST_DAILY_QUOTA_MAX default 10
	// ProxyJanitor: periodic sweep that auto-rebinds accounts stuck on dead proxies.
	ProxyJanitorInterval time.Duration // WADIST_PROXY_JANITOR_INTERVAL_MS default 30000ms
	ProxyJanitorBatch    int           // WADIST_PROXY_JANITOR_BATCH default 256
	// Reconciler: periodic sweep that re-enqueues active accounts missing from
	// the scheduler's due set (closes the scheduling loop against drift).
	ReconcileInterval time.Duration // WADIST_RECONCILE_INTERVAL_MS default 300000ms (5m)
	// SendRate is the global token-bucket rate (sends/sec) for the pump.
	SendRate float64 // WADIST_SEND_RATE default 160.0
	// PumpBuffer is the in-memory SendPayload channel capacity for the pump.
	PumpBuffer int // WADIST_PUMP_BUFFER default 512
	// SendWorkers is the number of concurrent send workers driving the pump.
	SendWorkers int // WADIST_SEND_WORKERS default 32
	// Risk governor tuning: adaptive-τ AIMD loop, always active, driven by the
	// fleet-wide ban rate.
	// banRate>=GovSLO → τ*=GovFactor (floored at GovMinRate);
	// banRate<GovSLO → τ+=GovStep (capped at SendRate). Evaluated every
	// GovIntervalMs over a GovWindowSec window, skipped when attempted<GovMinSample.
	GovSLO, GovStep, GovFactor, GovMinRate    float64
	GovIntervalMs, GovWindowSec, GovMinSample int
	// Segment governor tuning: L3 per-country segment slowdown layered on top of
	// the L4 risk governor, always active.
	// A country cc is "hot" when its window ban rate >= max(fleetBaseline*SegMult,
	// SegSLO) and its attempted >= SegMinSample; a hot cc's sends are capped at
	// SegSlowRate/s (uncapped again on recovery).
	SegMult, SegSLO, SegSlowRate float64
	SegMinSample                 int
	// Ghost Reaper (M6): bounded reclamation of dead/ghost whatsmeow connections.
	// Always active: auto-reconnect stays off and the reaper loop runs
	// unconditionally. ttl≤30s per design.
	GhostTTL  time.Duration // WADIST_GHOST_TTL_MS default 20000
	GhostMax  int           // WADIST_GHOST_MAX default 128
	GhostTick time.Duration // WADIST_GHOST_TICK_MS default 5000
	// BootRamp spreads the post-restart warm burst; 0 disables (default).
	BootRamp time.Duration // WADIST_BOOT_RAMP_MS default 0
	// Admission backoff (segment-level exponential slowdown on wa_warning),
	// always active.
	BackoffFactor int           // WADIST_BACKOFF_FACTOR default 2
	BackoffMax    int           // WADIST_BACKOFF_MAX default 8
	BackoffTTL    time.Duration // WADIST_BACKOFF_TTL_MS default 300000ms
	// Evolution API 数据面（换栈）。E0 仅装载，未接线。
	EvolutionBaseURL       string
	EvolutionAPIKey        string
	EvolutionWebhookSecret string
	EvolutionNode          string
	// Evolution cutover (E6)
	Sender               string            // WADIST_SENDER: whatsmeow|evolution
	Conn                 string            // WADIST_CONN: whatsmeow|evolution
	EvolutionNodes       map[string]string // node name -> base URL
	EvolutionCapPerNode  int
	EvolutionWebhookURL  string
	EvoLimiterMax        int
	EvoBreakerThreshold  int
	EvoBreakerCooloffSec int
	EvoRetryAttempts     int
	// Metrics sampler (P3): periodic DB-aggregate → metric_snapshots.
	MetricsSampleInterval time.Duration // WADIST_METRICS_SAMPLE_INTERVAL default 15m
	MetricsRetentionDays  int           // WADIST_METRICS_RETENTION_DAYS default 90
}

// Load reads configuration from the environment. PostgresDSN is required;
// everything else has a sane default.
func Load() (*Config, error) {
	dsn := os.Getenv("WADIST_POSTGRES_DSN")
	if dsn == "" {
		return nil, fmt.Errorf("config: WADIST_POSTGRES_DSN is required")
	}
	cfg := &Config{
		PostgresDSN:            dsn,
		RedisAddr:              getenv("WADIST_REDIS_ADDR", "localhost:6379"),
		NodeID:                 getenv("WADIST_NODE_ID", hostnameOr("node-unknown")),
		MaxOpenConns:           getenvInt32("WADIST_MAX_OPEN_CONNS", 50),
		MetricsAddr:            getenv("WADIST_METRICS_ADDR", ":9090"),
		EvolutionBaseURL:       getenv("WADIST_EVOLUTION_BASE_URL", "http://localhost:8080"),
		EvolutionAPIKey:        getenv("WADIST_EVOLUTION_APIKEY", ""),
		EvolutionWebhookSecret: getenv("WADIST_EVOLUTION_WEBHOOK_SECRET", ""),
		EvolutionNode:          getenv("WADIST_EVOLUTION_NODE", "default"),
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
	cfg.PreStopDelay = 5 * time.Second
	if v := getenv("WADIST_PRESTOP_DELAY", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.PreStopDelay = d
		}
	}

	cfg.CanaryPercent = 0
	if v := getenv("WADIST_CANARY_PERCENT", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
			cfg.CanaryPercent = uint8(n)
		}
	}

	cfg.FenceOnSend = getenv("WADIST_OWNERSHIP_FENCE_ON_SEND", "true") != "false"
	cfg.TypingMin = msEnv("WADIST_TYPING_MIN_MS", 1200)
	cfg.TypingMax = msEnv("WADIST_TYPING_MAX_MS", 3500)
	cfg.DwellMin = msEnv("WADIST_DWELL_MIN_MS", 3000)
	cfg.DwellMax = msEnv("WADIST_DWELL_MAX_MS", 10000)
	cfg.Linger = msEnv("WADIST_LINGER_MS", 15000)
	cfg.WarmTarget = intEnv("WADIST_WARM_TARGET", 1500)
	cfg.WSTick = msEnv("WADIST_WS_TICK_MS", 500)
	cfg.KeepWarmHorizon = msEnv("WADIST_KEEP_WARM_HORIZON_MS", 90000)
	cfg.WarmReqBatch = intEnv("WADIST_WARMREQ_BATCH", 64)
	cfg.DailyQuotaMin = intEnv("WADIST_DAILY_QUOTA_MIN", 5)
	cfg.DailyQuotaMax = intEnv("WADIST_DAILY_QUOTA_MAX", 10)
	cfg.ProxyJanitorInterval = msEnv("WADIST_PROXY_JANITOR_INTERVAL_MS", 30000)
	cfg.ProxyJanitorBatch = intEnv("WADIST_PROXY_JANITOR_BATCH", 256)
	cfg.ReconcileInterval = msEnv("WADIST_RECONCILE_INTERVAL_MS", 300000)

	cfg.PumpBuffer = intEnv("WADIST_PUMP_BUFFER", 512)
	cfg.SendWorkers = intEnv("WADIST_SEND_WORKERS", 32)
	cfg.SendRate = floatEnv("WADIST_SEND_RATE", 160.0)

	cfg.GovSLO = floatEnv("WADIST_GOV_SLO", 0.02)
	cfg.GovStep = floatEnv("WADIST_GOV_STEP", 5)
	cfg.GovFactor = floatEnv("WADIST_GOV_FACTOR", 0.5)
	cfg.GovMinRate = floatEnv("WADIST_GOV_MIN_RATE", 1)
	cfg.GovIntervalMs = intEnv("WADIST_GOV_INTERVAL_MS", 20000)
	cfg.GovWindowSec = intEnv("WADIST_GOV_WINDOW_SEC", 900)
	cfg.GovMinSample = intEnv("WADIST_GOV_MIN_SAMPLE", 20)

	cfg.SegMult = floatEnv("WADIST_SEG_MULT", 3)
	cfg.SegSLO = floatEnv("WADIST_SEG_SLO", 0.05)
	cfg.SegSlowRate = floatEnv("WADIST_SEG_SLOW_RATE", 1)
	cfg.SegMinSample = intEnv("WADIST_SEG_MIN_SAMPLE", 10)

	cfg.GhostTTL = msEnv("WADIST_GHOST_TTL_MS", 20000)
	cfg.GhostMax = intEnv("WADIST_GHOST_MAX", 128)
	cfg.GhostTick = msEnv("WADIST_GHOST_TICK_MS", 5000)
	cfg.BootRamp = msEnvZero("WADIST_BOOT_RAMP_MS")

	cfg.BackoffFactor = intEnv("WADIST_BACKOFF_FACTOR", 2)
	cfg.BackoffMax = intEnv("WADIST_BACKOFF_MAX", 8)
	cfg.BackoffTTL = msEnv("WADIST_BACKOFF_TTL_MS", 300000)

	cfg.Sender = getenv("WADIST_SENDER", "evolution")
	cfg.Conn = getenv("WADIST_CONN", "evolution")
	cfg.EvolutionNodes = parseNodes(getenv("WADIST_EVOLUTION_NODES", ""), cfg.EvolutionBaseURL)
	cfg.EvolutionCapPerNode = intEnv("WADIST_EVOLUTION_CAP_PER_NODE", 800)
	cfg.EvolutionWebhookURL = getenv("WADIST_EVOLUTION_WEBHOOK_URL", "")
	cfg.EvoLimiterMax = intEnv("WADIST_EVO_LIMITER_MAX", 1)
	cfg.EvoBreakerThreshold = intEnv("WADIST_EVO_BREAKER_THRESHOLD", 5)
	cfg.EvoBreakerCooloffSec = intEnv("WADIST_EVO_BREAKER_COOLOFF_SEC", 30)
	cfg.EvoRetryAttempts = intEnv("WADIST_EVO_RETRY_ATTEMPTS", 4)

	cfg.MetricsSampleInterval = 15 * time.Minute
	if v := getenv("WADIST_METRICS_SAMPLE_INTERVAL", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.MetricsSampleInterval = d
		}
	}
	cfg.MetricsRetentionDays = intEnv("WADIST_METRICS_RETENTION_DAYS", 90)
	if cfg.MetricsRetentionDays < 1 {
		// A 0/negative value would push Prune's olderThan cutoff into the
		// future, deleting every metric_snapshots row on the very next tick.
		cfg.MetricsRetentionDays = 90
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
		DSN:           c.PostgresDSN,
		MaxOpenConns:  c.MaxOpenConns,
		NodeID:        c.NodeID,
		AppTenantDSN:  c.AppTenantDSN,
		AppSystemDSN:  c.AppSystemDSN,
		BadgerDir:     os.Getenv("WADIST_BADGER_DIR"),
		ProxyCooldown: msEnvZero("WADIST_PROXY_COOLDOWN_MS"),
		// Redis is injected by cmd/wadist after Store() returns.
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnv(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func floatEnv(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func msEnv(key string, def int) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Millisecond
		}
	}
	return time.Duration(def) * time.Millisecond
}

// msEnvZero reads key as milliseconds and returns 0 (not a hardcoded default)
// when the env var is empty or unparseable, so the caller's own zero-value
// default (e.g. store.Config.withDefaults) applies instead.
func msEnvZero(key string) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 0
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

// parseNodes parses "n1=url1,n2=url2" into a node->URL map. Empty/malformed
// entries are skipped. An empty result falls back to {"default": fallbackURL}.
func parseNodes(s, fallbackURL string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		i := strings.IndexByte(pair, '=')
		if i <= 0 || i == len(pair)-1 {
			continue // no '=', empty name, or empty url
		}
		out[strings.TrimSpace(pair[:i])] = strings.TrimSpace(pair[i+1:])
	}
	if len(out) == 0 {
		return map[string]string{"default": fallbackURL}
	}
	return out
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
