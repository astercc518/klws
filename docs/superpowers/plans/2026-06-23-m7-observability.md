# M7 可观测性 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为系统提供有界基数的 Prometheus 指标、DB 聚合采集器(TTL 缓存)、发送链路业务埋点,以及承载 `/metrics` 的首个可运行二进制 `cmd/wadist`。

**Architecture:** 新增 `internal/metrics` 包,集中持有所有 collector(全部注册到一个注入的 `*prometheus.Registry`),并暴露 nil-safe 的记录方法。业务包通过 `WithMetrics(*metrics.Metrics)` setter 注入(不改已合并构造函数签名,nil 时全部 no-op,已合并测试零回归)。`DBCollector` 实现 `prometheus.Collector`,按 TTL 缓存聚合快照,避免每次抓取打 PG。`cmd/wadist/main.go` 把 config→store→repos→metrics→DBCollector→/metrics HTTP→asynq worker 串起来(完整生命周期/优雅停机留给 M8)。

**Tech Stack:** Go 1.26.4、`github.com/prometheus/client_golang`、pgx/v5 pgxpool、`net/http`+`promhttp`、hibiken/asynq、testcontainers(postgres:16/redis:7)。

## Global Constraints

- **高基数护栏(硬门禁)**:`scripts/check_metric_labels.sh` 禁止 `jid`、`tenant_id`、`message_id`、`phone` 作为 `WithLabelValues(...)` 的实参 token。所有 label 值只能取有界枚举:`ban_status`(active/flagged/banned/logged_out/init)、`signal`(undelivered/recipient_block/wa_warning/conn_churn/delivered)、gate `reason`(ok/not_active/quarantined/daily_quota/pacing)、charge `state`(held/settled/refund_pending/refunded/rejected)、recipient `state`(pending/sent/failed/skipped)、`country_code`(有界)、`outcome`/`op` 固定字符串。**绝不**按账号/租户/消息/号码分维。
- 所有 DB 调用必须传 `ctx`;错误用 `%w` 包裹;SQL 仅用 `$N` 占位符,禁止字符串拼接。
- 集成测试用真实 PG16(+Redis7)经 testcontainers;`-race` 必须干净;短模式 `t.Skip("integration")`。
- 已合并业务包(billing/sendgate/dispatch/store)的构造函数签名**不得更改**;指标注入一律通过 nil-safe setter。
- 所有 collector 注册到**注入的** `*prometheus.Registry`(不用 `prometheus.DefaultRegisterer` 全局态),便于测试隔离。
- module `github.com/acme/wadist`,Go 1.26.4,二进制 `/usr/local/bin/go`。

---

## File Structure

- `internal/metrics/metrics.go` — `Metrics` 结构(全部 CounterVec/Histogram + nil-safe 记录方法)、`New(reg)`、`RegistrySnapshotProvider` 接口。
- `internal/metrics/metrics_test.go` — 单元测试(注册无 panic、label 有界、Gather 含预期指标名)。
- `internal/metrics/dbcollector.go` — `DBCollector`(实现 `prometheus.Collector`,TTL 缓存聚合快照)。
- `internal/metrics/dbcollector_test.go` — 集成测试(真实 PG,seed→Collect→断言 gauge 值)。
- `internal/metrics/server.go` — `Server`(`/metrics` + `/healthz`,优雅启停)+ `Handler()`(供 httptest)。
- `internal/metrics/server_test.go` — httptest 单元测试。
- `internal/config/config.go` — 增加 `MetricsAddr`(env `WADIST_METRICS_ADDR`,默认 `:9090`)。
- `internal/dispatch/worker.go` / `batch.go` / `types.go` — 增加 nil-safe `*metrics.Metrics` 字段 + `WithMetrics` setter + 各 outcome 分支记录调用。
- `internal/dispatch/metrics_wiring_test.go` — 集成测试(真实 registry,断言计数增量)。
- `cmd/wadist/main.go` — 首个二进制:装配 + 启动 `/metrics` + asynq worker。
- `cmd/wadist/main_smoke_test.go` — 启动冒烟测试(临时端口起 metrics server,GET /metrics 200)。

---

### Task 1: metrics 核心包(collector 集合 + nil-safe 记录 + 注册桩)

**Files:**
- Create: `internal/metrics/metrics.go`
- Create: `internal/metrics/metrics_test.go`
- Modify: `internal/config/config.go`(增加 `MetricsAddr`)
- Modify: `go.mod` / `go.sum`(`go get github.com/prometheus/client_golang`)

**Interfaces:**
- Produces: `type Metrics struct{...}`;`func New(reg *prometheus.Registry) *Metrics`;`func (m *Metrics) Registry() *prometheus.Registry`;nil-safe 记录方法 `RecordSend(outcome string)`、`RecordGate(allow bool, reason string)`、`RecordHealthSignal(signal string)`、`RecordBilling(op, outcome string)`、`ObserveBatch(assigned int)`、`IncNoCapacity()`、`RecordProxy(op, outcome string)`、`RecordLock(outcome string)`;`type RegistrySnapshotProvider interface { ActiveSessions() int }`。
- Consumes: 无(基础任务)。

- [ ] **Step 1: 添加依赖**

```bash
cd /var/klwa
/usr/local/bin/go get github.com/prometheus/client_golang@latest
```

- [ ] **Step 2: 写 config 失败测试(MetricsAddr 默认值)**

在 `internal/config/config_test.go`(若不存在则创建)追加:

```go
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
```

- [ ] **Step 3: 改 config.go 加 MetricsAddr**

在 `Config` 结构增加字段 `MetricsAddr string`,在 `Load()` 中加载:

```go
// 在 Config 结构体中新增：
MetricsAddr string

// 在 Load() 中（沿用现有 env 读取风格）：
cfg.MetricsAddr = getenvDefault("WADIST_METRICS_ADDR", ":9090")
```

> 读现有 `Load()` 用的 env 辅助函数名(可能是 `getenv`/`envOr` 等),用同名函数,不要新造风格。

- [ ] **Step 4: 跑 config 测试**

Run: `/usr/local/bin/go test ./internal/config/ -run TestLoad_MetricsAddrDefault -v`
Expected: PASS

- [ ] **Step 5: 写 metrics 失败测试**

`internal/metrics/metrics_test.go`:

```go
package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestNew_RegistersAndRecords(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	if m.Registry() != reg {
		t.Fatal("Registry() mismatch")
	}
	// 有界 label 记录，不得 panic
	m.RecordSend("sent")
	m.RecordGate(false, "daily_quota")
	m.RecordHealthSignal("wa_warning")
	m.RecordBilling("hold", "insufficient_funds")
	m.ObserveBatch(7)
	m.IncNoCapacity()
	m.RecordProxy("bind", "ok")
	m.RecordLock("contended")

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	for _, want := range []string{
		"wadist_send_outcomes_total",
		"wadist_gate_decisions_total",
		"wadist_health_signals_total",
		"wadist_billing_ops_total",
		"wadist_dispatch_batch_assigned",
		"wadist_dispatch_no_capacity_total",
		"wadist_proxy_ops_total",
		"wadist_lock_ops_total",
	} {
		if !names[want] {
			t.Errorf("missing metric %q", want)
		}
	}
	_ = dto.MetricType_COUNTER
	_ = strings.TrimSpace
}

func TestNilSafe(t *testing.T) {
	var m *Metrics // nil
	// 所有记录方法在 nil receiver 下必须安全 no-op
	m.RecordSend("sent")
	m.RecordGate(true, "ok")
	m.RecordHealthSignal("delivered")
	m.RecordBilling("settle", "ok")
	m.ObserveBatch(1)
	m.IncNoCapacity()
	m.RecordProxy("release", "ok")
	m.RecordLock("acquired")
}
```

- [ ] **Step 6: 跑测试确认失败(包不存在)**

Run: `/usr/local/bin/go test ./internal/metrics/ -run TestNew_RegistersAndRecords -v`
Expected: FAIL(build error / undefined New)

- [ ] **Step 7: 写 metrics.go**

```go
// Package metrics holds all Prometheus collectors for the distribution system.
// Label values are STRICTLY bounded enums — never jid/tenant_id/message_id/phone
// (enforced by scripts/check_metric_labels.sh). All collectors register on an
// injected registry so tests stay isolated and there is no global state.
package metrics

import "github.com/prometheus/client_golang/prometheus"

// RegistrySnapshotProvider is satisfied by the M8 account Registry. M7 wires a
// nil provider; the DBCollector treats nil as "0 active sessions".
type RegistrySnapshotProvider interface {
	ActiveSessions() int
}

type Metrics struct {
	reg *prometheus.Registry

	sendOutcomes  *prometheus.CounterVec // label: outcome
	gateDecisions *prometheus.CounterVec // labels: allow, reason
	healthSignals *prometheus.CounterVec // label: signal
	billingOps    *prometheus.CounterVec // labels: op, outcome
	batchAssigned prometheus.Histogram
	noCapacity    prometheus.Counter
	proxyOps      *prometheus.CounterVec // labels: op, outcome
	lockOps       *prometheus.CounterVec // label: outcome
}

func New(reg *prometheus.Registry) *Metrics {
	m := &Metrics{reg: reg}
	m.sendOutcomes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_send_outcomes_total",
		Help: "Send pipeline outcomes by terminal classification.",
	}, []string{"outcome"})
	m.gateDecisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_gate_decisions_total",
		Help: "Admission gate decisions.",
	}, []string{"allow", "reason"})
	m.healthSignals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_health_signals_total",
		Help: "Applied account health signals.",
	}, []string{"signal"})
	m.billingOps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_billing_ops_total",
		Help: "Billing operations by op and outcome.",
	}, []string{"op", "outcome"})
	m.batchAssigned = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "wadist_dispatch_batch_assigned",
		Help:    "Recipients assigned per dispatchBatch call.",
		Buckets: []float64{0, 1, 5, 10, 25, 50, 100, 250, 500},
	})
	m.noCapacity = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "wadist_dispatch_no_capacity_total",
		Help: "Recipients left pending due to ErrNoCapacity.",
	})
	m.proxyOps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_proxy_ops_total",
		Help: "Proxy pool operations by op and outcome.",
	}, []string{"op", "outcome"})
	m.lockOps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_lock_ops_total",
		Help: "Advisory device-lock acquisition outcomes.",
	}, []string{"outcome"})

	reg.MustRegister(
		m.sendOutcomes, m.gateDecisions, m.healthSignals, m.billingOps,
		m.batchAssigned, m.noCapacity, m.proxyOps, m.lockOps,
	)
	return m
}

func (m *Metrics) Registry() *prometheus.Registry { return m.reg }

// ---- nil-safe recorders (business packages inject *Metrics via WithMetrics) ----

func (m *Metrics) RecordSend(outcome string) {
	if m == nil {
		return
	}
	m.sendOutcomes.WithLabelValues(outcome).Inc()
}

func (m *Metrics) RecordGate(allow bool, reason string) {
	if m == nil {
		return
	}
	a := "false"
	if allow {
		a = "true"
	}
	m.gateDecisions.WithLabelValues(a, reason).Inc()
}

func (m *Metrics) RecordHealthSignal(signal string) {
	if m == nil {
		return
	}
	m.healthSignals.WithLabelValues(signal).Inc()
}

func (m *Metrics) RecordBilling(op, outcome string) {
	if m == nil {
		return
	}
	m.billingOps.WithLabelValues(op, outcome).Inc()
}

func (m *Metrics) ObserveBatch(assigned int) {
	if m == nil {
		return
	}
	m.batchAssigned.Observe(float64(assigned))
}

func (m *Metrics) IncNoCapacity() {
	if m == nil {
		return
	}
	m.noCapacity.Inc()
}

func (m *Metrics) RecordProxy(op, outcome string) {
	if m == nil {
		return
	}
	m.proxyOps.WithLabelValues(op, outcome).Inc()
}

func (m *Metrics) RecordLock(outcome string) {
	if m == nil {
		return
	}
	m.lockOps.WithLabelValues(outcome).Inc()
}
```

- [ ] **Step 8: 跑 metrics 测试 + 高基数门禁**

Run: `/usr/local/bin/go test ./internal/metrics/ -v && ./scripts/check_metric_labels.sh`
Expected: PASS;门禁输出 `OK: no high-cardinality metric labels found`

- [ ] **Step 9: 提交**

```bash
git add internal/metrics/metrics.go internal/metrics/metrics_test.go internal/config/ go.mod go.sum
git commit -m "feat(metrics): bounded-label collector set + nil-safe recorders + MetricsAddr config"
```

---

### Task 2: DBCollector(TTL 缓存的聚合 gauge)

**Files:**
- Create: `internal/metrics/dbcollector.go`
- Create: `internal/metrics/dbcollector_test.go`

**Interfaces:**
- Consumes: `RegistrySnapshotProvider`(Task 1)。
- Produces: `type DBCollector struct{...}`;`func NewDBCollector(pool *pgxpool.Pool, ttl time.Duration, snap RegistrySnapshotProvider) *DBCollector`;实现 `prometheus.Collector`(`Describe`/`Collect`)。

- [ ] **Step 1: 写失败测试(真实 PG)**

`internal/metrics/dbcollector_test.go`:

```go
package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// pgPool + applyMigrations + seed helpers are provided by dbcollector_testsupport_test.go
// (mirror the testcontainers pattern from internal/dispatch/testsupport_test.go).

func TestDBCollector_Gauges(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	// seed: one active healthy account, one wallet, one alive proxy, one pending recipient
	seedCollectorFixture(t, ctx, pool)

	c := NewDBCollector(pool, time.Minute, nil) // nil snapshot provider → 0 active sessions
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	// account_devices ban_status gauge for 'active' should be >= 1
	got := testutil.ToFloat64(activeAccountsProbe(t, reg))
	if got < 1 {
		t.Fatalf("expected >=1 active account gauge, got %v", got)
	}
	// active sessions gauge with nil provider == 0
	if v := gaugeValue(t, reg, "wadist_active_sessions"); v != 0 {
		t.Fatalf("nil provider should yield 0 active sessions, got %v", v)
	}
	// pending recipients gauge >= 1
	if v := gaugeValue(t, reg, "wadist_recipients_state"); v < 0 {
		t.Fatalf("unexpected recipients gauge %v", v)
	}
	_ = context.Background
}
```

> **实现说明给 implementer:** `activeAccountsProbe`/`gaugeValue` 是测试辅助;用 `reg.Gather()` 遍历 metric family 取指定 name(+label `ban_status="active"` / `state="pending"`)的 gauge 值即可。`pgPool`/`applyMigrations`/`seedCollectorFixture` 放入 `internal/metrics/dbcollector_testsupport_test.go`,完全照搬 `internal/dispatch/testsupport_test.go` 的 testcontainers 写法(glob `../../migrations/*.sql`)。seed 至少插入:1 行 `account_devices`(ban_status='active', health_score=100, 需先插 proxy_pool 满足 FK)、1 行 `tenant_wallets`、1 行 alive `proxy_pool`、1 个 campaign + 1 个 pending `campaign_recipients`。

- [ ] **Step 2: 跑测试确认失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./internal/metrics/ -run TestDBCollector_Gauges -v`
Expected: FAIL(undefined NewDBCollector)

- [ ] **Step 3: 写 dbcollector.go**

```go
package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// DBCollector exposes aggregate gauges sourced from Postgres. It implements
// prometheus.Collector and caches the snapshot for `ttl` so a scrape storm
// cannot hammer the database. All gauges use bounded labels only.
type DBCollector struct {
	pool *pgxpool.Pool
	ttl  time.Duration
	snap RegistrySnapshotProvider

	mu      sync.Mutex
	cached  []prometheus.Metric
	expires time.Time

	descAccounts   *prometheus.Desc // labels: ban_status
	descHealth     *prometheus.Desc // labels: bucket
	descWalletBal  *prometheus.Desc
	descWalletFroz *prometheus.Desc
	descWalletLock *prometheus.Desc
	descProxyUsed  *prometheus.Desc
	descProxyFree  *prometheus.Desc
	descProxyDead  *prometheus.Desc
	descRecipients *prometheus.Desc // labels: state
	descCharges    *prometheus.Desc // labels: state
	descRefundPend *prometheus.Desc
	descSessions   *prometheus.Desc
}

func NewDBCollector(pool *pgxpool.Pool, ttl time.Duration, snap RegistrySnapshotProvider) *DBCollector {
	return &DBCollector{
		pool: pool, ttl: ttl, snap: snap,
		descAccounts:   prometheus.NewDesc("wadist_accounts", "Account count by ban_status.", []string{"ban_status"}, nil),
		descHealth:     prometheus.NewDesc("wadist_accounts_health", "Active account count by health bucket.", []string{"bucket"}, nil),
		descWalletBal:  prometheus.NewDesc("wadist_wallet_balance_total", "Sum of tenant wallet available balance.", nil, nil),
		descWalletFroz: prometheus.NewDesc("wadist_wallet_frozen_total", "Sum of tenant wallet frozen funds.", nil, nil),
		descWalletLock: prometheus.NewDesc("wadist_wallet_locked", "Count of locked wallets (reconciliation drift).", nil, nil),
		descProxyUsed:  prometheus.NewDesc("wadist_proxy_slots_used", "Bound proxy slots (alive proxies).", nil, nil),
		descProxyFree:  prometheus.NewDesc("wadist_proxy_slots_free", "Free proxy slots (alive proxies).", nil, nil),
		descProxyDead:  prometheus.NewDesc("wadist_proxy_dead", "Count of dead proxies.", nil, nil),
		descRecipients: prometheus.NewDesc("wadist_recipients_state", "Campaign recipients by state.", []string{"state"}, nil),
		descCharges:    prometheus.NewDesc("wadist_charges_state", "Billing charges by state.", []string{"state"}, nil),
		descRefundPend: prometheus.NewDesc("wadist_refunds_pending", "Pending refund requests awaiting admin review.", nil, nil),
		descSessions:   prometheus.NewDesc("wadist_active_sessions", "Active account sessions (from M8 Registry; 0 until wired).", nil, nil),
	}
}

func (c *DBCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.descAccounts
	ch <- c.descHealth
	ch <- c.descWalletBal
	ch <- c.descWalletFroz
	ch <- c.descWalletLock
	ch <- c.descProxyUsed
	ch <- c.descProxyFree
	ch <- c.descProxyDead
	ch <- c.descRecipients
	ch <- c.descCharges
	ch <- c.descRefundPend
	ch <- c.descSessions
}

func (c *DBCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.cached == nil || now.After(c.expires) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		metrics := c.snapshot(ctx)
		cancel()
		if metrics != nil { // keep stale cache on transient DB error
			c.cached = metrics
			c.expires = now.Add(c.ttl)
		}
	}
	for _, m := range c.cached {
		ch <- m
	}
}

// snapshot runs the aggregate queries. On any query error it returns nil so the
// caller keeps the previous (stale) cache rather than emitting partial data.
func (c *DBCollector) snapshot(ctx context.Context) []prometheus.Metric {
	var out []prometheus.Metric
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		out = append(out, prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...))
	}

	// accounts by ban_status
	rows, err := c.pool.Query(ctx, `SELECT ban_status::text, COUNT(*) FROM account_devices GROUP BY ban_status`)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var s string
		var n float64
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil
		}
		gauge(c.descAccounts, n, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil
	}

	// active accounts by health bucket
	rows, err = c.pool.Query(ctx, `
SELECT CASE WHEN health_score >= 80 THEN 'healthy'
            WHEN health_score >= 40 THEN 'degraded'
            ELSE 'at_risk' END AS bucket, COUNT(*)
  FROM account_devices WHERE ban_status='active' GROUP BY bucket`)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var b string
		var n float64
		if err := rows.Scan(&b, &n); err != nil {
			rows.Close()
			return nil
		}
		gauge(c.descHealth, n, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil
	}

	// wallets
	var bal, froz, locked float64
	if err := c.pool.QueryRow(ctx, `
SELECT COALESCE(SUM(balance),0), COALESCE(SUM(frozen),0),
       COUNT(*) FILTER (WHERE locked=TRUE)
  FROM tenant_wallets`).Scan(&bal, &froz, &locked); err != nil {
		return nil
	}
	gauge(c.descWalletBal, bal)
	gauge(c.descWalletFroz, froz)
	gauge(c.descWalletLock, locked)

	// proxy pool (alive utilization + dead count)
	var used, free, dead float64
	if err := c.pool.QueryRow(ctx, `
SELECT COALESCE(SUM(current_bindings) FILTER (WHERE is_alive),0),
       COALESCE(SUM(max_bindings - current_bindings) FILTER (WHERE is_alive),0),
       COUNT(*) FILTER (WHERE is_alive=FALSE)
  FROM proxy_pool`).Scan(&used, &free, &dead); err != nil {
		return nil
	}
	gauge(c.descProxyUsed, used)
	gauge(c.descProxyFree, free)
	gauge(c.descProxyDead, dead)

	// recipients by state
	rows, err = c.pool.Query(ctx, `SELECT state::text, COUNT(*) FROM campaign_recipients GROUP BY state`)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var s string
		var n float64
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil
		}
		gauge(c.descRecipients, n, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil
	}

	// charges by state
	rows, err = c.pool.Query(ctx, `SELECT state::text, COUNT(*) FROM billing_charges GROUP BY state`)
	if err != nil {
		return nil
	}
	for rows.Next() {
		var s string
		var n float64
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil
		}
		gauge(c.descCharges, n, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil
	}

	// pending refunds
	var refundPend float64
	if err := c.pool.QueryRow(ctx, `SELECT COUNT(*) FROM refund_requests WHERE state='pending'`).Scan(&refundPend); err != nil {
		return nil
	}
	gauge(c.descRefundPend, refundPend)

	// active sessions from M8 Registry (nil → 0)
	sessions := 0.0
	if c.snap != nil {
		sessions = float64(c.snap.ActiveSessions())
	}
	gauge(c.descSessions, sessions)

	return out
}
```

> **校验 implementer:** 上面所有列/表名须与迁移一致(已侦察确认:`account_devices.ban_status/health_score`、`tenant_wallets.balance/frozen/locked`、`proxy_pool.is_alive/current_bindings/max_bindings`、`campaign_recipients.state`、`billing_charges.state`、`refund_requests.state`)。若某列名实际不同,以迁移文件为准并在报告中说明。

- [ ] **Step 4: 跑集成测试**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./internal/metrics/ -race -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/metrics/dbcollector.go internal/metrics/dbcollector_test.go internal/metrics/dbcollector_testsupport_test.go
git commit -m "feat(metrics): DBCollector with TTL-cached aggregate gauges"
```

---

### Task 3: `/metrics` HTTP Server(优雅启停 + healthz)

**Files:**
- Create: `internal/metrics/server.go`
- Create: `internal/metrics/server_test.go`

**Interfaces:**
- Consumes: `*prometheus.Registry`(Task 1 `Metrics.Registry()`)。
- Produces: `type Server struct{...}`;`func NewServer(addr string, reg *prometheus.Registry) *Server`;`func (s *Server) Handler() http.Handler`;`func (s *Server) Start() error`(绑定后后台 serve);`func (s *Server) Addr() string`(实际监听地址,支持 `:0`);`func (s *Server) Shutdown(ctx context.Context) error`。

- [ ] **Step 1: 写失败测试(httptest + 真实端口)**

`internal/metrics/server_test.go`:

```go
package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestServer_Handler_MetricsAndHealthz(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.RecordSend("sent")
	s := NewServer(":0", reg)

	// /healthz
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthz: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// /metrics contains our metric
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "wadist_send_outcomes_total") {
		t.Fatalf("metrics: code=%d missing series", rec.Code)
	}
}

func TestServer_StartShutdown(t *testing.T) {
	reg := prometheus.NewRegistry()
	New(reg)
	s := NewServer("127.0.0.1:0", reg)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + s.Addr() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.TrimSpace(string(b)) != "ok" {
		t.Fatalf("live healthz: %d %q", resp.StatusCode, b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
```

- [ ] **Step 2: 跑确认失败**

Run: `/usr/local/bin/go test ./internal/metrics/ -run TestServer -v`
Expected: FAIL(undefined NewServer)

- [ ] **Step 3: 写 server.go**

```go
package metrics

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server hosts /metrics (scoped to the injected registry) and /healthz.
type Server struct {
	addr string
	reg  *prometheus.Registry
	srv  *http.Server
	ln   net.Listener
}

func NewServer(addr string, reg *prometheus.Registry) *Server {
	return &Server{addr: addr, reg: reg}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// Start binds the listener synchronously (so Addr() is valid on return) and
// serves in a background goroutine.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.addr
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
```

- [ ] **Step 4: 跑测试**

Run: `/usr/local/bin/go test ./internal/metrics/ -race -v`
Expected: PASS(全部 metrics 包测试)

- [ ] **Step 5: 提交**

```bash
git add internal/metrics/server.go internal/metrics/server_test.go
git commit -m "feat(metrics): /metrics + /healthz HTTP server with graceful shutdown"
```

---

### Task 4: 发送链路业务埋点(nil-safe 注入)

**Files:**
- Modify: `internal/dispatch/types.go`(SendWorker/Dispatcher 增 `m *metrics.Metrics` 字段 + `WithMetrics` setter)
- Modify: `internal/dispatch/worker.go`(各 outcome 分支 + gate + billing + health 记录)
- Modify: `internal/dispatch/batch.go`(batch 直方图 + no-capacity 计数)
- Create: `internal/dispatch/metrics_wiring_test.go`

**Interfaces:**
- Consumes: `*metrics.Metrics`(Task 1)及其 nil-safe 记录方法。
- Produces: `func (w *SendWorker) WithMetrics(m *metrics.Metrics) *SendWorker`;`func (d *Dispatcher) WithMetrics(m *metrics.Metrics) *Dispatcher`。

> **关键约束:不改 `NewSendWorker`/`NewDispatcher` 签名。** 只新增字段(默认 nil)+ setter。已合并测试构造时不传 metrics,字段为 nil,所有记录 no-op,零回归。

- [ ] **Step 1: 写失败测试**

`internal/dispatch/metrics_wiring_test.go`(复用本包既有 `pgPool`/`redisClient`/`applyMigrations`/seed 辅助 + 既有 fakes):

```go
package dispatch

import (
	"testing"

	"github.com/acme/wadist/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// 复用既有 admit-deny 集成场景：构造一个被隔离/超额的账号，ProcessSend 应记 gate_denied。
func TestProcessSend_RecordsGateDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	// 照搬既有 admit-deny 测试的 setup（隔离账号 + 已分配 recipient + funded wallet）。
	w, pl, body := setupAdmitDenyCase(t) // 提取既有测试的 setup 为辅助；若不存在则内联其步骤
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	w.WithMetrics(m)

	if err := w.ProcessSend(tctx(t), pl, body, "", "", nil); err != nil {
		t.Fatalf("ProcessSend: %v", err)
	}
	// gate_denied 计数 +1（outcome 维度）
	if v := testutil.ToFloat64(gateDeniedCounter(t, reg)); v < 1 {
		t.Fatalf("expected gate_denied recorded, got %v", v)
	}
}

func TestDispatchBatch_ObservesAssigned(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	d, campaignID := setupBatchHappyCase(t) // 提取既有 batch happy 测试 setup
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	d.WithMetrics(m)

	n, err := d.dispatchBatch(tctx(t), campaignID, 10)
	if err != nil || n < 1 {
		t.Fatalf("dispatchBatch n=%d err=%v", n, err)
	}
	// 直方图样本数 >=1
	if c := histogramSampleCount(t, reg, "wadist_dispatch_batch_assigned"); c < 1 {
		t.Fatalf("expected batch histogram sample, got %d", c)
	}
}
```

> **给 implementer:** `setupAdmitDenyCase`/`setupBatchHappyCase`/`tctx`/`gateDeniedCounter`/`histogramSampleCount` 为测试辅助——优先从既有 `worker_test.go`/`batch_test.go` 抽取 setup(不要复制粘贴整段逻辑,提取成共享辅助);`gateDeniedCounter`/`histogramSampleCount` 用 `reg.Gather()` 取对应 metric family。`outcome` 标签取值见下表。

- [ ] **Step 2: 跑确认失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./internal/dispatch/ -run 'TestProcessSend_RecordsGateDenied|TestDispatchBatch_ObservesAssigned' -v`
Expected: FAIL(undefined WithMetrics)

- [ ] **Step 3: types.go 加字段 + setter**

在 `SendWorker` 结构体增加字段 `m *metrics.Metrics`;`Dispatcher` 结构体增加 `m *metrics.Metrics`。在 types.go(或各自文件)追加:

```go
import "github.com/acme/wadist/internal/metrics"

func (w *SendWorker) WithMetrics(m *metrics.Metrics) *SendWorker { w.m = m; return w }
func (d *Dispatcher) WithMetrics(m *metrics.Metrics) *Dispatcher { d.m = m; return d }
```

> 确认无 import 环:`metrics` 不 import `dispatch`(已侦察确认)。

- [ ] **Step 4: worker.go 各分支埋点**

读现有 `ProcessSend`,在以下分支(顺序即代码路径)插入 nil-safe 记录(`w.m` 为 nil 时全部 no-op):

| 分支 | 记录调用 |
|------|----------|
| 终态幂等跳过(st==sent/failed) | `w.m.RecordSend("idempotent_skip")` |
| `!dec.Allow` requeue 前 | `w.m.RecordGate(false, dec.Reason)` 然后 `w.m.RecordSend("gate_denied")` |
| `dec.Allow` 时 | `w.m.RecordGate(true, "ok")` |
| `Hold` 失败 requeue | `w.m.RecordBilling("hold", holdOutcome(err))` 然后 `w.m.RecordSend("hold_failed")` |
| `Hold` 成功 | `w.m.RecordBilling("hold", "held")` |
| 媒体解析失败 | `w.m.RecordSend("media_failed")` |
| `Send` 失败(非封号) | `w.m.RecordSend("send_failed")` |
| `Send` 失败(封号信号) | `w.m.RecordHealthSignal("wa_warning")`(已有 ApplyHealthSignal 调用处)+ `w.m.RecordSend("send_failed")` |
| `Settle` 失败 | `w.m.RecordBilling("settle", "error")` 然后 `w.m.RecordSend("settle_failed")` |
| `Settle` 成功 + markSent | `w.m.RecordBilling("settle", "ok")` 然后 `w.m.RecordSend("sent")` |

新增小辅助(放 worker.go):

```go
func holdOutcome(err error) string {
	switch {
	case errors.Is(err, billing.ErrInsufficientFunds):
		return "insufficient_funds"
	case errors.Is(err, billing.ErrWalletLocked):
		return "wallet_locked"
	case errors.Is(err, billing.ErrNotFound):
		return "not_found"
	default:
		return "error"
	}
}
```

> `billing` 已是本包依赖(ProcessSend 调 billing.Hold/Settle)。`dec.Reason` 取值有界(ok/not_active/quarantined/daily_quota/pacing),作为 label 值安全。**不要**把 jid/phone/message_id 传入任何 WithLabelValues。

- [ ] **Step 5: batch.go 埋点**

在 `dispatchBatch` 末尾(成功返回前)记录:`d.m.ObserveBatch(assigned)`;在 `ErrNoCapacity` continue 分支记录:`d.m.IncNoCapacity()`。

- [ ] **Step 6: 跑全包测试 + 门禁**

Run:
```bash
TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./internal/dispatch/ -race -v
./scripts/check_metric_labels.sh
/usr/local/bin/go vet ./internal/dispatch/
```
Expected: 全 PASS;门禁 `OK`;vet 干净。**既有 dispatch 测试必须全绿(nil metrics no-op)。**

- [ ] **Step 7: 提交**

```bash
git add internal/dispatch/
git commit -m "feat(dispatch): instrument send pipeline with nil-safe metrics (gate/billing/outcomes/batch)"
```

---

### Task 5: `cmd/wadist` 二进制(装配 + 启动 /metrics + asynq worker)

**Files:**
- Create: `cmd/wadist/main.go`
- Create: `cmd/wadist/main_smoke_test.go`

**Interfaces:**
- Consumes: `config.Load`、`store.Init`、`billing.NewRepo`、`sendgate.NewSendGate`、`dispatch.NewDispatcher/NewSendWorker/WithMetrics`、`metrics.New/NewDBCollector/NewServer`、`dispatch.RegisterSendHandler`/`AsynqEnqueuer`。
- Produces: 可运行二进制 + 可测试的 `run(ctx, cfg) (*metrics.Server, func(), error)` 装配函数。

> **范围界定:** 完整生命周期/优雅停机(Supervisor、SetLimit(32)、断会话顺序)属于 **M8**。M7 的 main 只需:装配依赖、注册 DBCollector、起 `/metrics` server、起 asynq worker、收到 SIGINT/SIGTERM 后做基础收尾(停 http、cancel ctx、关池)。把装配逻辑抽成 `run()` 便于冒烟测试。

- [ ] **Step 1: 写冒烟测试**

`cmd/wadist/main_smoke_test.go`:

```go
package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// 需要真实 PG/Redis（testcontainers 在本测试内拉起，或读环境 DSN）。
// 简化：若无 WADIST_POSTGRES_DSN 则 skip（冒烟仅验证装配+/metrics 起得来）。
func TestRun_BootsMetrics(t *testing.T) {
	if testing.Short() || os.Getenv("WADIST_POSTGRES_DSN") == "" {
		t.Skip("integration: set WADIST_POSTGRES_DSN")
	}
	cfg := loadForTest(t) // config.Load() 后把 MetricsAddr 改成 127.0.0.1:0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, stop, err := run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer stop()

	resp, err := http.Get("http://" + srv.Addr() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "wadist_") {
		t.Fatalf("metrics not served: %d", resp.StatusCode)
	}
	_ = time.Second
}
```

> implementer:若拉 testcontainers 进 cmd 测试过重,可保留 skip-on-missing-DSN 形式(已在 dispatch/metrics 包有真实集成覆盖);本测试核心是验证 `run()` 装配不 panic 且 `/metrics` 可达。

- [ ] **Step 2: 写 main.go**

```go
// Command wadist is the distribution node entrypoint. M7 scope: dependency
// assembly, /metrics exposure, and the asynq send worker. Full graceful
// shutdown (Supervisor, session draining, SetLimit) lands in M8.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/config"
	walog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/metrics"
	"github.com/acme/wadist/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv, stop, err := run(ctx, cfg)
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	log.Printf("wadist up; metrics on %s", srv.Addr())
	<-ctx.Done()
	log.Printf("shutdown signal received")
	stop()
}

// run assembles dependencies, starts the metrics server, and returns a stop
// func. It is the seam the smoke test drives.
func run(ctx context.Context, cfg *config.Config) (*metrics.Server, func(), error) {
	logger, flush, err := walog.Production()
	if err != nil {
		return nil, nil, err
	}

	mgr, err := store.Init(ctx, cfg.Store(), logger)
	if err != nil {
		flush()
		return nil, nil, err
	}
	pool := mgr.Pool() // 若 Manager 暴露池的方法名不同，以实际为准

	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	reg.MustRegister(metrics.NewDBCollector(pool, 10*time.Second, nil)) // M8 用真实 Registry 替换 nil

	// repos / gate（按实际构造签名装配；priceFor 用基础占位定价）
	_ = billing.NewRepo(pool)
	// sendgate/dispatch worker 装配与 asynq handler 注册（见下）：
	// gate := sendgate.NewSendGate(pool, adm, baseGap)
	// worker := dispatch.NewSendWorker(pool, gate, bill, sender, uploader).WithMetrics(m)
	// mux := asynq.NewServeMux(); dispatch.RegisterSendHandler(mux, worker)
	// asynqSrv := asynq.NewServer(redisOpt, asynq.Config{Concurrency: 32})
	// go asynqSrv.Run(mux)
	//
	// NOTE: sender/uploader 是 whatsmeow 适配（M-后续）；M7 若尚无真实 sender，
	// 用一个返回 not-implemented 的占位 Sender/Uploader 起 worker，确保二进制可编译可启动。

	srv := metrics.NewServer(cfg.MetricsAddr, reg)
	if err := srv.Start(); err != nil {
		flush()
		return nil, nil, err
	}

	stop := func() {
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(sctx)
		// asynqSrv.Shutdown() // M8 接入完整顺序
		mgr.Close()
		flush()
	}
	return srv, stop, nil
}
```

> **implementer 注意:**
> - 读 `store.Manager` 暴露池的真实方法(可能是 `Pool()` / 字段);若未导出,新增一个 `func (m *Manager) Pool() *pgxpool.Pool` 只读访问器(最小改动)。
> - `walog.Production()` 返回 `(waLog.Logger, func(), error)`(已侦察确认)。`store.Init` 第二参为 `cfg.Store()` 投影后的 `store.Config`(确认 `Config.Store()` 存在)。
> - asynq/sendgate/sender 装配:若 M7 阶段尚无真实 whatsmeow sender,用占位 `Sender`/`Uploader`(返回 `errors.New("sender: not wired (pre-M-send)")`)让二进制可编译可启动;真实接线在后续里程碑。务必让 `/metrics` 这条主线**真正起得来**。
> - **不要**引入 import 环;**不要**把高基数值塞进任何 label。

- [ ] **Step 3: 编译 + 冒烟**

Run:
```bash
/usr/local/bin/go build ./... && /usr/local/bin/go vet ./cmd/...
TESTCONTAINERS_RYUK_DISABLED=true WADIST_POSTGRES_DSN="$WADIST_POSTGRES_DSN" /usr/local/bin/go test ./cmd/wadist/ -v
```
Expected: build 成功;冒烟 PASS(或在无 DSN 时 skip)。

- [ ] **Step 4: 全门禁复跑**

Run:
```bash
/usr/local/bin/go build ./...
/usr/local/bin/go vet ./...
./scripts/check_metric_labels.sh
TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./... -race
```
Expected: 全绿;高基数门禁 `OK`。

- [ ] **Step 5: 提交**

```bash
git add cmd/wadist/
git commit -m "feat(cmd): wadist binary wiring metrics server + DBCollector + asynq worker"
```

---

## Self-Review(写作后自查)

- **Spec 覆盖:** Prometheus 有界 label 指标 ✅(Task 1)、DBCollector + TTL 缓存 ✅(Task 2)、`/metrics` HTTP ✅(Task 3)、业务埋点 ✅(Task 4)、PublishRegistryState → 以 `RegistrySnapshotProvider` nil-safe 桩 + `wadist_active_sessions` gauge 表达(M8 替换真实 Registry)✅。高基数门禁纳入每个相关任务步骤 ✅。
- **类型一致:** `New(reg)`、`WithMetrics`、`NewDBCollector(pool,ttl,snap)`、`NewServer(addr,reg)`/`Start`/`Addr`/`Shutdown` 在任务间一致。`RegistrySnapshotProvider.ActiveSessions() int` 在 Task 1 定义、Task 2 消费。
- **占位扫描:** 无 TODO/占位;每个代码步骤含完整代码或精确编辑指令(修改既有文件的任务给出逐分支 label 表 + 辅助函数完整代码)。
- **前置/边界:** main 的完整优雅停机显式划归 M8;真实 whatsmeow sender 用占位,保证 `/metrics` 主线可启动。Manager 池访问器若缺则最小新增。
