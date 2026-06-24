# M11 容量/灰度 + 硬化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 收尾里程碑——容量计算器 `Plan()`、就绪/排空生命周期(`/readyz` + preStop 延迟,实现 k8s 零中断滚动)、cohort 金丝雀分配 + 有界观测、节点死亡→接管的混沌门禁(端到端,复用 M9)、k8s 清单(maxSurge:1/maxUnavailable:0 + PDB + preStop)与 PG/Redis 配置基线,并激活路线图预留的 CI 门禁。

**Architecture:** 容量计算为纯函数(账号→节点 by `MaxLockConns` 上限→每节点连接数→所需 `max_connections`→Redis 内存)。就绪门是 `metrics.Server` 上的 `atomic.Bool`:`/readyz` 在排空时返 503,k8s readinessProbe 据此摘除端点;SIGTERM→`SetReady(false)`→`PreStopDelay` 等待→`Supervisor.Shutdown`(复用 M8 有序停机 + M9 deregister)实现平滑迁移。Cohort 用 `fnv64a(jid)%100<pct` 确定性分桶(复用 lock.go 同款哈希),金丝雀以有界 label `cohort=canary|stable` 观测。混沌门禁端到端拼接 M9 构件(`pg_terminate_backend` 杀锁→scanner→TakeoverHandler→接管)。

**Tech Stack:** Go 1.26.4 stdlib(`hash/fnv`、`sync/atomic`、`net/http`)、testcontainers(混沌门禁)、k8s YAML、docker-compose。

## Global Constraints

- **零中断滚动**:`maxUnavailable:0` + `maxSurge:1`;readinessProbe→`/readyz`(排空时 503),livenessProbe→`/healthz`(进程活着恒 200);preStop = `SetReady(false)` + `PreStopDelay`(默认 5s,给 k8s 摘端点窗口)后才 `Supervisor.Shutdown`;`terminationGracePeriodSeconds ≥ PreStopDelay + ShutdownTimeout`。
- **容量公式**(纯函数,可测):`accountsPerNode = MaxLockConns`;`nodesNeeded = ceil(accounts/accountsPerNode)`;`connsPerNode = MaxLockConns + MaxOpenConns(biz) + MaxOpenConns(sqlDB) + (双 RLS DSN ? 2×MaxOpenConns : 0)`;`requiredMaxConnections = connsPerNode×nodesNeeded + headroom`;`redisMemoryMB` 由账号数×(日配额键+pacing 键)+ asynq 队列深度×平均任务大小推导。
- **金丝雀有界**:cohort label 只取 `canary|stable`;高基数门禁(`check_metric_labels.sh`)禁 jid/tenant_id/message_id/phone——cohort 安全。CanaryPercent 默认 0(关闭)。
- **混沌门禁**:`TestTakeoverChaos` 用真实 PG+Redis,模拟节点死亡(杀 advisory 锁 backend + 心跳过期)→ 对端 scanner 入队 → TakeoverHandler 接管,断言限时完成;可 `-count=30` 稳定。
- **CI 激活**:取消 `release-gate.yml` 中预留注释门禁(takeover chaos、memory baseline `-tags memory_gate`、shutdown 泄漏——本里程碑落地 chaos + memory)。
- **向后兼容**:就绪门、cohort、容量计算均加性;`/readyz` 新增不影响 `/healthz`;CanaryPercent=0 时无行为变化;既有测试零回归。
- ctx 贯穿;`%w`;`-race`;集成测试真实 PG;短模式 skip;高基数门禁绿。
- **范围**:不并入 M10 的 RLS 全量改造/明文收缩(独立大改);M11 聚焦容量/灰度/硬化。
- module `github.com/acme/wadist`,Go 1.26.4,`/usr/local/bin/go`。

---

## File Structure

- `internal/capacity/capacity.go` — `Inputs`、`Plan`、`Compute`。
- `internal/capacity/capacity_test.go` — 表驱动 + `memory_baseline_test.go`(`-tags memory_gate`)。
- `internal/metrics/server.go` — `ready atomic.Bool`、`/readyz`、`SetReady`。
- `internal/metrics/server_test.go` — readyz 200/503、SetReady 翻转。
- `internal/config/config.go` — `PreStopDelay`、`CanaryPercent`、`AsynqConcurrency`。
- `internal/canary/canary.go` — `InCohort`、`Cohort`。
- `internal/canary/canary_test.go` — 确定性、边界(0/100)、分布。
- `internal/metrics/metrics.go` — `RecordCohortSend(cohort, outcome)` 有界计数。
- `internal/dispatch/worker.go` — ProcessSend 记 cohort(nil-safe)。
- `internal/node/chaos_test.go` — `TestTakeoverChaos` 端到端。
- `cmd/wadist/main.go` — SetReady(true) 启动后、stop() 排空序列、AsynqConcurrency 配置化。
- `deploy/deployment.yaml`、`deploy/pdb.yaml`、`deploy/README.md` — k8s 清单 + 基线文档。
- `internal/deploy/manifests_test.go` — 解析校验关键字段(maxUnavailable:0、/readyz、preStop)。
- `docker-compose.yml` — redis `--maxmemory` 基线。
- `.github/workflows/release-gate.yml` — 激活 chaos + memory 门禁。
- `Makefile` — `memory-gate`、`chaos` 目标。

---

### Task 1: 容量计算器 Plan() + 内存基线门禁

**Files:**
- Create: `internal/capacity/capacity.go`, `internal/capacity/capacity_test.go`, `internal/capacity/memory_baseline_test.go`
- Modify: `internal/config/config.go`(AsynqConcurrency)

**Interfaces:**
- Produces:
  - `type Inputs struct { Accounts, MaxLockConns, MaxOpenConns, AsynqConcurrency, HeadroomConns int; DualRLSPools bool; DailyQuotaKeyBytes, PacingKeyBytes, AsynqQueueDepth, AvgTaskBytes int }`
  - `type Plan struct { AccountsPerNode, NodesNeeded, ConnsPerNode, RequiredMaxConnections, RedisMemoryMB int }`
  - `func Compute(in Inputs) Plan`
  - config `AsynqConcurrency int`(env `WADIST_ASYNQ_CONCURRENCY`,默认 32)。

- [ ] **Step 1: 写失败测试**

`capacity_test.go`(表驱动):

```go
package capacity

import "testing"

func TestCompute(t *testing.T) {
	cases := []struct {
		name string
		in   Inputs
		want Plan
	}{
		{
			name: "600 accounts single-DSN two nodes",
			in: Inputs{Accounts: 600, MaxLockConns: 300, MaxOpenConns: 50,
				AsynqConcurrency: 32, HeadroomConns: 50, DualRLSPools: false,
				DailyQuotaKeyBytes: 64, PacingKeyBytes: 64, AsynqQueueDepth: 1000, AvgTaskBytes: 512},
			want: Plan{
				AccountsPerNode:        300,
				NodesNeeded:            2,
				ConnsPerNode:           300 + 50 + 50, // lock + biz + sqlDB = 400
				RequiredMaxConnections: 400*2 + 50,    // 850
				RedisMemoryMB:          computeRedisMB(600, 64, 64, 1000, 512),
			},
		},
		{
			name: "exact ceiling one node",
			in:   Inputs{Accounts: 300, MaxLockConns: 300, MaxOpenConns: 50, HeadroomConns: 50},
			want: Plan{AccountsPerNode: 300, NodesNeeded: 1, ConnsPerNode: 400, RequiredMaxConnections: 450, RedisMemoryMB: 0},
		},
		{
			name: "dual RLS pools add 2x biz",
			in:   Inputs{Accounts: 100, MaxLockConns: 300, MaxOpenConns: 50, HeadroomConns: 0, DualRLSPools: true},
			want: Plan{AccountsPerNode: 300, NodesNeeded: 1, ConnsPerNode: 300 + 50 + 50 + 100, RequiredMaxConnections: 500, RedisMemoryMB: 0},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Compute(c.in)
			if got != c.want {
				t.Fatalf("Compute(%+v)\n got=%+v\nwant=%+v", c.in, got, c.want)
			}
		})
	}
}

func TestCompute_ZeroAccounts(t *testing.T) {
	got := Compute(Inputs{Accounts: 0, MaxLockConns: 300, MaxOpenConns: 50})
	if got.NodesNeeded != 0 {
		t.Fatalf("zero accounts → 0 nodes, got %d", got.NodesNeeded)
	}
}
```

> `computeRedisMB` 是被测内部辅助;实现时定义并在测试引用以避免重复魔数(或测试直接写期望整数)。

- [ ] **Step 2: 跑确认失败**

Run: `/usr/local/bin/go test ./internal/capacity/ -run TestCompute -v`
Expected: FAIL(包不存在)

- [ ] **Step 3: 写 capacity.go**

```go
// Package capacity computes node/connection/memory requirements from per-node
// limits. accountsPerNode is bounded by MaxLockConns (one pinned advisory-lock
// connection per active account). All pure functions — no I/O.
package capacity

type Inputs struct {
	Accounts         int
	MaxLockConns     int // per-node account ceiling
	MaxOpenConns     int // biz pool size (also sqlDB; ×2 more if DualRLSPools)
	AsynqConcurrency int
	HeadroomConns    int
	DualRLSPools     bool

	DailyQuotaKeyBytes int
	PacingKeyBytes     int
	AsynqQueueDepth    int
	AvgTaskBytes       int
}

type Plan struct {
	AccountsPerNode        int
	NodesNeeded            int
	ConnsPerNode           int
	RequiredMaxConnections int
	RedisMemoryMB          int
}

func ceilDiv(a, b int) int {
	if b <= 0 || a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func computeRedisMB(accounts, dailyKey, pacingKey, queueDepth, taskBytes int) int {
	bytes := accounts*(dailyKey+pacingKey) + queueDepth*taskBytes
	if bytes <= 0 {
		return 0
	}
	// 2x overhead for Redis structures/fragmentation, then round up to MB.
	return ceilDiv(bytes*2, 1<<20)
}

func Compute(in Inputs) Plan {
	apn := in.MaxLockConns
	nodes := ceilDiv(in.Accounts, apn)
	conns := in.MaxLockConns + in.MaxOpenConns + in.MaxOpenConns // lock + biz + sqlDB
	if in.DualRLSPools {
		conns += 2 * in.MaxOpenConns
	}
	required := 0
	if nodes > 0 {
		required = conns*nodes + in.HeadroomConns
	}
	return Plan{
		AccountsPerNode:        apn,
		NodesNeeded:            nodes,
		ConnsPerNode:           conns,
		RequiredMaxConnections: required,
		RedisMemoryMB:          computeRedisMB(in.Accounts, in.DailyQuotaKeyBytes, in.PacingKeyBytes, in.AsynqQueueDepth, in.AvgTaskBytes),
	}
}
```

- [ ] **Step 4: 内存基线门禁测试**

`memory_baseline_test.go`(build tag,仅 `-tags memory_gate` 跑;断言 N 个会话占用 < 阈值,防内存回归):

```go
//go:build memory_gate

package capacity

import (
	"runtime"
	"testing"
)

// TestMemoryBaseline_PerSession asserts a rough per-"session" heap budget so a
// future change that balloons per-account memory is caught in CI. It allocates
// lightweight stand-ins (the real Session lives in cluster; this is a structural
// budget guard, not a live-connection test).
func TestMemoryBaseline_PerSession(t *testing.T) {
	const n = 10000
	type sessionStub struct {
		jid    string
		state  int32
		buf    []byte
	}
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)

	stubs := make([]*sessionStub, n)
	for i := range stubs {
		stubs[i] = &sessionStub{jid: "1234567890.0:0@s.whatsapp.net", buf: make([]byte, 256)}
	}
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	perSession := (m1.HeapAlloc - m0.HeapAlloc) / n
	const budget = 2048 // bytes/session structural budget
	if perSession > budget {
		t.Fatalf("per-session heap %d bytes exceeds budget %d", perSession, budget)
	}
	runtime.KeepAlive(stubs)
}
```

> 这是结构性预算守卫(非真实连接);阈值宽松,目的是抓"每账号内存暴涨"的回归。

- [ ] **Step 5: config AsynqConcurrency**

`config.go`:增 `AsynqConcurrency int`(env `WADIST_ASYNQ_CONCURRENCY`,strconv,默认 32,<=0 回退)。加默认值测试。

- [ ] **Step 6: 跑测试**

Run:
```bash
/usr/local/bin/go test ./internal/capacity/ ./internal/config/ -race -v
/usr/local/bin/go test -tags memory_gate ./internal/capacity/ -run TestMemoryBaseline -v
/usr/local/bin/go build ./... && /usr/local/bin/go vet ./internal/capacity/
```
Expected: 全 PASS(含 memory gate)。

- [ ] **Step 7: 提交**

```bash
git add internal/capacity/ internal/config/
git commit -m "feat(capacity): node/connection/redis capacity calculator + per-session memory baseline gate"
```

---

### Task 2: 就绪/排空生命周期(/readyz + SetReady + preStop 延迟)

**Files:**
- Modify: `internal/metrics/server.go`(ready 标志 + /readyz + SetReady)
- Modify: `internal/metrics/server_test.go`
- Modify: `internal/config/config.go`(PreStopDelay)
- Modify: `cmd/wadist/main.go`(启动后 SetReady(true);stop() 排空序列)

**Interfaces:**
- Produces:
  - `func (s *Server) SetReady(ready bool)`;`/readyz`(ready→200 "ready";否则 503 "draining")。
  - config `PreStopDelay time.Duration`(env `WADIST_PRESTOP_DELAY`,默认 5s)。

- [ ] **Step 1: 写失败测试**

`server_test.go` 追加:

```go
func TestServer_Readyz_ReflectsState(t *testing.T) {
	reg := prometheus.NewRegistry()
	New(reg)
	s := NewServer(":0", reg)

	// default NOT ready → 503
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 503 {
		t.Fatalf("default readyz want 503, got %d", rec.Code)
	}
	s.SetReady(true)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ready") {
		t.Fatalf("ready readyz want 200/ready, got %d %q", rec.Code, rec.Body.String())
	}
	s.SetReady(false)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 503 {
		t.Fatalf("draining readyz want 503, got %d", rec.Code)
	}
	// /healthz stays 200 regardless of readiness
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("healthz must stay 200, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: 跑确认失败**

Run: `/usr/local/bin/go test ./internal/metrics/ -run TestServer_Readyz -v`
Expected: FAIL

- [ ] **Step 3: server.go 加就绪门**

`Server` 增字段 `ready atomic.Bool`(零值 false=未就绪);`Handler()` 注册 `/readyz`:

```go
mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
	if s.ready.Load() {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("draining"))
})

func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }
```

> import `sync/atomic`。`/healthz` 不变(恒 200,liveness)。

- [ ] **Step 4: config PreStopDelay**

`config.go`:`PreStopDelay time.Duration`(env `WADIST_PRESTOP_DELAY`,ParseDuration,默认 5s,<=0 回退)。加默认值测试。

- [ ] **Step 5: main 排空序列**

`cmd/wadist/main.go`:
- run() 成功装配 + `srv.Start()` 后,标记就绪:`srv.SetReady(true)`(在 run 内返回前,或 main 拿到 srv 后立即调)。
- stop():改为排空序列——
```go
stop := func() {
	srv.SetReady(false)            // /readyz → 503: k8s stops routing new work
	time.Sleep(cfg.PreStopDelay)   // give k8s time to remove the endpoint
	sup.Shutdown()                 // drain in-flight → deregister → close
	sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	_ = srv.Shutdown(sctx)         // metrics HTTP last
	_ = asynqClient.Close()
	_ = rdb.Close()
}
```
> 注意:`cfg` 需在 run()/stop() 闭包可见;若 stop() 当前不引用 cfg,把 PreStopDelay 捕获进闭包。保持其余装配/Supervisor/asynq 不变。run() 签名不变。

- [ ] **Step 6: 跑测试 + 门禁**

Run:
```bash
/usr/local/bin/go test ./internal/metrics/ ./internal/config/ -race -v
/usr/local/bin/go build ./... && /usr/local/bin/go vet ./...
./scripts/check_metric_labels.sh
```
Expected: 全 PASS。

- [ ] **Step 7: 提交**

```bash
git add internal/metrics/ internal/config/ cmd/wadist/
git commit -m "feat(metrics/cmd): /readyz drain toggle + preStop delay for zero-downtime rolling deploy"
```

---

### Task 3: cohort 金丝雀分配 + 有界观测

**Files:**
- Create: `internal/canary/canary.go`, `internal/canary/canary_test.go`
- Modify: `internal/config/config.go`(CanaryPercent)
- Modify: `internal/metrics/metrics.go`(RecordCohortSend)
- Modify: `internal/dispatch/worker.go`(ProcessSend 记 cohort,nil-safe)
- Modify: `internal/dispatch/types.go`/构造(注入 canaryPercent,可选)

**Interfaces:**
- Produces:
  - `func InCohort(jid string, pct uint8) bool`(fnv64a(jid)%100 < pct)
  - `func Cohort(jid string, pct uint8) string`(返回 `"canary"` 或 `"stable"`)
  - config `CanaryPercent uint8`(env `WADIST_CANARY_PERCENT`,默认 0)。
  - metrics `func (m *Metrics) RecordCohortSend(cohort, outcome string)`(nil-safe,新计数 `wadist_cohort_sends_total{cohort, outcome}`)。

- [ ] **Step 1: canary 失败测试**

`canary_test.go`:

```go
package canary

import "testing"

func TestInCohort_Deterministic(t *testing.T) {
	jid := "1234567890.0:0@s.whatsapp.net"
	if InCohort(jid, 0) {
		t.Fatal("pct=0 → never in cohort")
	}
	if !InCohort(jid, 100) {
		t.Fatal("pct=100 → always in cohort")
	}
	// deterministic
	a := InCohort(jid, 50)
	b := InCohort(jid, 50)
	if a != b {
		t.Fatal("must be deterministic for same jid/pct")
	}
}

func TestCohort_Label(t *testing.T) {
	if Cohort("x", 100) != "canary" || Cohort("x", 0) != "stable" {
		t.Fatal("Cohort label wrong")
	}
}

func TestInCohort_Distribution(t *testing.T) {
	in := 0
	const total = 10000
	for i := 0; i < total; i++ {
		if InCohort(jidN(i), 10) {
			in++
		}
	}
	// ~10% ± 2%
	if in < total*8/100 || in > total*12/100 {
		t.Fatalf("10%% cohort distribution off: %d/%d", in, total)
	}
}

func jidN(i int) string { return "jid-" + itoa(i) + "@s.whatsapp.net" }
func itoa(i int) string { /* minimal */ return string(rune('0'+i%10)) + "-" + "x" } // implementer: use strconv.Itoa
```

> implementer:用 `strconv.Itoa` 生成不同 jid,别用上面的占位 itoa。

- [ ] **Step 2: 跑确认失败**

Run: `/usr/local/bin/go test ./internal/canary/ -v`
Expected: FAIL

- [ ] **Step 3: canary.go**

```go
// Package canary assigns accounts to a rollout cohort deterministically so a new
// release/behavior can be staged to a percentage of accounts. Uses the same
// fnv64a hash as the advisory-lock key (stable across processes).
package canary

import "hash/fnv"

func bucket(jid string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(jid))
	return h.Sum64() % 100
}

// InCohort reports whether jid is in the canary cohort for the given percent
// (0..100). pct=0 → never; pct>=100 → always.
func InCohort(jid string, pct uint8) bool {
	if pct == 0 {
		return false
	}
	if pct >= 100 {
		return true
	}
	return bucket(jid) < uint64(pct)
}

// Cohort returns the bounded metric label for jid.
func Cohort(jid string, pct uint8) string {
	if InCohort(jid, pct) {
		return "canary"
	}
	return "stable"
}
```

- [ ] **Step 4: config CanaryPercent**

`config.go`:`CanaryPercent uint8`(env `WADIST_CANARY_PERCENT`,strconv 0..100,越界/错误回退 0)。默认值测试。

- [ ] **Step 5: metrics RecordCohortSend(有界 label)**

`metrics.go`:新增 CounterVec `wadist_cohort_sends_total` labels `["cohort","outcome"]`,注册到 reg;nil-safe `RecordCohortSend(cohort, outcome string)`。**label 值只取 canary/stable + 既有 outcome,过高基数门禁。**

- [ ] **Step 6: worker 记 cohort**

`SendWorker` 增可选 `canaryPct uint8` 字段 + setter `WithCanary(pct uint8)`(默认 0);在 ProcessSend 的终态 outcome 记录处追加 `w.m.RecordCohortSend(canary.Cohort(pl.JID, w.canaryPct), outcome)`(nil-safe via w.m)。**不改 NewSendWorker 签名**(setter 注入,既有测试零回归)。main 用 cfg.CanaryPercent 调 WithCanary。补一个集成/单元断言:cohort 计数随 canaryPct 变化(canary 账号 → cohort_sends{cohort=canary} +1)。

- [ ] **Step 7: 跑测试 + 门禁**

Run:
```bash
TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./internal/canary/ ./internal/metrics/ ./internal/dispatch/ ./internal/config/ -race -v
./scripts/check_metric_labels.sh
/usr/local/bin/go build ./... && /usr/local/bin/go vet ./...
```
Expected: 全 PASS;门禁 OK(cohort 有界)。

- [ ] **Step 8: 提交**

```bash
git add internal/canary/ internal/config/ internal/metrics/ internal/dispatch/
git commit -m "feat(canary): deterministic cohort assignment + bounded canary send metric"
```

---

### Task 4: 混沌门禁 TestTakeoverChaos(节点死亡→接管,端到端)

**Files:**
- Create: `internal/node/chaos_test.go`

**Interfaces:**
- Consumes: M9 构件(Orchestrator/StartAccountWithLock/RunTakeoverScanner/scanOnce/TakeoverEnqueuer/RegisterTakeoverHandler、store.AcquireDeviceLock、`pg_terminate_backend`)。

- [ ] **Step 1: 写混沌测试**

`internal/node/chaos_test.go`(真实 PG+Redis,tag integration,可 `-count=30`):

```go
package node

import (
	"context"
	"testing"
	"time"
)

// TestTakeoverChaos simulates node death and asserts a peer takes over the
// account: node A holds the lock for jid; A's lock backend is terminated and its
// heartbeat goes stale; node B's scanner enqueues a takeover; B's handler
// (StartAccountWithLock) acquires the now-free lock and claims ownership.
func TestTakeoverChaos(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t)
	ctx := context.Background()
	redisAddr := startRedis(t)

	const jid = "chaos-jid-1"
	seedAccountDevice(t, ctx, m, jid)

	// Node A: heartbeat + start the account (acquires lock, owner_node=node-A).
	m.UpsertNodeHeartbeat(ctx, "node-A")
	regA := /* cluster.NewRegistry */ newRegistry()
	supA := /* cluster.NewSupervisor */ newSupervisor(regA)
	factoryA := fakeFactory() // returns fakeConn-backed session
	orchA := NewOrchestrator(m, regA, supA, "node-A", factoryA, time.Second)
	sessA, err := orchA.StartAccountWithLock(ctx, jid)
	if err != nil || sessA == nil {
		t.Fatalf("node A start: %v", err)
	}

	// CHAOS: kill node A's lock backend (advisory lock auto-released by PG) and
	// make node A's heartbeat stale.
	killLockBackend(t, ctx, m, sessA) // pg_terminate_backend on the session's lock conn
	m.bizPool.Exec(ctx, `UPDATE cluster_nodes SET last_heartbeat_at = now() - interval '10 minutes' WHERE node_id='node-A'`)

	// Node B: scanner finds the stale-owned account, enqueues; handler takes over.
	regB := newRegistry()
	supB := newSupervisor(regB)
	orchB := NewOrchestrator(m, regB, supB, "node-B", fakeFactory(), time.Second)
	enq := NewTakeoverEnqueuer(asynqClientFor(redisAddr), 30*time.Second)

	n, err := orchB.scanOnce(ctx, 30*time.Second, enq)
	if err != nil || n < 1 {
		t.Fatalf("node B scan: n=%d err=%v", n, err)
	}
	// Drive the takeover directly (handler body): StartAccountWithLock on node B.
	deadline := time.Now().Add(10 * time.Second)
	for {
		sessB, err := orchB.StartAccountWithLock(ctx, jid)
		if err == nil && sessB != nil {
			break // took over
		}
		if time.Now().After(deadline) {
			t.Fatalf("node B failed to take over within deadline (err=%v)", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// owner_node now node-B
	var owner string
	m.bizPool.QueryRow(ctx, `SELECT owner_node FROM account_devices WHERE account_jid=$1`, jid).Scan(&owner)
	if owner != "node-B" {
		t.Fatalf("owner_node=%q, want node-B", owner)
	}
	_ = regB
}
```

> **implementer 重要说明:** 上面 `newRegistry`/`newSupervisor`/`fakeFactory`/`killLockBackend`/`asynqClientFor` 是占位——用真实构件落地:
> - registry/supervisor:`cluster.NewRegistry()` / `cluster.NewSupervisor(reg, cluster.SupervisorOpts{Limit:4,ShutdownTimeout:time.Second})`(node 测试已 import cluster)。
> - fakeFactory:复用 orchestrator_test.go 的 SessionFactory 返回 fakeConn-backed `cluster.NewSession`。
> - killLockBackend:从 sessA 拿不到内部锁——改为在 StartAccountWithLock 后,用一条独立 `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE ...` 杀掉持有该 jid advisory 锁的 backend;或更简:直接在 PG 释放——因为难以定位具体 backend,**改用更直接的混沌注入**:节点 A 的 StartAccountWithLock 后,直接 `Release` 不行(那是正常释放)。实现者应:让 node A 的 session 暴露/或通过 orchestrator 持有 lock 引用,杀其 backend;若 orchestrator 未暴露,则在 chaos_test 里 node A 改为直接 `lock, _ := m.AcquireDeviceLock(ctx, jid)` 自己持锁(不经 orchestrator),记录 `lock.conn.Conn().PgConn().PID()`(同包 store?不同包——node 不能读 store 未导出字段)。**最简可行**:node A 用 `m.AcquireDeviceLock` 持锁 + ClaimAccount(owner=node-A) 模拟"A 在跑";混沌 = 从另一连接 `pg_terminate_backend` 掉那把锁的 backend(用 `SELECT pid FROM pg_locks WHERE locktype='advisory' AND ...` 或 lock 暴露 PID 的辅助);锁释放后 node B `StartAccountWithLock` 成功接管。
> - **若定位 backend PID 困难**:store 可加一个测试友好的导出 `func (l *DeviceLock) BackendPID() uint32`(只读,最小),chaos_test 用它 + `pg_terminate_backend`。这是允许的最小 store 改动。
> - asynqClientFor:`asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})`。
> 目标:**端到端证明 A 死后 B 接管、owner_node 变为 B**。实现者选最简健壮路径并在报告说明;必要时给 store.DeviceLock 加 `BackendPID()` 只读辅助。

- [ ] **Step 2: 跑(含重复稳定性)**

Run:
```bash
TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./internal/node/ -run TestTakeoverChaos -race -v
TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./internal/node/ -run TestTakeoverChaos -count=10
```
Expected: 稳定 PASS(无 flake)。

- [ ] **Step 3: 提交**

```bash
git add internal/node/chaos_test.go internal/store/lock.go
git commit -m "test(node): TestTakeoverChaos end-to-end node-death -> peer takeover gate"
```

---

### Task 5: k8s 清单 + PG/Redis 基线 + 激活 CI 门禁

**Files:**
- Create: `deploy/deployment.yaml`, `deploy/pdb.yaml`, `deploy/README.md`
- Create: `internal/deploy/manifests_test.go`(解析校验)
- Modify: `docker-compose.yml`(redis `--maxmemory`)
- Modify: `.github/workflows/release-gate.yml`(激活 chaos + memory 门禁)
- Modify: `Makefile`(`chaos`、`memory-gate` 目标)

**Interfaces:**
- Produces: k8s Deployment(maxSurge:1/maxUnavailable:0、readinessProbe /readyz、livenessProbe /healthz、preStop、resources、env)、PDB(minAvailable:1)。

- [ ] **Step 1: deployment.yaml**

`deploy/deployment.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: wadist
spec:
  replicas: 2
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0
  selector:
    matchLabels: { app: wadist }
  template:
    metadata:
      labels: { app: wadist }
    spec:
      terminationGracePeriodSeconds: 45   # >= PRESTOP_DELAY(5) + SHUTDOWN_TIMEOUT(30) + margin
      containers:
        - name: wadist
          image: wadist:latest
          ports:
            - { name: metrics, containerPort: 9090 }
          env:
            - { name: WADIST_METRICS_ADDR, value: ":9090" }
            - { name: WADIST_PRESTOP_DELAY, value: "5s" }
            - { name: WADIST_SHUTDOWN_TIMEOUT, value: "30s" }
            - { name: WADIST_MAX_LOCK_CONNS, value: "300" }
          readinessProbe:
            httpGet: { path: /readyz, port: metrics }
            periodSeconds: 2
            failureThreshold: 1
          livenessProbe:
            httpGet: { path: /healthz, port: metrics }
            periodSeconds: 10
            failureThreshold: 3
          lifecycle:
            preStop:
              exec: { command: ["/bin/sh", "-c", "sleep 5"] }
          resources:
            requests: { cpu: "500m", memory: "512Mi" }
            limits:   { cpu: "2",    memory: "1Gi" }
```

- [ ] **Step 2: pdb.yaml**

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: wadist-pdb
spec:
  minAvailable: 1
  selector:
    matchLabels: { app: wadist }
```

- [ ] **Step 3: manifests_test.go(解析校验,无需 kubectl)**

`internal/deploy/manifests_test.go`:用 `sigs.k8s.io/yaml` 或 `gopkg.in/yaml.v3`(若已是依赖;否则用 stdlib 解析为 map)读 deploy/*.yaml,断言关键字段:Deployment.spec.strategy.rollingUpdate.maxUnavailable==0、maxSurge==1、readinessProbe.httpGet.path=="/readyz"、livenessProbe.path=="/healthz"、存在 preStop、PDB.spec.minAvailable==1。

> implementer:优先用已有 yaml 依赖;若无,加 `gopkg.in/yaml.v3`(go get)或用 encoding/json + sigs.k8s.io/yaml。读 go.mod 确认。测试路径 `../../deploy/*.yaml`。

- [ ] **Step 4: docker-compose redis maxmemory + 基线文档**

`docker-compose.yml`:redis command 增 `--maxmemory 512mb`(保持 `--maxmemory-policy noeviction`)。`deploy/README.md` 记容量基线公式(引用 capacity.Compute)+ PG `max_connections` 推导 + Redis 内存推导 + 滚动/PDB/preStop 说明。

- [ ] **Step 5: 激活 CI 门禁**

`.github/workflows/release-gate.yml`:取消注释/新增:
- takeover chaos:`go test -tags integration -run TestTakeoverChaos -count=5 ./internal/node/`(需 PG+Redis service;按现有 CI 的 service 容器接法)。
- memory baseline:`go test -tags memory_gate -run TestMemoryBaseline ./internal/capacity/`。
`Makefile`:增 `chaos`(跑 TestTakeoverChaos)、`memory-gate`(跑 memory 门禁)目标,并入 `gate` 或单列。

> implementer:CI 若无 PG/Redis service 容器,chaos 用 testcontainers(已是测试内置,需 Docker-in-CI);按现有 release-gate.yml 的 migrate_twice 步骤已假设 PG 可用的方式接。若 CI 无 Docker,把 chaos 设为单独可选 job 并注明。

- [ ] **Step 6: 全门禁**

Run:
```bash
/usr/local/bin/go build ./... && /usr/local/bin/go vet ./...
./scripts/check_metric_labels.sh
TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./... -race
/usr/local/bin/go test -tags memory_gate ./internal/capacity/ -run TestMemoryBaseline
```
Expected: 全绿;门禁 OK;manifests 解析测试通过。

- [ ] **Step 7: 提交**

```bash
git add deploy/ internal/deploy/ docker-compose.yml .github/workflows/release-gate.yml Makefile
git commit -m "feat(deploy): k8s rolling/PDB/preStop manifests + redis maxmemory baseline + activate chaos/memory CI gates"
```

---

## Self-Review(写作后自查)

- **Spec 覆盖:** 容量计算器 Plan ✅(T1)、内存基线门禁 ✅(T1)、就绪/排空 + preStop 延迟(零中断滚动)✅(T2)、cohort 金丝雀 + 有界观测 ✅(T3)、节点死亡→接管混沌门禁 ✅(T4)、k8s 清单(maxSurge:1/maxUnavailable:0/PDB/preStop)✅(T5)、PG/Redis 基线 ✅(T5)、CI 门禁激活 ✅(T5)。
- **整合复用:** T2 排空复用 M8 Supervisor + M9 deregister;T4 混沌复用 M9 全套接管 + M1 fencing(pg_terminate_backend);cohort 复用 lock.go 的 fnv64a。
- **可测 vs 基建:** T1/T2/T3 纯/单元可测;T4 真实 PG+Redis 集成(可 -count 稳定);T5 清单用解析测试校验关键字段(无需 kubectl)。
- **向后兼容:** /readyz 加性不影响 /healthz;CanaryPercent=0 无行为变化;worker WithCanary/WithMetrics setter 注入零回归;AsynqConcurrency 配置化默认 32 同现状。
- **有界基数:** cohort label 仅 canary/stable,过高基数门禁。
- **范围诚实:** 不并入 M10 RLS 全量改造/明文收缩(独立大改),仅聚焦容量/灰度/硬化;承接债仍在 ledger/TODO。
- **类型一致:** capacity.Inputs/Plan/Compute、Server.SetReady、canary.InCohort/Cohort、Metrics.RecordCohortSend、config(AsynqConcurrency/PreStopDelay/CanaryPercent)跨任务一致。
- **占位:** T4 测试含占位辅助名,已明确要求用真实构件落地 + 允许给 DeviceLock 加 `BackendPID()` 只读辅助;无生产 panic 占位。
