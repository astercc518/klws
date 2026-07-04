# M5: L3 段级 τ（按国家的封号率分段降速）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 M4 舰队级自适应 τ（L4）之上加 L3 段级熔断——当某个国家(cc)的滚动封号率显著高于舰队基线时，只给该国家的发送套一个更低的段级令牌桶降速（不影响健康国家），把「一个坏 cc 拖垮整池」隔离掉。

**Architecture:** 复用 M3 Pump 的 worker 循环：每个 SendPayload 已带 `Country`(cc)。Pump 新增 per-cc 令牌桶 `segLim map[cc]*rate.Limiter`（RWMutex 保护）+ `SetSegmentRate(cc, r)`；worker 取全局令牌后，若该 cc 有段级 limiter 再取一个段级令牌（两级都过才发）。M4 的 `Governor` 扩展一个可选「段级 pass」：每轮除 L4 全局 AIMD 外，用 `SegmentBanRates`（按 country_code 分组的 definition-A 封号率）算各 cc 率与舰队基线，热段（`banRate ≥ max(基线×Mult, SegSLO)` 且样本足）→ `SetSegmentRate(cc, SlowRate)`；恢复的段 → 解限（`SetSegmentRate(cc, 0)`）。配置门控 `WADIST_SEGMENT_GOVERNOR`（默认 `off`）——关时 Governor 不做段级 pass、无 per-cc limiter、worker 只过全局令牌（M4 行为逐字不变，零爆炸半径）。

**Tech Stack:** Go 1.26、`golang.org/x/time/rate`（per-cc `Limiter`）、PostgreSQL（`campaign_recipients` GROUP BY country_code 封号率，经 `mgr.SystemPool()` BYPASSRLS）、testcontainers-go。

## Global Constraints

- **零爆炸半径**：`WADIST_SEGMENT_GOVERNOR` 默认 `off`；关时 Governor 不调 `SetSegmentRate`，Pump 的 `segLim` 恒空，worker 段级查找命中不到 → 只过全局令牌（M4 行为）。段级只在 `RiskGovernor=="on" && SegmentGovernor=="on"` 时生效（L3 建立在 L4 之上）。
- **健康段不受影响**：无段级 limiter 的 cc（默认）不额外限速；只有被判热的 cc 才套 `SlowRate`。恢复即解限。
- **热段判定**（spec §6.2 L3「某 cc 滚动封号率 > 基线×3」）：`banRate ≥ max(fleetBaseline×Mult, SegSLO)` 且 `attempted ≥ SegMinSample`。`SegSLO` 是绝对下限，防健康舰队（基线≈0）时 ×Mult 仍≈0 而对噪声过敏。
- **舰队作用域**：`SegmentBanRates` 查 `campaign_recipients`（FORCE RLS），必须用 BYPASSRLS 池（`mgr.SystemPool()`），与 M4 `FleetBanRate`、riskbreaker 一致。
- **worker 两级 Wait 的排空语义**：全局与段级两个 `Wait` 都在 ctx 上；任一 error（含关停/deadline-predict）→ 该 payload 落 `context.Background()` 处理，绝不丢已提交未发的工作（沿用 M3/M4 drain 契约）。
- **段级 limiter 并发**：`segLim` map 用 `sync.RWMutex`——worker 读用 RLock（热路径，每发一次），Governor 写(SetSegmentRate)用 Lock。
- **范围**：仅按国家(cc)分段（payload 自带 cc）。**按代理 /24 ASN 分段延后**（payload 不带 proxy /24，需额外查表）。
- whatsmeow/pgx/rate 版本沿用 go.mod。TDD；`nextRate`/热段判定纯逻辑单测；SegmentBanRates/段级 pass 用 testcontainers PG。改 `internal/dispatch` 跑 `make gate`。

---

## 文件结构

- Modify `internal/config/config.go` — 新增 `SegmentGovernor string`(默认"off") + `SegMult/SegSLO/SegSlowRate float64`、`SegMinSample int`，读 `WADIST_SEGMENT_GOVERNOR`/`WADIST_SEG_*`。
- Modify `internal/dispatch/pump.go` — `Pump` 加 `segLim map[string]*rate.Limiter` + `segMu sync.RWMutex`；`SetSegmentRate(cc string, r float64)`；worker 循环在全局 Wait 后加段级 Wait（按 `pl.Country`）。
- Modify `internal/dispatch/governor.go` — `SegStat` + `SegmentBanRates(ctx, pool, windowSec)`（GROUP BY country_code）+ `Governor.WithSegments(setSegRate, SegParams)` + 段级 pass（在 EvaluateOnce 里，enabled 时）+ 已限段集合跟踪。
- Modify `internal/dispatch/governor_test.go` / `pump_test.go` — 段级判定/limiter 单测 + 段级 pass 集成测试。
- Modify `cmd/wadist/main.go` — `SegmentGovernor=="on"` 时 `gov.WithSegments(pump.SetSegmentRate, SegParams{...})`。

---

### Task 1: 段级 Governor 配置

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Config.SegmentGovernor string`(默认"off")、`SegMult float64`(默认 3)、`SegSLO float64`(默认 0.05)、`SegSlowRate float64`(默认 1)、`SegMinSample int`(默认 10)。

- [ ] **Step 1: Write the failing test**

```go
// internal/config/config_test.go (add)
func TestConfig_SegmentGovernorDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SegmentGovernor != "off" {
		t.Fatalf("SegmentGovernor default = %q; want off", cfg.SegmentGovernor)
	}
	if cfg.SegMult != 3 || cfg.SegSLO != 0.05 || cfg.SegSlowRate != 1 {
		t.Fatalf("seg AIMD defaults wrong: mult=%v slo=%v slow=%v", cfg.SegMult, cfg.SegSLO, cfg.SegSlowRate)
	}
	if cfg.SegMinSample != 10 {
		t.Fatalf("SegMinSample = %d; want 10", cfg.SegMinSample)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/config/ -run TestConfig_SegmentGovernorDefaults -v`
Expected: FAIL — fields undefined.

- [ ] **Step 3: Implement**

Add to `Config`:
```go
// SegmentGovernor enables L3 per-country segment slowdown ("on") on top of the
// L4 risk governor, or leaves segments uncapped ("off", default). Requires
// RiskGovernor=="on". Only active in pump dispatch mode.
SegmentGovernor string
// A country cc is "hot" when its window ban rate >= max(fleetBaseline*SegMult,
// SegSLO) and its attempted >= SegMinSample; a hot cc's sends are capped at
// SegSlowRate/s (uncapped again on recovery).
SegMult, SegSLO, SegSlowRate float64
SegMinSample int
```
In `Load()` (after the M4 Gov* block — grep `cfg.GovMinSample`):
```go
cfg.SegmentGovernor = getenv("WADIST_SEGMENT_GOVERNOR", "off")
cfg.SegMult = floatEnv("WADIST_SEG_MULT", 3)
cfg.SegSLO = floatEnv("WADIST_SEG_SLO", 0.05)
cfg.SegSlowRate = floatEnv("WADIST_SEG_SLOW_RATE", 1)
cfg.SegMinSample = intEnv("WADIST_SEG_MIN_SAMPLE", 10)
```
> Reuse existing `getenv`/`intEnv`/`floatEnv`.

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/config/ -run TestConfig_SegmentGovernorDefaults -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): L3 segment governor + params (default off)"
```

---

### Task 2: Pump per-cc 段级 limiter + SetSegmentRate + worker 段级 Wait

**Files:**
- Modify: `internal/dispatch/pump.go`
- Test: `internal/dispatch/pump_test.go`

**Interfaces:**
- Produces on `*Pump`:
  - `func (p *Pump) SetSegmentRate(cc string, r float64)` — `r>0` → 设/建 cc 的 `rate.NewLimiter(rate.Limit(r), 1)`；`r<=0` → 删除该 cc（解限）。thread-safe（segMu.Lock）。
  - worker 循环：全局 `p.lim.Wait` 后，按 `pl.Country` RLock 查段级 limiter，命中则再 `segLimiter.Wait`（error 同样 → sendCtx=Background）。
  - `func (p *Pump) segLimiter(cc string) *rate.Limiter` — RLock 查，未命中返回 nil（helper，测试用）。

- [ ] **Step 1: Write the failing test**

```go
// internal/dispatch/pump_test.go (add)
func TestPump_SetSegmentRate(t *testing.T) {
	pe := NewPumpEnqueuer(1)
	p := NewPump(pe, nil, nil, 100, 2)
	if p.segLimiter("US") != nil {
		t.Fatalf("no segment limiter expected initially")
	}
	p.SetSegmentRate("US", 5)
	lim := p.segLimiter("US")
	if lim == nil || lim.Limit() != rate.Limit(5) {
		t.Fatalf("US segment limiter = %v; want rate 5", lim)
	}
	if p.segLimiter("GB") != nil {
		t.Fatalf("GB must be unaffected (nil)")
	}
	p.SetSegmentRate("US", 0) // uncap → removed
	if p.segLimiter("US") != nil {
		t.Fatalf("US segment limiter should be removed after SetSegmentRate(0)")
	}
}
```
> Add `"golang.org/x/time/rate"` to test imports (likely already present from Task 2 of M4).

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestPump_SetSegmentRate -v`
Expected: FAIL — `SetSegmentRate`/`segLimiter` undefined.

- [ ] **Step 3: Implement**

Add fields to `Pump` struct + init in `NewPump` (`segLim: map[string]*rate.Limiter{}`), then:
```go
// append to internal/dispatch/pump.go
// SetSegmentRate caps sends for a given country cc at r/s (r>0), or uncaps it
// (r<=0 removes the limiter). Used by the L3 segment governor. Thread-safe.
func (p *Pump) SetSegmentRate(cc string, r float64) {
	p.segMu.Lock()
	defer p.segMu.Unlock()
	if r <= 0 {
		delete(p.segLim, cc)
		return
	}
	if lim, ok := p.segLim[cc]; ok {
		lim.SetLimit(rate.Limit(r))
		return
	}
	p.segLim[cc] = rate.NewLimiter(rate.Limit(r), 1)
}

// segLimiter returns the segment limiter for cc, or nil if uncapped.
func (p *Pump) segLimiter(cc string) *rate.Limiter {
	p.segMu.RLock()
	defer p.segMu.RUnlock()
	return p.segLim[cc]
}
```
Add to the `Pump` struct definition: `segMu sync.RWMutex` and `segLim map[string]*rate.Limiter`. In `NewPump`, initialize `segLim: map[string]*rate.Limiter{}`.
In the worker loop (Run), AFTER the global `p.lim.Wait(ctx)` block and BEFORE `p.process(sendCtx, pl)`, insert the segment Wait:
```go
if seg := p.segLimiter(pl.Country); seg != nil {
	if err := seg.Wait(ctx); err != nil {
		sendCtx = context.Background()
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestPump_SetSegmentRate -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/dispatch/pump.go internal/dispatch/pump_test.go
git commit -m "feat(dispatch): per-country segment limiters + worker segment gate"
```

---

### Task 3: SegmentBanRates（按 country_code 分组的封号率）

**Files:**
- Modify: `internal/dispatch/governor.go`
- Test: `internal/dispatch/governor_test.go`

**Interfaces:**
- Produces:
  - `type SegStat struct { CC string; Attempted, BanFailures int }`
  - `func SegmentBanRates(ctx context.Context, pool *pgxpool.Pool, windowSec int) ([]SegStat, error)` — 每 cc 的 definition-A attempted/banFailures（GROUP BY country_code），窗口内。必须用 BYPASSRLS 池（同 FleetBanRate 的 doc 约束）。

- [ ] **Step 1: Write the failing test** (PG: seed per-cc outcomes → grouped rows)

```go
func TestSegmentBanRates(t *testing.T) {
	ctx := context.Background()
	pool := newDispatchTestPool(t) // reuse dispatch PG helper (grep: pgPool)
	// US: 3 sent + 2 ban-failed ; GB: 5 sent + 0 ban-failed
	seedFleetOutcomesCC(t, pool, "US", 3, 2)
	seedFleetOutcomesCC(t, pool, "GB", 5, 0)

	stats, err := SegmentBanRates(ctx, pool, 900)
	if err != nil {
		t.Fatalf("SegmentBanRates: %v", err)
	}
	m := map[string]SegStat{}
	for _, s := range stats {
		m[s.CC] = s
	}
	if m["US"].Attempted != 5 || m["US"].BanFailures != 2 {
		t.Fatalf("US = %+v; want attempted 5 banFailures 2", m["US"])
	}
	if m["GB"].Attempted != 5 || m["GB"].BanFailures != 0 {
		t.Fatalf("GB = %+v; want attempted 5 banFailures 0", m["GB"])
	}
}
```
> `seedFleetOutcomesCC(t, pool, cc, nSent, nBanFailed)`: like M4's `seedFleetOutcomes` but sets `country_code=cc`. Reuse/extend the existing seed helper; grep how M4's governor_test seeds outcomes.

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestSegmentBanRates -v`
Expected: FAIL — `SegmentBanRates` undefined.

- [ ] **Step 3: Implement**

```go
// append to internal/dispatch/governor.go
// SegStat is a country's window send outcomes (definition A).
type SegStat struct {
	CC          string
	Attempted   int
	BanFailures int
}

// SegmentBanRates returns per-country attempted/ban-failure counts over the last
// windowSec (definition A, grouped by country_code). Like FleetBanRate it MUST
// be called with a BYPASSRLS pool (app_system) or RLS on campaign_recipients
// silently scopes results to one tenant.
func SegmentBanRates(ctx context.Context, pool *pgxpool.Pool, windowSec int) ([]SegStat, error) {
	window := fmt.Sprintf("%d seconds", windowSec)
	rows, err := pool.Query(ctx, `
SELECT country_code,
  count(*) FILTER (WHERE state IN ('sent','failed') AND updated_at >= now() - $1::interval),
  count(*) FILTER (WHERE state = 'failed' AND updated_at >= now() - $1::interval AND (
        last_error ILIKE '%wa_warning%' OR last_error ILIKE '%banned%' OR last_error ILIKE '%403%'))
  FROM campaign_recipients
 GROUP BY country_code`, window)
	if err != nil {
		return nil, fmt.Errorf("segment ban rates: %w", err)
	}
	defer rows.Close()
	var out []SegStat
	for rows.Next() {
		var s SegStat
		if err := rows.Scan(&s.CC, &s.Attempted, &s.BanFailures); err != nil {
			return nil, fmt.Errorf("scan segment stat: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestSegmentBanRates -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/dispatch/governor.go internal/dispatch/governor_test.go
git commit -m "feat(dispatch): per-country segment ban-rate query"
```

---

### Task 4: Governor 段级 pass（WithSegments + 热段判定 + 解限跟踪）

**Files:**
- Modify: `internal/dispatch/governor.go`
- Test: `internal/dispatch/governor_test.go`

**Interfaces:**
- Consumes: Task 3 `SegmentBanRates`/`SegStat`.
- Produces:
  - `type SegParams struct { Mult, SegSLO, SlowRate float64; MinSample int }`
  - `func (g *Governor) WithSegments(setSegRate func(cc string, r float64), p SegParams) *Governor` — 启用段级 pass（builder，返回 g）。
  - `func hotSegments(stats []SegStat, p SegParams) map[string]bool` — 纯函数：舰队基线=Σban/Σattempted；某 cc 热 ⟺ `attempted>=MinSample && rate >= max(baseline*Mult, SegSLO)`。
  - Governor.`EvaluateOnce` 里：若段级启用，读 `SegmentBanRates` → `hotSegments` → 对热段 `setSegRate(cc, SlowRate)`（并记入 `capped` 集）；对上轮 capped 但本轮不热的 cc `setSegRate(cc, 0)` 并移出 `capped`。段级 pass 失败只记日志，不影响 L4。

- [ ] **Step 1: Write the failing test** (pure hotSegments + a segment-pass integration test)

```go
func TestHotSegments(t *testing.T) {
	p := SegParams{Mult: 3, SegSLO: 0.05, SlowRate: 1, MinSample: 3}
	stats := []SegStat{
		{CC: "US", Attempted: 10, BanFailures: 4}, // rate .4, baseline=(4+0)/(10+20)=.133 → .4>=max(.4, .05) hot
		{CC: "GB", Attempted: 20, BanFailures: 0}, // rate 0 → not hot
		{CC: "BR", Attempted: 2, BanFailures: 2},  // attempted<MinSample → not hot
	}
	hot := hotSegments(stats, p)
	if !hot["US"] || hot["GB"] || hot["BR"] {
		t.Fatalf("hot = %v; want only US", hot)
	}
}

func TestGovernor_SegmentPass(t *testing.T) {
	ctx := context.Background()
	pool := newDispatchTestPool(t)
	seg := map[string]float64{}
	g := NewGovernor(pool, func(float64) {}, GovParams{SLO: 0.02, Step: 5, Factor: 0.5, MinRate: 1, MaxRate: 160}, 900, 3, 100).
		WithSegments(func(cc string, r float64) { seg[cc] = r }, SegParams{Mult: 3, SegSLO: 0.05, SlowRate: 1, MinSample: 3})

	seedFleetOutcomesCC(t, pool, "US", 2, 4) // US hot (rate .667)
	seedFleetOutcomesCC(t, pool, "GB", 10, 0)
	if _, _, err := g.EvaluateOnce(ctx); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if seg["US"] != 1 {
		t.Fatalf("US should be capped to SlowRate 1; seg=%v", seg)
	}
	if _, ok := seg["GB"]; ok {
		t.Fatalf("GB should never be capped; seg=%v", seg)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run 'TestHotSegments|TestGovernor_SegmentPass' -v`
Expected: FAIL — `hotSegments`/`WithSegments` undefined.

- [ ] **Step 3: Implement**

```go
// append to internal/dispatch/governor.go
type SegParams struct {
	Mult, SegSLO, SlowRate float64
	MinSample              int
}

// hotSegments returns the ccs whose window ban rate is elevated: attempted >=
// MinSample AND rate >= max(fleetBaseline*Mult, SegSLO). fleetBaseline is the
// pooled ban rate across all segments (SegSLO floors it so a healthy fleet
// doesn't flag noise).
func hotSegments(stats []SegStat, p SegParams) map[string]bool {
	var totAtt, totBan int
	for _, s := range stats {
		totAtt += s.Attempted
		totBan += s.BanFailures
	}
	var baseline float64
	if totAtt > 0 {
		baseline = float64(totBan) / float64(totAtt)
	}
	threshold := baseline * p.Mult
	if p.SegSLO > threshold {
		threshold = p.SegSLO
	}
	hot := map[string]bool{}
	for _, s := range stats {
		if s.Attempted < p.MinSample {
			continue
		}
		rate := float64(s.BanFailures) / float64(s.Attempted)
		if rate >= threshold {
			hot[s.CC] = true
		}
	}
	return hot
}
```
Add to `Governor` struct: `setSegRate func(cc string, r float64)`, `segParams SegParams`, `capped map[string]bool` (nil until WithSegments). Then:
```go
func (g *Governor) WithSegments(setSegRate func(cc string, r float64), p SegParams) *Governor {
	g.setSegRate = setSegRate
	g.segParams = p
	g.capped = map[string]bool{}
	return g
}

// evaluateSegments runs one L3 pass: cap hot ccs at SlowRate, uncap recovered
// ones. No-op when segments aren't enabled (setSegRate nil).
func (g *Governor) evaluateSegments(ctx context.Context) error {
	if g.setSegRate == nil {
		return nil
	}
	stats, err := SegmentBanRates(ctx, g.pool, g.windowSec)
	if err != nil {
		return err
	}
	hot := hotSegments(stats, g.segParams)
	for cc := range hot {
		g.setSegRate(cc, g.segParams.SlowRate)
		g.capped[cc] = true
	}
	for cc := range g.capped {
		if !hot[cc] {
			g.setSegRate(cc, 0) // recovered → uncap
			delete(g.capped, cc)
		}
	}
	return nil
}
```
In `EvaluateOnce`, after the L4 apply/skip logic, call the segment pass (log-and-continue, don't fail L4):
```go
	if serr := g.evaluateSegments(ctx); serr != nil {
		log.Printf("dispatch: segment governor: %v", serr)
	}
```
(Insert just before the final `return` of EvaluateOnce, so L4's return value is unchanged.)

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run 'TestHotSegments|TestGovernor_SegmentPass' -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/dispatch/governor.go internal/dispatch/governor_test.go
git commit -m "feat(dispatch): L3 segment pass (hot-cc detect, cap/uncap tracking)"
```

---

### Task 5: main.go 装配 + e2e + make gate

**Files:**
- Modify: `cmd/wadist/main.go`
- Test: `internal/dispatch/governor_test.go` (recover-uncaps assertion) + full gate

**Interfaces:**
- Consumes: Task 2 `Pump.SetSegmentRate`, Task 4 `Governor.WithSegments`.

- [ ] **Step 1: Wire the segment governor**

In `cmd/wadist/main.go`, inside the `if cfg.RiskGovernor == "on" {` block (M4), AFTER `gov := dispatch.NewGovernor(...)` and BEFORE the `sup.Go(gov.Run)`:
```go
if cfg.SegmentGovernor == "on" {
	gov.WithSegments(pump.SetSegmentRate, dispatch.SegParams{
		Mult: cfg.SegMult, SegSLO: cfg.SegSLO, SlowRate: cfg.SegSlowRate, MinSample: cfg.SegMinSample,
	})
	log.Printf("segment governor on (mult=%.1f segSLO=%.3f slow=%.1f/s minSample=%d)",
		cfg.SegMult, cfg.SegSLO, cfg.SegSlowRate, cfg.SegMinSample)
}
```
`WithSegments` mutates the same `gov` before it's started, so `sup.Go(gov.Run)` picks it up. Segment governance is only enabled when BOTH RiskGovernor and SegmentGovernor are "on" (L3 sits on L4). Default (SegmentGovernor off) → `gov.setSegRate` stays nil → `evaluateSegments` no-ops → M4 behavior, zero blast radius.

- [ ] **Step 2: Add a recover-uncaps e2e assertion**

In `governor_test.go` add `TestGovernor_SegmentRecovers`: US hot → capped to SlowRate; then seed many clean US sends so US ban rate drops below threshold → next EvaluateOnce uncaps US (`setSegRate("US", 0)` called, US removed from capped).

```go
func TestGovernor_SegmentRecovers(t *testing.T) {
	ctx := context.Background()
	pool := newDispatchTestPool(t)
	seg := map[string]float64{}
	g := NewGovernor(pool, func(float64) {}, GovParams{SLO: 0.02, Step: 5, Factor: 0.5, MinRate: 1, MaxRate: 160}, 900, 3, 100).
		WithSegments(func(cc string, r float64) { seg[cc] = r }, SegParams{Mult: 3, SegSLO: 0.05, SlowRate: 1, MinSample: 3})

	seedFleetOutcomesCC(t, pool, "US", 2, 4) // hot
	_, _, _ = g.EvaluateOnce(ctx)
	if seg["US"] != 1 {
		t.Fatalf("US should be capped; seg=%v", seg)
	}
	seedFleetOutcomesCC(t, pool, "US", 400, 0) // heal
	_, _, _ = g.EvaluateOnce(ctx)
	if seg["US"] != 0 {
		t.Fatalf("US should be uncapped (0) after recovery; seg=%v", seg)
	}
}
```

- [ ] **Step 3: Verify build + full gate**

Run: `cd /var/klwa && go build ./... && go vet ./... && go test ./internal/dispatch/ -run 'TestPump_SetSegmentRate|TestSegment|TestHotSegments|TestGovernor_Segment' -race && make gate`
Expected: build OK; segment tests pass; `make gate` GREEN. Default `WADIST_SEGMENT_GOVERNOR` unset → off → no segment limiters, existing behavior unchanged. (NOTE the known pre-existing TestPump_EndToEnd/RateLimits parallel-testcontainer flake — if only those flake, verify in isolation and report honestly; don't claim green if a non-pump test fails.)

- [ ] **Step 4: Commit**

```bash
cd /var/klwa
git add cmd/wadist/main.go internal/dispatch/governor_test.go
git commit -m "feat(wadist): wire L3 segment governor (default off) + recover e2e; gate green"
```

---

## Self-Review 结论（作者已核对）

- **Spec 覆盖**（§6.2 L3「某 cc 滚动封号率 > 基线×3 → 该段 τ 降速」）：per-cc 令牌桶 = Task 2；per-cc 封号率 = Task 3；热段判定(基线×Mult，SegSLO 下限) + 降速/恢复 = Task 4；装配 = Task 5。per-ASN /24 显式延后（payload 无 proxy /24）。
- **占位符扫描**：无 TODO/“类似上文”。测试助手（newDispatchTestPool/pgPool/seedFleetOutcomesCC）标注复用现有 harness；`seedFleetOutcomesCC` 是 M4 `seedFleetOutcomes` 的 cc 变体，需按真实签名扩展。env 助手复用。
- **类型一致性**：`SetSegmentRate`/`segLimiter`/`segLim`/`segMu`、`SegStat`/`SegmentBanRates`、`SegParams`/`hotSegments`/`WithSegments`/`evaluateSegments`/`capped`、`Config.SegmentGovernor/Seg*` 跨 Task 一致；SlowRate=cfg.SegSlowRate、SegSLO 语义一致。
- **零爆炸半径**：`SegmentGovernor` 默认 off；`WithSegments` 不调用 → `setSegRate` nil → `evaluateSegments` no-op → `segLim` 空 → worker 段级查找 nil → 只过全局令牌（M4）。

## 已知风险与后续（非阻塞，记入执行账本）

- **per-ASN /24 段延后**：需 payload 带 proxy /24 或按 jid→proxy 查表；M5 只按 cc。
- **热段降速用绝对 SlowRate**（默认 1/s）而非按全局 τ 比例；简单可预测，标定留 canary。
- **两级 Wait 的公平性**：热段 payload 先取全局令牌再取段级令牌——极端情况下热段 worker 阻塞在段级 Wait 时仍占着一个已取的全局令牌（轻微浪费）；worker 池 ≥ 段数时可忽略，记为后续观测。
- **SegmentBanRates 全表 GROUP BY**：每 interval 一次；与 FleetBanRate 同表，考虑 `(country_code, state, updated_at)` 索引（后续）。
- **capped 集合内存**：per-cc bool，规模=国家数，可忽略。
