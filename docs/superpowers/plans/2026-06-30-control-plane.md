# 控制面编排（温驻留 + 去同步调度 + 代理粘性）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 `internal/control` 包：在 100K 账号池中维持 ~1500 温会话工作集，按去同步到期调度滚动轮换、代理终身粘性，让现有 campaign-dispatch 选到在线账号——全程不改 dispatch/sendgate/billing。

**Architecture:** `control` 包纯逻辑 + 注入回调（`warmFn`/`evictFn`/`isWarmFn`/`bindProxy`），不 import node/cluster（避免依赖环、可单测）。主动预热 due-ZSet 头部 + 被动消费 `warm:req`（dispatch 选到冷账号时由 RoutingSender LPUSH）双向收敛。

**Tech Stack:** Go 1.26、go-redis/v9、testcontainers Redis7。依赖 B2 计划（StartAccountWithLock 返回的会话已温）与反指纹计划（`Session.GracefulClose`、RoutingSender warm:req 钩子）。

## Global Constraints

- 红线：**不改** `internal/dispatch`/`internal/sendgate`/`internal/billing` 逻辑。新代码在 `internal/control`；`cmd/wadist`（装配）、`cluster/sender.go`（warm:req 钩子，加性）、`internal/config` 可改。
- `control` 包**禁止 import `internal/node`、`internal/cluster`、`internal/store`**——只收回调与 `*goredis.Client`。
- Redis 库别名 `goredis "github.com/redis/go-redis/v9"`。
- 去同步：相邻发送硬下限 ≥60s（与 sendgate 3s minGap 叠加）。
- 代理粘性：仅首次/代理死亡才 `BindProxy`，否则复用 `GetBoundProxy`。
- v1 不做 Risk Governor（AIMD）与短链——各自后续独立计划。

## File Structure

- Create `internal/control/scheduler.go` — `Scheduler`（due:{cc} ZSet）+ `NextEligibleMs` + `Window`。
- Create `internal/control/workingset.go` — `WorkingSet`（Tick/Run）+ `Config`。
- Create `internal/control/proxyaffinity.go` — 粘性绑定包装。
- Create `internal/control/*_test.go` — fake 回调 + testcontainer Redis。
- Modify `internal/cluster/sender.go` — `RoutingSender` 无会话时 `LPUSH warm:req`（注入 rdb，可空）。
- Modify `internal/config/config.go` — WarmTarget/tick/horizon/quota/window env。
- Modify `cmd/wadist/main.go` — startup 改 SeedDue + WorkingSet.Run（替换全量 StartAccounts），注入回调。

---

## Task 1: Scheduler（due:{cc} ZSet）+ 去同步公式

**Files:**
- Create: `internal/control/scheduler.go`
- Test: `internal/control/scheduler_test.go`、`internal/control/redis_helper_test.go`

**Interfaces:**
- Produces:
  - `type Scheduler struct{ rdb *goredis.Client }` + `NewScheduler(rdb)`
  - `func (s *Scheduler) Enqueue(ctx context.Context, jid, cc string, nextMs int64) error`
  - `func (s *Scheduler) PopDue(ctx context.Context, cc string, nowMs int64, max int) ([]string, error)`
  - `type Window struct{ StartHour, EndHour int }` + `func (w Window) SecondsActive() int64`
  - `func NextEligibleMs(nowMs int64, quotaToday int, w Window, rng *rand.Rand) int64`

- [ ] **Step 1: Redis 测试夹具**（`internal/control/redis_helper_test.go`）

```go
package control

import (
	"context"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func newTestRedis(t *testing.T) *goredis.Client {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "redis:7", ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp"),
		}, Started: true,
	})
	if err != nil { t.Fatalf("redis: %v", err) }
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "6379/tcp")
	rdb := goredis.NewClient(&goredis.Options{Addr: host + ":" + port.Port()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}
```

- [ ] **Step 2: 失败测试 — PopDue 只弹 score<=now、按序**

```go
// internal/control/scheduler_test.go
package control

import (
	"context"
	"testing"
)

func TestSchedulerPopDue(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	s := NewScheduler(newTestRedis(t))
	_ = s.Enqueue(ctx, "jid-late", "US", 10_000)
	_ = s.Enqueue(ctx, "jid-due1", "US", 1_000)
	_ = s.Enqueue(ctx, "jid-due2", "US", 2_000)

	got, err := s.PopDue(ctx, "US", 5_000, 10)
	if err != nil { t.Fatal(err) }
	if len(got) != 2 || got[0] != "jid-due1" || got[1] != "jid-due2" {
		t.Fatalf("popDue=%v want [jid-due1 jid-due2]", got)
	}
	// 未到期的仍在
	again, _ := s.PopDue(ctx, "US", 5_000, 10)
	if len(again) != 0 { t.Fatalf("second pop should be empty, got %v", again) }
}
```

Run: `go test ./internal/control/ -run TestSchedulerPopDue -count=1`
Expected: FAIL（未定义）

- [ ] **Step 3: 实现 Scheduler（scheduler.go）**

```go
// internal/control/scheduler.go
package control

import (
	"context"
	"math/rand"
	"strconv"

	goredis "github.com/redis/go-redis/v9"
)

type Scheduler struct{ rdb *goredis.Client }

func NewScheduler(rdb *goredis.Client) *Scheduler { return &Scheduler{rdb: rdb} }

func dueKey(cc string) string { return "due:" + cc }

func (s *Scheduler) Enqueue(ctx context.Context, jid, cc string, nextMs int64) error {
	return s.rdb.ZAdd(ctx, dueKey(cc), goredis.Z{Score: float64(nextMs), Member: jid}).Err()
}

// PopDue 原子弹出 score<=nowMs 的最多 max 个成员（按 score 升序）。
func (s *Scheduler) PopDue(ctx context.Context, cc string, nowMs int64, max int) ([]string, error) {
	res, err := popDueLua.Run(ctx, s.rdb, []string{dueKey(cc)}, nowMs, max).StringSlice()
	if err != nil { return nil, err }
	return res, nil
}

// KEYS=[due:{cc}] ARGV=[nowMs, max]
var popDueLua = goredis.NewScript(`
local due = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, tonumber(ARGV[2]))
if #due > 0 then redis.call('ZREM', KEYS[1], unpack(due)) end
return due`)

// SeedDue 启动播种：把账号按打散相位写入各自国家队列。
type AccountCC struct{ JID, CC string; NextMs int64 }
func (s *Scheduler) SeedDue(ctx context.Context, accts []AccountCC) error {
	pipe := s.rdb.Pipeline()
	for _, a := range accts { pipe.ZAdd(ctx, dueKey(a.CC), goredis.Z{Score: float64(a.NextMs), Member: a.JID}) }
	_, err := pipe.Exec(ctx)
	return err
}

var _ = strconv.Itoa // keep import if unused elsewhere
var _ = rand.Int
```

- [ ] **Step 4: 测试转绿**

Run: `go test ./internal/control/ -run TestSchedulerPopDue -count=1`
Expected: PASS

- [ ] **Step 5: 失败测试 — NextEligibleMs 去同步**

```go
// scheduler_test.go 追加
func TestNextEligibleSpread(t *testing.T) {
	w := Window{StartHour: 9, EndHour: 22}
	rng := rand.New(rand.NewSource(1))
	now := int64(1_000_000_000_000)
	for i := 0; i < 1000; i++ {
		got := NextEligibleMs(now, 7, w, rng)
		delta := got - now
		if delta < 60_000 { t.Fatalf("delta %dms < 60s 硬下限", delta) }
	}
}
```

（`scheduler_test.go` 顶部 import `"math/rand"`。）

Run: `go test ./internal/control/ -run TestNextEligibleSpread -count=1`
Expected: FAIL（`NextEligibleMs`/`Window` 未定义）

- [ ] **Step 6: 实现 Window + NextEligibleMs（scheduler.go 追加）**

```go
type Window struct{ StartHour, EndHour int }
func (w Window) SecondsActive() int64 { return int64((w.EndHour - w.StartHour)) * 3600 }

// NextEligibleMs 均匀基距 ± 40% 抖动，硬下限 60s。窗外顺延逻辑由调用方处理。
func NextEligibleMs(nowMs int64, quotaToday int, w Window, rng *rand.Rand) int64 {
	if quotaToday < 1 { quotaToday = 1 }
	base := w.SecondsActive() / int64(quotaToday)
	jitter := int64(float64(base) * (rng.Float64()*0.8 - 0.4))
	delta := base + jitter
	if delta < 60 { delta = 60 }
	return nowMs + delta*1000
}
```

- [ ] **Step 7: 测试转绿 + 提交**

Run: `go test ./internal/control/ -run 'PopDue|NextEligible' -count=1`
Expected: PASS

```bash
git add internal/control/scheduler.go internal/control/scheduler_test.go internal/control/redis_helper_test.go
git commit -m "feat(control): due-ZSet scheduler + de-synchronized NextEligibleMs"
```

---

## Task 2: WorkingSet（主动预热 + 被动补热 + 驱逐）

**Files:**
- Create: `internal/control/workingset.go`
- Test: `internal/control/workingset_test.go`

**Interfaces:**
- Consumes: `Scheduler`（Task 1）
- Produces:
  - `type Config struct{ Target, WarmReqBatch int; KeepWarmHorizon, Linger time.Duration }`
  - `type Deps struct{ Warm func(ctx, jid string) (bool, error); Evict func(ctx, jid string, linger time.Duration); IsWarm func(jid string) bool; BindProxy func(ctx, jid, cc string) error }`
  - `type WorkingSet struct{ ... }` + `NewWorkingSet(rdb, sched, ccs []string, cfg Config, deps Deps)`
  - `func (w *WorkingSet) Tick(ctx context.Context, nowMs int64) error`

- [ ] **Step 1: 失败测试 — 主动预热补到 target**

```go
// internal/control/workingset_test.go
package control

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeDeps struct {
	mu      sync.Mutex
	warmed  []string
	evicted []string
	bound   map[string]int
	warmSet map[string]bool
}
func newFakeDeps() *fakeDeps { return &fakeDeps{bound: map[string]int{}, warmSet: map[string]bool{}} }
func (d *fakeDeps) deps() Deps {
	return Deps{
		Warm: func(_ context.Context, jid string) (bool, error) {
			d.mu.Lock(); defer d.mu.Unlock()
			d.warmed = append(d.warmed, jid); d.warmSet[jid] = true; return true, nil
		},
		Evict: func(_ context.Context, jid string, _ time.Duration) {
			d.mu.Lock(); defer d.mu.Unlock()
			d.evicted = append(d.evicted, jid); d.warmSet[jid] = false
		},
		IsWarm:    func(jid string) bool { d.mu.Lock(); defer d.mu.Unlock(); return d.warmSet[jid] },
		BindProxy: func(_ context.Context, jid, _ string) error { d.mu.Lock(); defer d.mu.Unlock(); d.bound[jid]++; return nil },
	}
}

func TestWorkingSetActiveWarm(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	sched := NewScheduler(rdb)
	for _, j := range []string{"a", "b", "c"} { _ = sched.Enqueue(ctx, j, "US", 1000) }
	d := newFakeDeps()
	w := NewWorkingSet(rdb, sched, []string{"US"}, Config{Target: 2, WarmReqBatch: 16, KeepWarmHorizon: 90 * time.Second}, d.deps())

	if err := w.Tick(ctx, 5000); err != nil { t.Fatal(err) }
	if len(d.warmed) != 2 { t.Fatalf("warmed=%v want 2 (target)", d.warmed) }
}
```

Run: `go test ./internal/control/ -run TestWorkingSetActiveWarm -count=1`
Expected: FAIL（未定义）

- [ ] **Step 2: 实现 WorkingSet（workingset.go）**

```go
// internal/control/workingset.go
package control

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type Config struct {
	Target          int
	WarmReqBatch    int
	KeepWarmHorizon time.Duration
	Linger          time.Duration
}

type Deps struct {
	Warm      func(ctx context.Context, jid string) (bool, error)
	Evict     func(ctx context.Context, jid string, linger time.Duration)
	IsWarm    func(jid string) bool
	BindProxy func(ctx context.Context, jid, cc string) error
}

type WorkingSet struct {
	rdb   *goredis.Client
	sched *Scheduler
	ccs   []string
	cfg   Config
	deps  Deps
}

func NewWorkingSet(rdb *goredis.Client, sched *Scheduler, ccs []string, cfg Config, deps Deps) *WorkingSet {
	return &WorkingSet{rdb: rdb, sched: sched, ccs: ccs, cfg: cfg, deps: deps}
}

const residentKey = "ctl:resident"
const warmReqKey = "warm:req"

func (w *WorkingSet) residentCount(ctx context.Context) int {
	n, _ := w.rdb.SCard(ctx, residentKey).Result()
	return int(n)
}

func (w *WorkingSet) warm(ctx context.Context, jid, cc string) {
	_ = w.deps.BindProxy(ctx, jid, cc) // 粘性由 BindProxy 包装内部决定
	ok, err := w.deps.Warm(ctx, jid)
	if err == nil && ok {
		w.rdb.SAdd(ctx, residentKey, jid)
	}
}

func (w *WorkingSet) Tick(ctx context.Context, nowMs int64) error {
	// 1. 被动补热：取 warm:req 一批
	reqs, _ := w.rdb.LPopCount(ctx, warmReqKey, w.cfg.WarmReqBatch).Result()
	for _, jid := range reqs {
		w.warm(ctx, jid, ccOfReq(jid)) // 见 Step 3：补热项可携带 cc，简化用空 cc 由 BindProxy 查
	}
	// 2. 主动预热到 target
	for _, cc := range w.ccs {
		if w.residentCount(ctx) >= w.cfg.Target { break }
		due, err := w.sched.PopDue(ctx, cc, nowMs, w.cfg.Target)
		if err != nil { return err }
		for _, jid := range due {
			if w.residentCount(ctx) >= w.cfg.Target { break }
			w.warm(ctx, jid, cc)
		}
	}
	// 3. 远端到期驱逐
	members, _ := w.rdb.SMembers(ctx, residentKey).Result()
	for _, jid := range members {
		if !w.deps.IsWarm(jid) { w.rdb.SRem(ctx, residentKey, jid); continue }
		// 远端到期判定：该 jid 在任一 due 队列的 score - now > horizon（简化：缺省不驱逐，留 Run 中按需）
	}
	return nil
}

func ccOfReq(jid string) string { return "" } // 占位：被动补热的 cc 由 BindProxy 内部 GetBoundProxy 查得
```

> 注：被动补热的 cc 留空，`BindProxy` 包装内先 `GetBoundProxy`（账号已绑则知道 cc 且跳过）；新账号无绑时需 cc——故 `warm:req` 推送方（RoutingSender，Task 4）应推 `jid|cc` 复合串，`ccOfReq` 解析。Step 3 修正。

- [ ] **Step 3: 修正被动补热携带 cc**

把 `warmReqKey` 项约定为 `jid + "|" + cc`，改：

```go
func splitReq(v string) (jid, cc string) {
	for i := 0; i < len(v); i++ { if v[i] == '|' { return v[:i], v[i+1:] } }
	return v, ""
}
// Tick 内被动补热：
	for _, item := range reqs {
		jid, cc := splitReq(item)
		w.warm(ctx, jid, cc)
	}
```
删除 `ccOfReq` 占位。

- [ ] **Step 4: 测试转绿**

Run: `go test ./internal/control/ -run TestWorkingSetActiveWarm -count=1`
Expected: PASS

- [ ] **Step 5: 失败测试 — 被动补热 + 驱逐**

```go
func TestWorkingSetReactiveWarm(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	sched := NewScheduler(rdb)
	d := newFakeDeps()
	w := NewWorkingSet(rdb, sched, []string{"US"}, Config{Target: 10, WarmReqBatch: 16, KeepWarmHorizon: 90 * time.Second}, d.deps())
	rdb.RPush(ctx, "warm:req", "hot1|US")
	if err := w.Tick(ctx, 5000); err != nil { t.Fatal(err) }
	if len(d.warmed) != 1 || d.warmed[0] != "hot1" { t.Fatalf("reactive warmed=%v want [hot1]", d.warmed) }
}
```

Run: `go test ./internal/control/ -run TestWorkingSetReactiveWarm -count=1`
Expected: PASS（实现已支持；验证）

- [ ] **Step 6: 提交**

```bash
git add internal/control/workingset.go internal/control/workingset_test.go
git commit -m "feat(control): WorkingSet active+reactive warm with resident set"
```

---

## Task 3: Proxy Affinity（粘性绑定包装）

**Files:**
- Create: `internal/control/proxyaffinity.go`
- Test: `internal/control/proxyaffinity_test.go`

**Interfaces:**
- Produces: `func StickyBindProxy(getBound func(ctx, jid string) (bool, error), bind func(ctx, jid, cc string) error) func(ctx, jid, cc string) error`
  - `getBound` 返回 (已绑?, error)；已绑则跳过 bind（粘性）。

- [ ] **Step 1: 失败测试 — 已绑不再 bind；未绑才 bind**

```go
// internal/control/proxyaffinity_test.go
package control

import (
	"context"
	"testing"
)

func TestProxyAffinityStickiness(t *testing.T) {
	ctx := context.Background()
	bindCalls := 0
	bound := map[string]bool{"already": true}
	sticky := StickyBindProxy(
		func(_ context.Context, jid string) (bool, error) { return bound[jid], nil },
		func(_ context.Context, jid, cc string) error { bindCalls++; bound[jid] = true; return nil },
	)
	_ = sticky(ctx, "already", "US") // 已绑 → 不 bind
	_ = sticky(ctx, "fresh", "US")   // 未绑 → bind 一次
	_ = sticky(ctx, "fresh", "US")   // 现已绑 → 不再 bind
	if bindCalls != 1 { t.Fatalf("bindCalls=%d want 1 (粘性)", bindCalls) }
}
```

Run: `go test ./internal/control/ -run TestProxyAffinityStickiness -count=1`
Expected: FAIL（未定义）

- [ ] **Step 2: 实现（proxyaffinity.go）**

```go
// internal/control/proxyaffinity.go
package control

import "context"

// StickyBindProxy 返回一个粘性绑定函数：账号已绑代理则跳过，仅未绑时调 bind。
// getBound 应封装 store.GetBoundProxy（绑定存在=true，ErrProxyNotBound=false）。
func StickyBindProxy(
	getBound func(ctx context.Context, jid string) (bool, error),
	bind func(ctx context.Context, jid, cc string) error,
) func(ctx context.Context, jid, cc string) error {
	return func(ctx context.Context, jid, cc string) error {
		ok, err := getBound(ctx, jid)
		if err == nil && ok { return nil } // 已绑 → 粘性，跳过
		return bind(ctx, jid, cc)
	}
}
```

- [ ] **Step 3: 测试转绿 + 提交**

Run: `go test ./internal/control/ -run TestProxyAffinityStickiness -count=1`
Expected: PASS

```bash
git add internal/control/proxyaffinity.go internal/control/proxyaffinity_test.go
git commit -m "feat(control): sticky proxy binding wrapper"
```

---

## Task 4: RoutingSender 无会话时 LPUSH warm:req（cluster，加性）

**Files:**
- Modify: `internal/cluster/sender.go`
- Test: `internal/cluster/antifingerprint_test.go`（与反指纹计划同文件）

**Interfaces:**
- Consumes: `RoutingSender`（反指纹计划已加 policy 字段）
- Produces: `RoutingSender` 新增可空字段 `warmReq func(jid string)`；`func (rs *RoutingSender) WithWarmRequest(fn func(jid string)) *RoutingSender`

- [ ] **Step 1: 失败测试 — 无会话时触发 warmReq 回调**

```go
func TestRoutingSenderWarmRequestOnMiss(t *testing.T) {
	reg := NewRegistry()
	var requested string
	sender := NewRoutingSender(reg).WithWarmRequest(func(jid string) { requested = jid })
	_, err := sender.Send(context.Background(), "absent-jid", "1555", "hi", nil)
	if err == nil { t.Fatal("expected no-session error") }
	if requested != "absent-jid" { t.Fatalf("warmReq=%q want absent-jid", requested) }
}
```

Run: `go test ./internal/cluster/ -run TestRoutingSenderWarmRequestOnMiss -count=1`
Expected: FAIL（`WithWarmRequest` 未定义）

- [ ] **Step 2: 实现（sender.go）**

`RoutingSender` 加字段 `warmReq func(jid string)`；新增：

```go
func (rs *RoutingSender) WithWarmRequest(fn func(jid string)) *RoutingSender {
	rs.warmReq = fn
	return rs
}
```

在 `Send` 的 `if !ok {` 分支内、return 前加：

```go
		if rs.warmReq != nil {
			rs.warmReq(jid)
		}
```

- [ ] **Step 3: 测试转绿 + 现有测试不破**

Run: `go test ./internal/cluster/ -run 'WarmRequest|FenceOnSend|TypingSequence' -count=1`
Expected: PASS

- [ ] **Step 4: 提交**

```bash
git add internal/cluster/sender.go internal/cluster/antifingerprint_test.go
git commit -m "feat(cluster): RoutingSender warm-request hook on no-session miss"
```

---

## Task 5: 装配 — config + cmd/wadist（startup 换 SeedDue + WorkingSet.Run）

**Files:**
- Modify: `internal/config/config.go`
- Modify: `cmd/wadist/main.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `control.Scheduler`/`WorkingSet`/`StickyBindProxy`/`NextEligibleMs`、`RoutingSender.WithWarmRequest`、`node.Orchestrator.StartAccountWithLock`、`cluster.Registry.Get` + `Session.GracefulClose`、`store.GetBoundProxy`/`BindProxy`/`ListActiveAccounts`

- [ ] **Step 1: 失败测试 — 控制面默认配置**

```go
func TestLoad_ControlDefaults(t *testing.T) {
	t.Setenv("WADIST_REDIS_ADDR", "x:1")
	cfg, err := Load()
	if err != nil { t.Fatal(err) }
	if cfg.WarmTarget != 1500 { t.Fatalf("WarmTarget=%d want 1500", cfg.WarmTarget) }
	if cfg.WSTick != 500*time.Millisecond { t.Fatalf("WSTick=%v", cfg.WSTick) }
}
```

Run: `go test ./internal/config/ -run TestLoad_ControlDefaults -count=1`
Expected: FAIL

- [ ] **Step 2: config 加字段**（`internal/config/config.go`）

`Config` 加：

```go
	WarmTarget      int
	WSTick          time.Duration
	KeepWarmHorizon time.Duration
	WarmReqBatch    int
	DailyQuotaMin, DailyQuotaMax int
```

`Load()` 加（用既有 `intEnv`/`msEnv` 风格；`msEnv` 由反指纹计划引入，若先行则此处新增）：

```go
		WarmTarget:      intEnv("WADIST_WARM_TARGET", 1500),
		WSTick:          msEnv("WADIST_WS_TICK_MS", 500),
		KeepWarmHorizon: msEnv("WADIST_KEEP_WARM_HORIZON_MS", 90000),
		WarmReqBatch:    intEnv("WADIST_WARMREQ_BATCH", 64),
		DailyQuotaMin:   intEnv("WADIST_DAILY_QUOTA_MIN", 5),
		DailyQuotaMax:   intEnv("WADIST_DAILY_QUOTA_MAX", 10),
```

`intEnv` helper（若不存在）：

```go
func intEnv(key string, def int) int {
	if v := os.Getenv(key); v != "" { if n, err := strconv.Atoi(v); err == nil { return n } }
	return def
}
```

- [ ] **Step 3: cmd/wadist 接线**

A. 构造 sticky bindProxy + 回调（在 mgr/reg/orch 已建之后）：

```go
	sched := control.NewScheduler(rdb)
	sticky := control.StickyBindProxy(
		func(ctx context.Context, jid string) (bool, error) {
			_, err := mgr.GetBoundProxy(ctx, jid)
			if errors.Is(err, store.ErrProxyNotBound) { return false, nil }
			return err == nil, err
		},
		func(ctx context.Context, jid, cc string) error { _, e := mgr.BindProxy(ctx, jid, cc); return e },
	)
	deps := control.Deps{
		Warm: func(ctx context.Context, jid string) (bool, error) {
			sess, err := orch.StartAccountWithLock(ctx, jid)
			return sess != nil, err
		},
		Evict: func(ctx context.Context, jid string, linger time.Duration) {
			if s, ok := reg.Get(jid); ok { s.GracefulClose(ctx, linger) }
		},
		IsWarm:    func(jid string) bool { _, ok := reg.Get(jid); return ok },
		BindProxy: sticky,
	}
	ws := control.NewWorkingSet(rdb, sched, activeCountries, control.Config{
		Target: cfg.WarmTarget, WarmReqBatch: cfg.WarmReqBatch,
		KeepWarmHorizon: cfg.KeepWarmHorizon, Linger: cfg.Linger,
	}, deps)
```

> `activeCountries`：从 proxy_pool/account_devices distinct country 读取一次；最简先 `[]string{...}` 由 env `WADIST_COUNTRIES` 提供，缺省读 DB distinct。

B. RoutingSender 接 warm:req 钩子（推 `jid|cc`，cc 由 GetBoundProxy 查；查不到推 `jid|`）：

```go
	routing = routing.WithWarmRequest(func(jid string) {
		cc := ""
		if pb, err := mgr.GetBoundProxy(context.Background(), jid); err == nil { cc = pb.Country }
		rdb.RPush(context.Background(), "warm:req", jid+"|"+cc)
	})
```

> 注：`store.ProxyBinding.Country` 在 `GetBoundProxy` 返回值里目前可能为空（仅 ProxyID/ProxyURL）——若为空，实现时让 `GetBoundProxy` 也回带 country_code（store 加性读取，非红线逻辑改动），或 warm:req 推空 cc 由 BindProxy 内部 country 查询补。**实现者按实际字段择一**。

C. **替换全量 startup**：把原 `sup.Go(func{ return sup.StartAccounts(lctx, jids, orch.StartAccountWithLock) })` 改为播种 + WorkingSet 循环：

```go
	sup.Go(func(lctx context.Context) error {
		accts := make([]control.AccountCC, 0, len(jids))
		now := time.Now().UnixMilli()
		rng := rand.New(rand.NewSource(now))
		for _, jid := range jids {
			cc := countryOf(lctx, mgr, jid) // GetBoundProxy.Country 或 DB 查
			accts = append(accts, control.AccountCC{JID: jid, CC: cc,
				NextMs: control.NextEligibleMs(now, randQuota(rng, cfg), control.Window{StartHour: 9, EndHour: 22}, rng)})
		}
		return sched.SeedDue(lctx, accts)
	})
	sup.Go(func(lctx context.Context) error { return ws.Run(lctx, cfg.WSTick) })
```

`WorkingSet.Run`（workingset.go 追加）：

```go
func (w *WorkingSet) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done(): return ctx.Err()
		case <-t.C:
			if err := w.Tick(ctx, time.Now().UnixMilli()); err != nil { /* log, continue */ }
		}
	}
}
```

> 保留 `RunTakeoverScanner`/`RunHeartbeat` 不变（接管 stale）。`countryOf`/`randQuota` 为 cmd 内小 helper。

- [ ] **Step 4: 构建 + 配置测试**

Run: `go build ./... && go test ./internal/config/ -run TestLoad_ControlDefaults -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go internal/control/workingset.go cmd/wadist/main.go
git commit -m "feat: wire control plane (SeedDue + WorkingSet.Run) replacing full-startup warm"
```

---

## 验收（合并前）

- [ ] `make gate` 绿；`go test ./internal/control/ ./internal/cluster/ ./internal/config/ -count=1` 绿。
- [ ] `control` 包未 import node/cluster/store（`go list -deps` 核对）。
- [ ] PopDue 只弹到期、按序；NextEligibleMs ≥60s 含抖动。
- [ ] WorkingSet 主动预热到 target、被动消费 warm:req、远端到期驱逐走 GracefulClose。
- [ ] 代理粘性：已绑账号不再 BindProxy。
- [ ] startup 不再全量 StartAccounts；takeover 扫描仍在。
- [ ] 未改 dispatch/sendgate/billing 逻辑。

## Self-Review 备注

- `control` 用回调注入避免依赖环——`go list` 验证。
- 远端到期驱逐的 due-score 查询在 Tick 内简化；若需精确，可让 Recycler 维护 jid→nextDue 映射。生产按 canary requeue 率调 WarmReqBatch/Target。
- 依赖顺序：本计划 Task 4 复用反指纹计划的 `sender.go`；执行时反指纹计划应先于本计划 Task 4，或合并到同一 sender.go 改动批次。
- Risk Governor（AIMD）与短链未含——各自后续 spec+计划。
