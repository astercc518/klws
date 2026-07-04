# M4: Risk Governor（自适应 τ / L4 舰队级封号率熔断）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增一个 Risk Governor 控制环，周期性读取舰队级滚动封号率，用 AIMD（健康则加性增、封号率超 SLO 则乘性减）动态调整 M3 Rolling-Wave Pump 的全局令牌桶速率 τ，闭合防封控制环——把「封号率是天花板」从被动熔断（riskbreaker 暂停 campaign）升级为主动调速。

**Architecture:** 复用 M3 的 `dispatch.Pump.lim`（`x/time/rate` 令牌桶）：Pump 暴露线程安全的 `SetRate(float64)`（`lim.SetLimit`）。新增 `dispatch.Governor`：一个 `Run(ctx)` 控制循环，每 `interval` 读 `FleetBanRate`（镜像 riskbreaker 的 definition-A 封号率 SQL，但去掉 campaign 过滤 = 舰队级），经纯函数 `nextRate`（AIMD）算出新 τ 并 `SetRate`。配置门控 `WADIST_RISK_GOVERNOR`（默认 `off`）——关时 τ 恒为 `SendRate`（M3 行为逐字不变，零爆炸半径）；仅在 pump 模式 + governor on 时启动。riskbreaker（campaign 级 L4 兜底）保持不变，与 Governor 正交（一个调速、一个暂停）。

**Tech Stack:** Go 1.26、`golang.org/x/time/rate`（`Limiter.SetLimit`，已 direct）、PostgreSQL（campaign_recipients 封号信号）、testcontainers-go（PG 集成测试）。

## Global Constraints

- **零爆炸半径**：`WADIST_RISK_GOVERNOR` 默认 `off`；关时不启动 Governor，Pump 的 τ 恒等于 `SendRate`（M3 行为）。Governor 只在 `DispatchMode=="pump" && RiskGovernor=="on"` 时启动。
- **只读信号 + 调 τ，不改引擎**（spec §6.140）：Governor 只读 `campaign_recipients`（封号率）、只调用 `Pump.SetRate`；不改 sendgate/billing/dispatchBatch/riskbreaker 逻辑。
- **封号率定义（definition A，与 riskbreaker 一致）**：窗口内 `attempted = count(state∈{sent,failed})`，`banFailures = count(state='failed' AND last_error ILIKE '%wa_warning%'|'%banned%'|'%403%')`，`banRate = banFailures/attempted`。M4 去掉 `WHERE campaign_id` → 舰队级。
- **AIMD**：`banRate ≥ SLO` → 乘性减 `τ = max(τ*factor, τ_min)`（factor∈(0,1)）；`banRate < SLO` → 加性增 `τ = min(τ+step, τ_max)`；`τ_max = SendRate`（M3 上限）。样本不足（`attempted < minSample`）跳过本轮（信号噪声大）。
- **线程安全**：`rate.Limiter.SetLimit` 是 mutex 保护的，Governor 循环与 Pump worker 的 `lim.Wait` 并发安全。
- **范围**：M4 只做 L4 舰队级自适应全局 τ。**L3（国家/ASN 段级 τ）延后**——Pump 只有一个全局 limiter，段级需 per-segment 限速器，属更大改动，1:1 单机目标下优先级低；记为后续。
- TDD；`nextRate` 是纯函数（无 infra 单测）；`FleetBanRate`/Governor 循环用 testcontainers PG。改 `internal/dispatch` 后跑 `make gate`。

---

## 文件结构

- Modify `internal/config/config.go` — 新增 `RiskGovernor string`(默认"off") + `GovSLO/GovStep/GovFactor/GovMinRate float64`、`GovIntervalMs/GovWindowSec/GovMinSample int`，读对应 `WADIST_GOV_*` env。
- Modify `internal/dispatch/pump.go` — `Pump` 加 `SetRate(r float64)`（线程安全，r<=0→rate.Inf 与 NewPump 一致）。
- Create `internal/dispatch/governor.go` — `nextRate`(纯 AIMD) + `FleetBanRate`(PG 查询) + `Governor` 类型 + `Run(ctx)`。
- Create `internal/dispatch/governor_test.go` — `nextRate` 表驱动单测（无 infra）+ `FleetBanRate`/Governor 循环集成测试（PG）。
- Modify `cmd/wadist/main.go` — pump 分支内，`RiskGovernor=="on"` 时构造 `Governor`（持 `pump.SetRate`）并 `sup.Go(gov.Run)`。

---

### Task 1: Risk Governor 配置

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.RiskGovernor string`(默认"off")、`GovSLO float64`(默认 0.02)、`GovStep float64`(默认 5)、`GovFactor float64`(默认 0.5)、`GovMinRate float64`(默认 1)、`GovIntervalMs int`(默认 20000)、`GovWindowSec int`(默认 900)、`GovMinSample int`(默认 20)。

- [ ] **Step 1: Write the failing test**

```go
// internal/config/config_test.go (add)
func TestConfig_RiskGovernorDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RiskGovernor != "off" {
		t.Fatalf("RiskGovernor default = %q; want off", cfg.RiskGovernor)
	}
	if cfg.GovSLO != 0.02 || cfg.GovFactor != 0.5 || cfg.GovStep != 5 || cfg.GovMinRate != 1 {
		t.Fatalf("AIMD defaults wrong: slo=%v step=%v factor=%v min=%v", cfg.GovSLO, cfg.GovStep, cfg.GovFactor, cfg.GovMinRate)
	}
	if cfg.GovIntervalMs != 20000 || cfg.GovWindowSec != 900 || cfg.GovMinSample != 20 {
		t.Fatalf("window defaults wrong: interval=%d window=%d sample=%d", cfg.GovIntervalMs, cfg.GovWindowSec, cfg.GovMinSample)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/config/ -run TestConfig_RiskGovernorDefaults -v`
Expected: FAIL — fields undefined.

- [ ] **Step 3: Implement**

Add to `Config`:
```go
// RiskGovernor enables the adaptive-τ risk governor ("on") or leaves the pump
// rate fixed at SendRate ("off", default). Only active in pump dispatch mode.
RiskGovernor string
// AIMD params: banRate>=GovSLO → τ*=GovFactor (floored at GovMinRate);
// banRate<GovSLO → τ+=GovStep (capped at SendRate). Evaluated every
// GovIntervalMs over a GovWindowSec window, skipped when attempted<GovMinSample.
GovSLO, GovStep, GovFactor, GovMinRate float64
GovIntervalMs, GovWindowSec, GovMinSample int
```
In `Load()` (after SendRate is resolved — grep for `cfg.SendRate`):
```go
cfg.RiskGovernor = getenv("WADIST_RISK_GOVERNOR", "off")
cfg.GovSLO = floatEnv("WADIST_GOV_SLO", 0.02)
cfg.GovStep = floatEnv("WADIST_GOV_STEP", 5)
cfg.GovFactor = floatEnv("WADIST_GOV_FACTOR", 0.5)
cfg.GovMinRate = floatEnv("WADIST_GOV_MIN_RATE", 1)
cfg.GovIntervalMs = intEnv("WADIST_GOV_INTERVAL_MS", 20000)
cfg.GovWindowSec = intEnv("WADIST_GOV_WINDOW_SEC", 900)
cfg.GovMinSample = intEnv("WADIST_GOV_MIN_SAMPLE", 20)
```
> Reuse the existing `getenv`/`intEnv`/`floatEnv` helpers (added/confirmed in M3 Task 1). Grep `internal/config/config.go` for their exact names.

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/config/ -run TestConfig_RiskGovernorDefaults -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): risk governor + AIMD params (default off)"
```

---

### Task 2: Pump.SetRate（线程安全调 τ）

**Files:**
- Modify: `internal/dispatch/pump.go`
- Test: `internal/dispatch/pump_test.go`

**Interfaces:**
- Produces: `func (p *Pump) SetRate(r float64)` — sets the token-bucket limit; `r<=0` → `rate.Inf` (unlimited), matching NewPump's guard. Thread-safe (`lim.SetLimit`).

- [ ] **Step 1: Write the failing test**

```go
// internal/dispatch/pump_test.go (add)
func TestPump_SetRate(t *testing.T) {
	pe := NewPumpEnqueuer(1)
	p := NewPump(pe, nil, nil, 100, 2) // sender/resolve nil: not exercised here
	p.SetRate(50)
	if got := p.lim.Limit(); got != rate.Limit(50) {
		t.Fatalf("after SetRate(50) limit=%v; want 50", got)
	}
	p.SetRate(0) // <=0 → unlimited
	if got := p.lim.Limit(); got != rate.Inf {
		t.Fatalf("after SetRate(0) limit=%v; want Inf", got)
	}
}
```
> Add `"golang.org/x/time/rate"` to the test imports.

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestPump_SetRate -v`
Expected: FAIL — `SetRate` undefined.

- [ ] **Step 3: Implement**

```go
// append to internal/dispatch/pump.go
// SetRate updates the pump's token-bucket rate at runtime (used by the Risk
// Governor). r<=0 means unlimited (rate.Inf), matching NewPump. Thread-safe:
// rate.Limiter.SetLimit is mutex-guarded, safe against concurrent Wait().
func (p *Pump) SetRate(r float64) {
	if r <= 0 {
		p.lim.SetLimit(rate.Inf)
		return
	}
	p.lim.SetLimit(rate.Limit(r))
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestPump_SetRate -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/dispatch/pump.go internal/dispatch/pump_test.go
git commit -m "feat(dispatch): Pump.SetRate for runtime τ adjustment (thread-safe)"
```

---

### Task 3: governor.go — nextRate (纯 AIMD) + FleetBanRate (PG)

**Files:**
- Create: `internal/dispatch/governor.go`
- Test: `internal/dispatch/governor_test.go`

**Interfaces:**
- Produces:
  - `type GovParams struct { SLO, Step, Factor, MinRate, MaxRate float64 }`
  - `func nextRate(current, banRate float64, p GovParams) float64` — pure AIMD: `banRate>=SLO` → `max(current*Factor, MinRate)`; else → `min(current+Step, MaxRate)`.
  - `func FleetBanRate(ctx context.Context, pool *pgxpool.Pool, windowSec int) (banRate float64, attempted int, err error)` — fleet-wide definition-A ban rate over the window (mirrors riskbreaker.banRate minus the campaign filter).

- [ ] **Step 1: Write the failing test** (table-driven pure AIMD, no infra)

```go
package dispatch

import (
	"math"
	"testing"
)

func TestNextRate_AIMD(t *testing.T) {
	p := GovParams{SLO: 0.02, Step: 5, Factor: 0.5, MinRate: 1, MaxRate: 160}
	cases := []struct {
		name              string
		current, banRate  float64
		want              float64
	}{
		{"healthy additive increase", 100, 0.00, 105},
		{"healthy capped at max", 158, 0.01, 160},
		{"over SLO multiplicative decrease", 100, 0.05, 50},
		{"decrease floored at min", 1.5, 0.9, 1}, // 1.5*0.5=0.75 → floored to 1
		{"exactly at SLO decreases", 80, 0.02, 40},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextRate(c.current, c.banRate, p); math.Abs(got-c.want) > 1e-9 {
				t.Fatalf("nextRate(%v,%v) = %v; want %v", c.current, c.banRate, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestNextRate_AIMD -v`
Expected: FAIL — `nextRate`/`GovParams` undefined.

- [ ] **Step 3: Implement**

```go
// internal/dispatch/governor.go
package dispatch

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// GovParams are the AIMD knobs. MaxRate is the SendRate ceiling.
type GovParams struct {
	SLO, Step, Factor, MinRate, MaxRate float64
}

// nextRate applies AIMD: at/over the SLO the rate is multiplicatively decreased
// (floored at MinRate); below the SLO it is additively increased (capped at
// MaxRate). Pure function — the whole control law, unit-tested in isolation.
func nextRate(current, banRate float64, p GovParams) float64 {
	if banRate >= p.SLO {
		next := current * p.Factor
		if next < p.MinRate {
			next = p.MinRate
		}
		return next
	}
	next := current + p.Step
	if next > p.MaxRate {
		next = p.MaxRate
	}
	return next
}

// FleetBanRate computes the fleet-wide definition-A ban rate over the last
// windowSec: banFailures/attempted where attempted = recent sent+failed and
// banFailures = recent failed whose last_error carries a ban signal. Mirrors
// riskbreaker.banRate without the campaign filter. Returns (0,0,nil) when idle.
func FleetBanRate(ctx context.Context, pool *pgxpool.Pool, windowSec int) (float64, int, error) {
	window := fmt.Sprintf("%d seconds", windowSec)
	var attempted, banFailures int
	err := pool.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE state IN ('sent','failed') AND updated_at >= now() - $1::interval),
  count(*) FILTER (WHERE state = 'failed' AND updated_at >= now() - $1::interval AND (
        last_error ILIKE '%wa_warning%' OR last_error ILIKE '%banned%' OR last_error ILIKE '%403%'))
  FROM campaign_recipients`, window).Scan(&attempted, &banFailures)
	if err != nil {
		return 0, 0, fmt.Errorf("fleet ban rate: %w", err)
	}
	if attempted == 0 {
		return 0, 0, nil
	}
	return float64(banFailures) / float64(attempted), attempted, nil
}
```
> Confirm `campaign_recipients` has `updated_at` + `last_error` (grep migrations 0006/0008; riskbreaker already uses both, so they exist). If `updated_at` doesn't exist under that name, use the column riskbreaker.banRate uses (copy it verbatim).

- [ ] **Step 4: Run to verify it passes** (pure test; FleetBanRate covered in Task 4's loop test)

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestNextRate_AIMD -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/dispatch/governor.go internal/dispatch/governor_test.go
git commit -m "feat(dispatch): AIMD nextRate + fleet ban-rate query"
```

---

### Task 4: Governor.Run（AIMD 控制循环）

**Files:**
- Modify: `internal/dispatch/governor.go`
- Test: `internal/dispatch/governor_test.go`

**Interfaces:**
- Consumes: Task 3 `nextRate`/`FleetBanRate`/`GovParams`.
- Produces:
  - `type Governor struct { ... }`
  - `func NewGovernor(pool *pgxpool.Pool, setRate func(float64), params GovParams, windowSec, minSample int, current float64) *Governor` — `current` = initial τ (= SendRate); `setRate` = pump.SetRate.
  - `func (g *Governor) Run(ctx context.Context, interval time.Duration) error` — ticker loop; each tick: `FleetBanRate`; if `attempted < minSample` skip (log at debug); else `g.current = nextRate(g.current, banRate, params)` + `g.setRate(g.current)`. Per-tick errors logged, loop continues. Exits on ctx cancel.
  - `func (g *Governor) EvaluateOnce(ctx context.Context) (newRate float64, applied bool, err error)` — one evaluation step, exported for tests.

- [ ] **Step 1: Write the failing test** (real PG; seed ban failures → τ decreases; then heal → τ increases)

```go
func TestGovernor_EvaluateOnce(t *testing.T) {
	ctx := context.Background()
	pool := newDispatchTestPool(t) // reuse the dispatch PG test helper (grep: pgPool)
	var setTo float64
	params := GovParams{SLO: 0.02, Step: 5, Factor: 0.5, MinRate: 1, MaxRate: 160}
	g := NewGovernor(pool, func(r float64) { setTo = r }, params, 900 /*window*/, 3 /*minSample*/, 100 /*current*/)

	// too few samples → skip (applied=false).
	if _, applied, err := g.EvaluateOnce(ctx); err != nil || applied {
		t.Fatalf("no data: applied=%v err=%v; want false,nil", applied, err)
	}

	// seed 5 attempted, 3 ban-failures (banRate=0.6 >= SLO) → decrease to 50.
	seedFleetOutcomes(t, pool, 2 /*sent*/, 3 /*ban-failed*/) // helper: inserts recipients w/ state+last_error+updated_at=now()
	r, applied, err := g.EvaluateOnce(ctx)
	if err != nil || !applied || r != 50 {
		t.Fatalf("over-SLO: r=%v applied=%v err=%v; want 50,true,nil", r, applied, err)
	}
	if setTo != 50 {
		t.Fatalf("setRate got %v; want 50", setTo)
	}
}
```
> `newDispatchTestPool`/`seedFleetOutcomes`: reuse the dispatch test harness (grep `internal/dispatch/*_test.go` for `pgPool`/seed helpers). `seedFleetOutcomes` inserts N sent + M failed(with a `last_error` containing `wa_warning`) recipients with `updated_at=now()` — build on the existing `seedRecipient` helper + a targeted UPDATE to set state/last_error/updated_at.

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestGovernor_EvaluateOnce -v`
Expected: FAIL — `NewGovernor` undefined.

- [ ] **Step 3: Implement**

```go
// append to internal/dispatch/governor.go
import (
	"log"
	"time"
)

type Governor struct {
	pool      *pgxpool.Pool
	setRate   func(float64)
	params    GovParams
	windowSec int
	minSample int
	current   float64
}

func NewGovernor(pool *pgxpool.Pool, setRate func(float64), params GovParams, windowSec, minSample int, current float64) *Governor {
	return &Governor{pool: pool, setRate: setRate, params: params, windowSec: windowSec, minSample: minSample, current: current}
}

// EvaluateOnce reads the fleet ban rate and, if there is enough signal
// (attempted >= minSample), applies one AIMD step to the pump rate. Returns the
// new rate, whether it was applied, and any error. Exported for tests.
func (g *Governor) EvaluateOnce(ctx context.Context) (float64, bool, error) {
	banRate, attempted, err := FleetBanRate(ctx, g.pool, g.windowSec)
	if err != nil {
		return g.current, false, err
	}
	if attempted < g.minSample {
		return g.current, false, nil // not enough signal; leave τ unchanged
	}
	g.current = nextRate(g.current, banRate, g.params)
	g.setRate(g.current)
	return g.current, true, nil
}

// Run drives EvaluateOnce every interval until ctx is cancelled. Per-tick errors
// are logged and do not stop the loop (transient DB error recovers next tick).
func (g *Governor) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, applied, err := g.EvaluateOnce(ctx); err != nil {
				log.Printf("risk governor: %v", err)
			} else if applied {
				log.Printf("risk governor: τ → %.1f/s", g.current)
			}
		}
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestGovernor -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/dispatch/governor.go internal/dispatch/governor_test.go
git commit -m "feat(dispatch): Risk Governor AIMD control loop (EvaluateOnce/Run)"
```

---

### Task 5: main.go 装配 + e2e + make gate

**Files:**
- Modify: `cmd/wadist/main.go`
- Test: `internal/dispatch/governor_test.go` (add a heal-then-recover e2e assertion) + full gate

**Interfaces:**
- Consumes: Task 2 `Pump.SetRate`, Task 4 `NewGovernor`/`Run`.

- [ ] **Step 1: Wire the governor** (pump mode + governor on)

In `cmd/wadist/main.go`, inside the `if cfg.DispatchMode == "pump" {` branch, AFTER `pump := dispatch.NewPump(...)` and its `sup.Go(pump.Run)`:
```go
if cfg.RiskGovernor == "on" {
	gov := dispatch.NewGovernor(pool, pump.SetRate,
		dispatch.GovParams{SLO: cfg.GovSLO, Step: cfg.GovStep, Factor: cfg.GovFactor, MinRate: cfg.GovMinRate, MaxRate: cfg.SendRate},
		cfg.GovWindowSec, cfg.GovMinSample, cfg.SendRate /*start τ at the ceiling*/)
	sup.Go(func(lctx context.Context) error {
		return gov.Run(lctx, time.Duration(cfg.GovIntervalMs)*time.Millisecond)
	})
	log.Printf("risk governor on (SLO=%.3f step=%.1f factor=%.2f min=%.1f max=%.1f window=%ds interval=%dms)",
		cfg.GovSLO, cfg.GovStep, cfg.GovFactor, cfg.GovMinRate, cfg.SendRate, cfg.GovWindowSec, cfg.GovIntervalMs)
}
```
The Governor is only constructed in pump mode (it holds the pump's SetRate) and only when `RiskGovernor=="on"` — off (default) leaves the pump at fixed `SendRate` (M3 behavior). asynq mode never constructs it.

- [ ] **Step 2: Add a heal-then-recover e2e assertion**

In `governor_test.go` add `TestGovernor_HealRecovers`: seed over-SLO ban outcomes → `EvaluateOnce` decreases τ; then insert enough recent `sent` (no ban) outcomes so the window's banRate drops below SLO → `EvaluateOnce` increases τ by Step. Asserts the loop is bidirectional (decrease under bans, recover when healthy).

```go
func TestGovernor_HealRecovers(t *testing.T) {
	ctx := context.Background()
	pool := newDispatchTestPool(t)
	var last float64
	params := GovParams{SLO: 0.02, Step: 5, Factor: 0.5, MinRate: 1, MaxRate: 160}
	g := NewGovernor(pool, func(r float64) { last = r }, params, 900, 3, 100)

	seedFleetOutcomes(t, pool, 2, 3) // banRate 0.6 → decrease
	if r, _, _ := g.EvaluateOnce(ctx); r != 50 {
		t.Fatalf("decrease: got %v want 50", r)
	}
	// heal: add many clean sends so banRate falls below SLO.
	seedFleetOutcomes(t, pool, 300, 0)
	r, applied, err := g.EvaluateOnce(ctx)
	if err != nil || !applied || r != 55 { // 50 + Step(5)
		t.Fatalf("recover: r=%v applied=%v err=%v; want 55,true,nil", r, applied, err)
	}
	_ = last
}
```

- [ ] **Step 3: Verify build + full gate**

Run: `cd /var/klwa && go build ./... && go vet ./... && go test ./internal/dispatch/ -run TestGovernor -race && make gate`
Expected: build OK; governor tests pass; `make gate` GREEN. Default `WADIST_RISK_GOVERNOR` unset → off → pump τ fixed, existing tests unchanged.

- [ ] **Step 4: Commit**

```bash
cd /var/klwa
git add cmd/wadist/main.go internal/dispatch/governor_test.go
git commit -m "feat(wadist): wire risk governor (pump mode, default off) + heal-recover e2e; gate green"
```

---

## Self-Review 结论（作者已核对）

- **Spec 覆盖**（§6.2）：L4 舰队级「全局滚动封号率 > SLO → τ 全局收敛」= Task 3 `FleetBanRate` + `nextRate` + Task 4 Governor 循环 + Task 5 装配。riskbreaker 的 campaign 暂停兜底保持不变（正交）。**L3（段级 τ）显式延后**，已在 Global Constraints 记录理由。§6.1 FSM / §6.3 反指纹是既有能力（sendgate health / M2 粘性 / M3 去同步），非本计划新增。
- **占位符扫描**：无 TODO/“类似上文”。测试助手（newDispatchTestPool/pgPool/seedRecipient/seedFleetOutcomes）标注为「复用现有 dispatch 测试 harness，名字不符时 grep 真实等价物」；env 助手（getenv/intEnv/floatEnv）同为复用。
- **类型一致性**：`GovParams`、`nextRate`、`FleetBanRate`、`Governor`(NewGovernor/EvaluateOnce/Run)、`Pump.SetRate`、`Config.RiskGovernor/Gov*` 跨 Task 一致；`MaxRate=cfg.SendRate`、起始 τ=SendRate 一致。
- **零爆炸半径**：`RiskGovernor` 默认 off；Governor 仅 pump+on 时构造；asynq 模式与 off 时 τ 恒定=M3。

## 已知风险与后续（非阻塞，记入执行账本）

- **L3 段级 τ 延后**：需 per-country/ASN limiter（Pump 现为单全局 limiter）——未来 spec/里程碑。
- **窗口 SLO vs 日 SLO**：spec §6.2 举例「0.5%/天」，本计划用「窗口内封号失败占比」(默认 SLO=2%、window=900s)，是不同口径——放量时按实测标定（spec §11 canary A/B）。默认值是起点假设。
- **FleetBanRate 全表聚合**：每 interval 一次 `campaign_recipients` 全表 FILTER 聚合；`idx_recip_pending`/`idx_recip_msgid` 不覆盖此查询，大表时考虑加 `(state, updated_at)` 部分索引（后续观测决定）。
- **Governor 与 riskbreaker 双动作**：一个调 τ、一个暂停 campaign；同源信号但动作正交，不冲突。若未来合并可复用信号查询。
- **起始 τ=SendRate（乐观）**：从上限起步、遇封号快速乘性减；若偏好保守可改为从低起步加性增（配置化留后续）。
