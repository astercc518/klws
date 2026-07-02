# Scheduler 调度闭环对账（Reconciler）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 `control.Reconciler`：周期性把"既非温驻留、又不在 due 队列"的活跃账号重新纳入主动预热调度，合上"启动播种后 due-ZSet 只出不进"的闭环——覆盖启动为空、运行中新建/解封、重启丢失、驱逐后回收。

**Architecture:** Reconciler 纯逻辑、零引擎依赖，靠注入回调（`ListActive`/`CountryOf`/`NextDue`）+ 直接读写 Redis（`ctl:resident` set、`due:{cc}` ZSet，与既有 WorkingSet/Scheduler 同结构）。复用既有 `Scheduler.Enqueue` 与 `NextEligibleMs`，不改 dispatch/WorkingSet 逻辑。

**Tech Stack:** Go 1.26、go-redis/v9、prometheus/client_golang、testcontainers Redis7（复用 `internal/control/redis_helper_test.go` 的 `newTestRedis`）。设计见 [`../specs/2026-07-02-control-plane-selfheal-design.md`](../specs/2026-07-02-control-plane-selfheal-design.md)。

## Global Constraints

- 红线（[[klws-redlines]]）：**不改** `billing`/`dispatch`/`store`/`sendgate`/`cluster` 逻辑，也**不改**既有 `WorkingSet`/`Scheduler` 行为。本计划全部为 `internal/control` 新增 + `metrics`/`config`/`cmd` 加性接线。
- `internal/control` **禁止 import** node/cluster/store——只收回调与 `*goredis.Client`。
- 复用既有 Redis key：`ctl:resident`（[workingset.go:37](../../../internal/control/workingset.go#L37)）、`due:{cc}`（[scheduler.go:14](../../../internal/control/scheduler.go#L14) `dueKey`）。
- 幂等：`Scheduler.Enqueue` 用 ZADD（同 member 覆盖 score），重复对账不产生重复项。
- 指标 label 有界：新增计数器**无 label**。
- **范围外**（另开 spec）：工作集轮换（KeepWarmHorizon 温龄驱逐）、Risk Governor。Reconciler 只保证"该被调度的都在被调度"，不保证公平轮换。

## File Structure

- Create `internal/control/reconciler.go` — `Reconciler`（Tick/Run）+ `ReconcileDeps`。
- Create `internal/control/reconciler_test.go` — testcontainer Redis + fake 回调。
- Modify `internal/metrics/metrics.go` — 新增 `wadist_schedule_reconciled_total` 计数器 + `IncScheduleReconciled()`。
- Modify `internal/config/config.go` + `internal/config/config_test.go` — reconcile interval env。
- Modify `cmd/wadist/main.go` — 装配 Reconciler 回调并 `sup.Go(reconciler.Run)`。

---

## Task 1: metrics 新增 wadist_schedule_reconciled_total

**Files:**
- Modify: `internal/metrics/metrics.go`（四处比照 `noCapacity`：字段 [:23](../../../internal/metrics/metrics.go#L23)、构造 [:53](../../../internal/metrics/metrics.go#L53)、注册 [:74](../../../internal/metrics/metrics.go#L74)、Inc 方法 [:125](../../../internal/metrics/metrics.go#L125)）
- Test: `internal/metrics/metrics_test.go`

**Interfaces:**
- Produces: `func (m *Metrics) IncScheduleReconciled(n int)`（一次补入 n 个账号）

- [ ] **Step 1: 写失败测试**

```go
// internal/metrics/metrics_test.go 追加
func TestIncScheduleReconciled(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.IncScheduleReconciled(3)
	m.IncScheduleReconciled(2)
	if got := testutil.ToFloat64(m.scheduleReconciled); got != 5 {
		t.Fatalf("schedule_reconciled=%v want 5", got)
	}
}
```

> import `"github.com/prometheus/client_golang/prometheus"` 与 `.../prometheus/testutil"`。

Run: `go test ./internal/metrics/ -run TestIncScheduleReconciled -count=1`
Expected: FAIL（未定义）

- [ ] **Step 2: 实现（metrics.go 四处，比照 noCapacity）**

字段：

```go
	scheduleReconciled prometheus.Counter
```

构造（`New` 内）：

```go
	m.scheduleReconciled = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "wadist_schedule_reconciled_total",
		Help: "Active accounts re-enqueued into the due-ZSet by the reconciler.",
	})
```

注册：把 `m.scheduleReconciled` 加入 `reg.MustRegister(...)` 列表。

Inc 方法：

```go
func (m *Metrics) IncScheduleReconciled(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.scheduleReconciled.Add(float64(n))
}
```

- [ ] **Step 3: 运行确认通过 + label 门禁**

Run: `go test ./internal/metrics/ -run TestIncScheduleReconciled -count=1 && ./scripts/check_metric_labels.sh`
Expected: PASS

- [ ] **Step 4: 提交**

```bash
git add internal/metrics/metrics.go internal/metrics/metrics_test.go
git commit -m "feat(metrics): add wadist_schedule_reconciled_total counter"
```

---

## Task 2: control.Reconciler（Tick + Run）

**Files:**
- Create: `internal/control/reconciler.go`
- Test: `internal/control/reconciler_test.go`

**Interfaces:**
- Consumes: `Scheduler`（现有）、`dueKey`（同包）、`residentKey`（同包 [workingset.go:37](../../../internal/control/workingset.go#L37)）
- Produces:
  - `type ReconcileDeps struct{ ListActive func(ctx) ([]string, error); CountryOf func(ctx, jid string) string; NextDue func(cc string) int64 }`
  - `func NewReconciler(rdb *goredis.Client, sched *Scheduler, ccs []string, deps ReconcileDeps) *Reconciler`
  - `func (r *Reconciler) Tick(ctx context.Context, nowMs int64) (int, error)`
  - `func (r *Reconciler) Run(ctx context.Context, interval time.Duration) error`

- [ ] **Step 1: 写失败测试 — 补入既非温、又未排队的账号**

```go
// internal/control/reconciler_test.go
package control

import (
	"context"
	"testing"
	"time"
)

func TestReconcilerTick_EnqueuesUnscheduled(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	rdb := newTestRedis(t)
	sched := NewScheduler(rdb)

	// resident（已温）：r1；已排队：q1（在 due:US）。应被跳过。
	rdb.SAdd(ctx, residentKey, "r1")
	_ = sched.Enqueue(ctx, "q1", "US", 5000)

	active := []string{"r1", "q1", "fresh1", "fresh2"} // fresh* 既非温又未排队
	cc := map[string]string{"r1": "US", "q1": "US", "fresh1": "US", "fresh2": "IN"}

	r := NewReconciler(rdb, sched, []string{"US", "IN"}, ReconcileDeps{
		ListActive: func(_ context.Context) ([]string, error) { return active, nil },
		CountryOf:  func(_ context.Context, jid string) string { return cc[jid] },
		NextDue:    func(_ string) int64 { return 9000 },
	})

	n, err := r.Tick(ctx, 1000)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n != 2 {
		t.Fatalf("reconciled n=%d want 2 (fresh1, fresh2)", n)
	}
	// fresh1 进 due:US，fresh2 进 due:IN，score=NextDue=9000。
	if s := rdb.ZScore(ctx, dueKey("US"), "fresh1").Val(); s != 9000 {
		t.Fatalf("fresh1 due:US score=%v want 9000", s)
	}
	if s := rdb.ZScore(ctx, dueKey("IN"), "fresh2").Val(); s != 9000 {
		t.Fatalf("fresh2 due:IN score=%v want 9000", s)
	}
	// r1（温）与 q1（已排队）不应被改动：q1 score 仍 5000，r1 不入 due。
	if s := rdb.ZScore(ctx, dueKey("US"), "q1").Val(); s != 5000 {
		t.Fatalf("q1 score=%v want unchanged 5000", s)
	}
	if rdb.ZScore(ctx, dueKey("US"), "r1").Err() == nil {
		t.Fatal("r1 (resident) should not be enqueued")
	}
}

func TestReconcilerTick_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	rdb := newTestRedis(t)
	sched := NewScheduler(rdb)
	r := NewReconciler(rdb, sched, []string{"US"}, ReconcileDeps{
		ListActive: func(_ context.Context) ([]string, error) { return []string{"x"}, nil },
		CountryOf:  func(_ context.Context, _ string) string { return "US" },
		NextDue:    func(_ string) int64 { return 7000 },
	})
	n1, _ := r.Tick(ctx, 1000)
	n2, _ := r.Tick(ctx, 1000) // 第二次：x 已在 due → 不再补入
	if n1 != 1 || n2 != 0 {
		t.Fatalf("n1=%d n2=%d want 1/0 (idempotent)", n1, n2)
	}
	if c := rdb.ZCard(ctx, dueKey("US")).Val(); c != 1 {
		t.Fatalf("due:US card=%d want 1 (no dup)", c)
	}
}
```

Run: `go test ./internal/control/ -run TestReconciler -count=1`
Expected: FAIL（未定义）

- [ ] **Step 2: 实现（reconciler.go）**

```go
// internal/control/reconciler.go
package control

import (
	"context"
	"log"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// ReconcileDeps 注入回调，保持 control 零引擎依赖。
type ReconcileDeps struct {
	ListActive func(ctx context.Context) ([]string, error) // 跨租户活跃账号
	CountryOf  func(ctx context.Context, jid string) string
	NextDue    func(cc string) int64 // 新调度项的 due 时间（含抖动，由 main 注入）
}

type Reconciler struct {
	rdb   *goredis.Client
	sched *Scheduler
	ccs   []string
	deps  ReconcileDeps
}

func NewReconciler(rdb *goredis.Client, sched *Scheduler, ccs []string, deps ReconcileDeps) *Reconciler {
	return &Reconciler{rdb: rdb, sched: sched, ccs: ccs, deps: deps}
}

// Tick 把"既非温驻留、又不在任一 due 队列"的活跃账号补入 due-ZSet。
// 返回本轮补入的账号数。
func (r *Reconciler) Tick(ctx context.Context, nowMs int64) (int, error) {
	active, err := r.deps.ListActive(ctx)
	if err != nil {
		return 0, err
	}

	// 一次性把 resident 与各国 due 成员拉进内存 set，避免每账号往返 Redis。
	resident := map[string]struct{}{}
	for _, jid := range r.rdb.SMembers(ctx, residentKey).Val() {
		resident[jid] = struct{}{}
	}
	scheduled := map[string]struct{}{}
	for _, cc := range r.ccs {
		for _, jid := range r.rdb.ZRange(ctx, dueKey(cc), 0, -1).Val() {
			scheduled[jid] = struct{}{}
		}
	}

	added := 0
	for _, jid := range active {
		if _, warm := resident[jid]; warm {
			continue // 已温
		}
		if _, queued := scheduled[jid]; queued {
			continue // 已排队
		}
		cc := r.deps.CountryOf(ctx, jid)
		if err := r.sched.Enqueue(ctx, jid, cc, r.deps.NextDue(cc)); err != nil {
			log.Printf("control: reconciler enqueue %s: %v", jid, err)
			continue
		}
		added++
	}
	return added, nil
}

// Run 每 interval 调 Tick；interval<=0 表示禁用。Tick 错误记录并继续。
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if n, err := r.Tick(ctx, time.Now().UnixMilli()); err != nil {
				log.Printf("control: reconciler tick error: %v", err)
			} else if n > 0 {
				log.Printf("control: reconciler re-enqueued %d unscheduled accounts", n)
			}
		}
	}
}
```

> 注：CountryOf 返回 "" 的账号进 `due:""` 队列（无国家池）；WorkingSet 的 `ccs` 若不含 "" 则该账号只能靠 warm:req 被动补——与既有语义一致（见控制面计划 Task 5 注）。若需覆盖，main 的 `activeCountries` 已由 `loadActiveCountries` 决定。

- [ ] **Step 3: 运行确认通过**

Run: `go test ./internal/control/ -run TestReconciler -count=1`
Expected: PASS

- [ ] **Step 4: 依赖隔离核对**

Run: `go list -deps ./internal/control/ | grep -E 'wadist/internal/(node|cluster|store)$' || echo "clean: no engine imports"`
Expected: `clean: no engine imports`

- [ ] **Step 5: 提交**

```bash
git add internal/control/reconciler.go internal/control/reconciler_test.go
git commit -m "feat(control): Reconciler closes the scheduling loop (re-enqueue unscheduled accounts)"
```

---

## Task 3: 装配 — config + cmd/wadist

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/wadist/main.go`

**Interfaces:**
- Consumes: `store.ListActiveAccounts`（现有，跨租户）、`cmd` 内既有 `countryOf`/`randQuota` helper（[main.go:432/441](../../../cmd/wadist/main.go#L432)）、`control.NextEligibleMs`/`control.Window`（现有）、`control.NewReconciler`（Task 2）、`metrics.IncScheduleReconciled`（Task 1）、现有 `rdb`/`mgr`/`sched`/`sup`/`m`/`activeCountries`/`cfg`。

- [ ] **Step 1: 写失败测试 — reconcile 默认配置**

```go
// internal/config/config_test.go 追加
func TestLoad_ReconcileDefault(t *testing.T) {
	t.Setenv("WADIST_REDIS_ADDR", "x:1")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReconcileInterval != 5*time.Minute {
		t.Fatalf("ReconcileInterval=%v want 5m", cfg.ReconcileInterval)
	}
}
```

Run: `go test ./internal/config/ -run TestLoad_ReconcileDefault -count=1`
Expected: FAIL

- [ ] **Step 2: config 加字段 + 加载（config.go）**

`Config` struct 加：

```go
	ReconcileInterval time.Duration
```

`Load()` 内加：

```go
	cfg.ReconcileInterval = msEnv("WADIST_RECONCILE_INTERVAL_MS", 300000)
```

Run: `go test ./internal/config/ -run TestLoad_ReconcileDefault -count=1`
Expected: PASS

- [ ] **Step 3: cmd/wadist 接线**

在 `sched`/`activeCountries`/`m` 已就位、`sup.Go(ws.Run...)` 附近，新增：

```go
	reconciler := control.NewReconciler(rdb, sched, activeCountries, control.ReconcileDeps{
		ListActive: mgr.ListActiveAccounts,
		CountryOf:  func(ctx context.Context, jid string) string { return countryOf(ctx, mgr, jid) },
		NextDue: func(cc string) int64 {
			now := time.Now().UnixMilli()
			// 复用启动播种同款去同步公式；rng 每次新建，避免跨 goroutine 共享。
			rng := rand.New(rand.NewSource(now)) //nolint:gosec
			q := randQuota(rng, cfg.DailyQuotaMin, cfg.DailyQuotaMax)
			return control.NextEligibleMs(now, q, control.Window{StartHour: 9, EndHour: 22}, rng)
		},
	})
	sup.Go(func(lctx context.Context) error {
		err := reconciler.Run(lctx, cfg.ReconcileInterval)
		return err
	})
```

> 若要接指标：把 `reconciler.Run` 换成一个薄封装 —— 由于 `IncScheduleReconciled` 需要每 Tick 的返回值，最简做法是在 main 内自建 ticker 调 `reconciler.Tick` 并在返回后 `m.IncScheduleReconciled(n)`。若沿用 `reconciler.Run`（自带 ticker）则指标暂由日志覆盖；二选一，推荐前者以上报指标：

```go
	sup.Go(func(lctx context.Context) error {
		if cfg.ReconcileInterval <= 0 {
			<-lctx.Done()
			return lctx.Err()
		}
		t := time.NewTicker(cfg.ReconcileInterval)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return lctx.Err()
			case <-t.C:
				n, err := reconciler.Tick(lctx, time.Now().UnixMilli())
				if err != nil {
					log.Printf("reconciler: %v", err)
					continue
				}
				m.IncScheduleReconciled(n)
			}
		}
	})
```

> 采用上面第二段（自建 ticker 上报指标）即可，不再调用 `reconciler.Run`——`Run` 保留供无指标场景/测试使用。

- [ ] **Step 4: 构建 + 全量测试**

Run: `go build ./... && go test ./internal/control/ ./internal/metrics/ ./internal/config/ -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/wadist/main.go
git commit -m "feat: wire scheduler Reconciler into node startup (close scheduling loop)"
```

---

## 验收（合并前）

- [ ] `make gate` 绿。
- [ ] `go list -deps ./internal/control/` 无 node/cluster/store import。
- [ ] Reconciler Tick：温账号跳过、已排队账号跳过（score 不变）、fresh 账号按 cc 补入 `due:{cc}` 且 score=NextDue；重复 Tick 幂等（无重复项）。
- [ ] `wadist_schedule_reconciled_total` 随补入递增，无高基数 label。
- [ ] 空启动后运行中插入 active 账号，≤1 个 reconcile 周期后其出现在 `due:{cc}`。
- [ ] 未改 dispatch/WorkingSet/Scheduler 既有逻辑。

## Self-Review 备注

- **闭环正确性**：`StartAccountWithLock` 对本节点已持锁账号返回 `(nil,nil)`（[orchestrator.go:59](../../../internal/node/orchestrator.go#L59)），故即便 reconciler 误把某温账号补入 due 并被 PopDue 再 warm 也无害（幂等，不重连）。但 Tick 已用 resident set 过滤温账号，正常路径不会发生。
- **性能**：Tick 每轮 1×SMEMBERS + N×ZRANGE(全量)，100K 池在 5min 周期下可接受；如需更省，可改 ZRANGEBYSCORE 分段或维护计数。v1 保持简单。
- **types 一致性**：`ReconcileDeps.NextDue func(cc string) int64` 与 `control.NextEligibleMs(nowMs, quota, Window, rng) int64` 由 main 适配（Task 3），签名不同、显式桥接。
- **范围**：本计划不含工作集温龄轮换（KeepWarmHorizon 驱逐）——那是让 100K 池公平轮换的独立增强，另开 spec。Reconciler 仅修"调度项丢失"这一自愈缺口。
- 与 [`2026-07-02-proxy-janitor.md`](2026-07-02-proxy-janitor.md) 无耦合，可独立先后执行。
