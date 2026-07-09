# 单一高密度配置 — 删除 legacy 双栈 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 删除 wadist 全部 legacy 发送/所有权/会话/代理/池化路径及其门控 flag，使高密度栈（pump + redis 所有权 + BadgerDB + redis 代理冷却 + 常驻反封控制环）成为唯一且默认生效的路径。

**Architecture:** 逐层坍缩——每个任务删掉一条 legacy 分支或一组门控 flag，保持编译与 `make gate` 全程绿；每层的高密度路径本已有测试作安全网，删除 legacy 分支后由这些测试 + 新增的"单一路径不变式"断言守护。asynq 库因 takeover 队列保留，仅删其发送路径。

**Tech Stack:** Go、pgx/pgxpool、go-redis、BadgerDB(wabadger)、whatsmeow、asynq(仅 takeover)、testcontainers。

## Global Constraints

以下值逐字来自 spec，每个任务隐含遵守：

- **正确性不变式必须存活**：`balance = Σledger`、`frozen = Σ未结charge`、`message_id` 幂等、`sent_today` 加减对称、单账号单会话（fence token）。
- **`make gate` 全程绿**：`gate: tidy vet test-race labels vuln`（Makefile L42-43）。`test-race` = `TESTCONTAINERS_RYUK_DISABLED=true go test -race ./...`（需 Docker）。
- **Redis 从可选变必须**：无 redis 启动 fail-fast；Redis 须开 AOF。
- **Badger 目录须挂 NVMe 持久卷**进 wadist 容器。
- **保留**：`proxy_pool` 表、`chk_bindings` 约束、`releaseWithinTx` 的 PG durable 写、`GetBoundProxy`/`proxy_url_cache`、业务 `sqlDB`/各 pgxpool、`internal/node/takeover.go`（asynq takeover 队列）、wabadger difftest（对拍 sqlstore，test-only）。
- **一次性删完，单分支**（非分模块灰度）。
- 会话迁移 = **接受账号全量重扫码**（不写迁移工具）。

**执行前置**：从 `main` 新建分支 `feat/single-config-collapse`（当前在 `feat/mvp-business-loop`，先确认基线 `make gate` 绿再开工）。

---

## Task 1: 预备性符号迁移（零行为变更，为后续删除解耦）

删除 `asynqadapter.go`/`lock.go` 前，先把它们里被高密度路径依赖的共享符号迁走；并解除 `SendWorkers` 默认值对 `AsynqConcurrency` 的依赖。纯移动，不改行为。

**Files:**
- Modify: `internal/dispatch/types.go`（接收 `SendBodyResolver`）
- Modify: `internal/dispatch/asynqadapter.go:39-40`（移出 `SendBodyResolver`）
- Modify: `internal/store/ownership.go`（接收 `ErrDeviceLocked`）
- Modify: `internal/store/lock.go:13`（移出 `ErrDeviceLocked`）
- Modify: `internal/config/config.go:202`（`SendWorkers` 独立默认）

**Interfaces:**
- Produces: `dispatch.SendBodyResolver`（现位于 types.go）、`store.ErrDeviceLocked`（现位于 ownership.go）——签名与语义不变，仅换文件。

- [ ] **Step 1: 把 `SendBodyResolver` 类型定义从 asynqadapter.go 迁到 types.go**

在 `internal/dispatch/types.go` 末尾追加（与 asynqadapter.go:39-40 逐字一致）：
```go
// SendBodyResolver resolves a campaign's message body/media at send time.
// Consumed by both the pump send loop and (legacy) the asynq handler.
type SendBodyResolver func(ctx context.Context, campaignID int64) (body, mediaSha, mime string, raw []byte, err error)
```
从 `internal/dispatch/asynqadapter.go` 删除该类型定义（L39-40）。

- [ ] **Step 2: 把 `ErrDeviceLocked` 从 lock.go 迁到 ownership.go**

在 `internal/store/ownership.go` 顶部（接口定义前）追加，并从 `internal/store/lock.go:13` 删除：
```go
// ErrDeviceLocked is returned when an account's device lock is already held
// by another owner. Returned by the redis ownership backend's Acquire.
var ErrDeviceLocked = errors.New("store: device already locked by another owner")
```
确认 `ownership.go` 已 import `"errors"`（无则加）。

- [ ] **Step 3: 解除 SendWorkers 对 AsynqConcurrency 的默认依赖**

`internal/config/config.go:202`，改：
```go
cfg.SendWorkers = intEnv("WADIST_SEND_WORKERS", cfg.AsynqConcurrency)
```
为：
```go
cfg.SendWorkers = intEnv("WADIST_SEND_WORKERS", 32)
```

- [ ] **Step 4: 验证编译与全量测试仍绿**

Run: `go build ./... && TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/dispatch/... ./internal/store/... ./internal/config/...`
Expected: PASS（纯移动，无行为变化）

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/types.go internal/dispatch/asynqadapter.go internal/store/ownership.go internal/store/lock.go internal/config/config.go
git commit -m "refactor: 迁出 SendBodyResolver/ErrDeviceLocked, 解耦 SendWorkers 默认(删前置)"
```

---

## Task 2: 发送层坍缩为 pump-only

删 asynq 发送路径，pump 成为唯一发送驱动。asynq 库、`asynqSrv`/`mux`/`asynqClient` 因 takeover 保留。

**Files:**
- Delete: `internal/dispatch/asynqadapter.go`（`SendBodyResolver` 已在 Task 1 迁走）
- Modify: `cmd/wadist/main.go`（入队器选择 L154-169、发送驱动选择 L214-219、asynq server Concurrency L210）
- Modify: `internal/config/config.go`（删 `DispatchMode` L68-70/L200、`AsynqConcurrency` L37-38/L163-166）
- Modify: `internal/api/admin_api.go:49`（`QueueBacklog` 注释语义）
- Modify: `internal/dispatch/worker.go`（更新 asynq 措辞注释 L15-23）

**Interfaces:**
- Consumes: `dispatch.NewPumpEnqueuer(buffer int) *PumpEnqueuer`、`dispatch.NewPump(pe *PumpEnqueuer, w *SendWorker, resolve SendBodyResolver, ratePerSec float64, workers int) *Pump`（均已存在）。
- Produces: main.go 中 `pe`/`pump` 无条件构造。

- [ ] **Step 1: 更新 config 测试断言（先失败）**

`internal/config/config_test.go` 的 `TestLoad_DefaultsAndStoreMapping`（L76 附近）：删除对 `DispatchMode == "asynq"`、`AsynqConcurrency == 32` 的断言。新增断言 `cfg.SendWorkers == 32`（独立默认）、`cfg.SendRate == 160.0`、`cfg.PumpBuffer == 512`。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/config/ -run TestLoad_DefaultsAndStoreMapping`
Expected: FAIL（旧字段仍存在/被断言）——或编译错误若已删断言引用的字段。可接受，进入实现。

- [ ] **Step 3: 删除 config 的 DispatchMode / AsynqConcurrency**

`internal/config/config.go`：
- 删字段 `AsynqConcurrency int`（L37-38 含注释）、`DispatchMode string`（L68-70 含注释）。
- 删加载 L163-166（`cfg.AsynqConcurrency = 32` 及其 env 覆盖块）、L200（`cfg.DispatchMode = getenv(...)`）。

- [ ] **Step 4: main.go 入队器与发送驱动改为无条件 pump**

`cmd/wadist/main.go` L154-169 改为（删 else asynq 分支）：
```go
var dispatcher *dispatch.Dispatcher
dispatch.ReclaimOrphanedAssignments(reclaimCtx, pool) // 崩溃恢复：重投孤儿分配
pe := dispatch.NewPumpEnqueuer(cfg.PumpBuffer)
dispatcher = dispatch.NewDispatcher(pool, billingRepo, pe, priceFor, 3*time.Second)
```
（保留原 ReclaimOrphanedAssignments 的实参/ctx 变量名，与原 L159 一致。）

L214-219 改为（删 else RegisterSendHandler 分支）：
```go
pump := dispatch.NewPump(pe, worker, sendResolver, cfg.SendRate, cfg.SendWorkers)
```

L454 起 `if cfg.DispatchMode == "pump" {` 的整个条件去掉，块内内容（`sup.Go(pump.Run)` 等 L457/471/481/524/497-502）无条件执行。`pe.Close()`（L524）保留在 drain 之后。

- [ ] **Step 5: takeover asynq server Concurrency 改常量**

`cmd/wadist/main.go` asynq server 构造处（约 L208-212），原 `Concurrency: cfg.AsynqConcurrency` 改为命名常量：
```go
const takeoverConcurrency = 8 // takeover 队列低吞吐，固定并发
// ...
asynqSrv := asynq.NewServer(redisOpt, asynq.Config{Concurrency: takeoverConcurrency, /* ...原有其他字段... */})
```
保留 `asynqClient`（L148）、`asynqSrv`（L208）、`mux`（L212）、`StopIntake: asynqSrv.Shutdown()`（L265）、`asynqSrv.Run(mux)`（L573）、`asynqClient.Close()`（L579/L600）——takeover 依赖。

- [ ] **Step 6: 删除 asynqadapter.go 与残留引用**

删除文件 `internal/dispatch/asynqadapter.go`。删除 main.go 中对 `dispatch.NewAsynqEnqueuer`（原 L167）与 `dispatch.RegisterSendHandler`（原 L218）的调用（已在 Step 4 随分支删除，确认无残留）。

- [ ] **Step 7: 更新注释语义**

`internal/api/admin_api.go:49` `QueueBacklog int` 注释由 "(asynq backlog proxy)" 改为 "(pump in-memory buffer depth)"（字段来源后续可接 `pe.Len()`，本任务仅改注释）。`internal/dispatch/worker.go:15-23` 注释中 "asynq retries/at-least-once" 改为 "pump reclaim/at-least-once"（逻辑不变）。

- [ ] **Step 8: 验证 + gate**

Run: `go build ./... && TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/dispatch/... ./internal/config/... ./cmd/...`
Expected: PASS（asynq 无专属测试，pump 测试不受影响）

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "refactor(dispatch): 坍缩为 pump-only, 删 asynq 发送路径(takeover 保留)"
```

---

## Task 3: 所有权层坍缩为 redis-only

删 pg + shadow 所有权后端、advisory lock 池及其 PG 方法。

**Files:**
- Delete: `internal/store/ownership_pg.go`、`internal/store/ownership_shadow.go`、`internal/store/lock.go`（`ErrDeviceLocked` 已迁走）
- Modify: `internal/store/ownership.go`（`newOwnership` 去 pg/shadow 分支、`backendName`）
- Modify: `internal/store/manager.go`（删 `lockPool` 字段 L48 + 创建 L154-177 + 关闭 L184-229/280-281；ownership 无条件 redis）
- Modify: `internal/store/node.go`（删 `deregisterNodePG`/`staleOwnedAccountsPG`/`listUnownedActiveAccountsPG`/`upsertNodeHeartbeatPG`）
- Modify: `internal/store/config.go`（删 `OwnershipBackend` 默认→redis 或去字段、`MaxLockConns`、`OnShadowDivergence`）
- Modify: `internal/config/config.go`（删 `OwnershipBackend` L45-46/L161、`MaxLockConns` L21/L126、Store() 投影 L252/L256）
- Modify: `cmd/wadist/main.go`（删 `OnShadowDivergence` 注入 L93；redis fail-fast，见 Task 9 也可）
- Modify: `internal/metrics/metrics.go`（删孤立的 `ObserveOwnershipDivergence` L187-192 + collector）
- Delete/port tests: `internal/store/lock_test.go`、`internal/store/ownership_select_test.go`、`internal/store/ownership_shadow_test.go`、`internal/node/chaos_test.go`(pg 版)

**Interfaces:**
- Consumes: `newRedisOwnershipWithRoster(rdb, roster, ttl)`、`redisOwnership` 全部方法（已存在）。
- Produces: `newOwnership(m) Ownership` 恒返回 redis 后端。

- [ ] **Step 1: 收紧后端选择的测试（先失败）**

`internal/store/ownership_select_test.go`：删除 pg / shadow 断言分支，改为单断言——无论 config 如何，`newOwnership(m)` 返回的 `backendName` 恒为 `"redis"`。（若该文件仅测多后端选择，可整体替换为此单测。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/store/ -run TestNewOwnershipSelect`
Expected: FAIL（当前默认返回 pg）

- [ ] **Step 3: newOwnership 收敛为 redis-only**

`internal/store/ownership.go` `newOwnership`（L30-40）改为：
```go
func newOwnership(m *Manager) Ownership {
	return newRedisOwnershipWithRoster(m.cfg.Redis, m.ListActiveAccounts, m.cfg.OwnershipTTL)
}
```
`backendName`（L44-55）删除 `*pgOwnership`/`*shadowOwnership` 分支，仅留 `*redisOwnership → "redis"`。

- [ ] **Step 4: 删除 pg/shadow 后端文件与 lock 池**

- 删 `internal/store/ownership_pg.go`、`internal/store/ownership_shadow.go`、`internal/store/lock.go`。
- `internal/store/manager.go`：删 `lockPool *pgxpool.Pool` 字段（L48）；删 lockPool 创建块（L154-177）及所有 error-path/close 引用（L184-185/196-197/213-214/228-229/243/280-281）。`newOwnership(m)` 调用（L249）保留。
- `internal/store/node.go`：删 `deregisterNodePG`(L31-46)、`staleOwnedAccountsPG`(L107-114 段)、`listUnownedActiveAccountsPG`(L130-135 段)、`upsertNodeHeartbeatPG`(L19-29)。公共委托方法 `DeregisterNode`/`StaleOwnedAccounts`/`ListUnownedActiveAccounts`/`UpsertNodeHeartbeat`（L195-218）保留（转发到 `m.ownership.*`，现走 redis）。

- [ ] **Step 5: 删除 shadow 配置与 divergence metric**

- `internal/store/config.go`：删 `OnShadowDivergence`（L40-42）；`OwnershipBackend`（L29-30）默认改 redis（withDefaults L76-78 由 `"pg"`→`"redis"`）或直接删字段并硬编码 redis（推荐删字段，彻底单一）；删 `MaxLockConns`（L19-22）字段与其默认 L73-75。`validate()`（L106-116）的 pgbouncer 分支暂留（Task 7 处理）。
- `internal/config/config.go`：删 `OwnershipBackend`（L45-46/L161）、`MaxLockConns`（L21/L126）；`Store()` 投影删 `MaxLockConns`（L252）、`OwnershipBackend`（L256）。
- `cmd/wadist/main.go:93` 删 `sc.OnShadowDivergence = ...`。
- `internal/metrics/metrics.go` L187-192 删 `ObserveOwnershipDivergence` 及其 `ownershipDivergence` collector（确认无其他 caller：`grep -rn ObserveOwnershipDivergence`）。

- [ ] **Step 6: 删除/移植 pg-耦合测试**

- 删 `internal/store/lock_test.go`（5 个 DeviceLock 测试，pg advisory 专属）。
- 删 `internal/store/ownership_shadow_test.go`。
- 删 `internal/node/chaos_test.go`（pg 版 `TestTakeoverChaos` + `mustPGLock`）——redis 版 `chaos_redis_test.go` 保留作接管混沌验收。
- 保留：`internal/store/ownership_race_test.go`、`internal/store/ownership_redis_test.go`、`internal/node/chaos_redis_test.go`。
- `internal/cluster/types.go:46` 注释引用 `*store.DeviceLock` 更新为 `LockHandle`。

- [ ] **Step 7: 验证 + gate**

Run: `go build ./... && TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/... ./internal/node/... ./internal/metrics/...`
Expected: PASS（含 `TestTakeoverChaos_Redis`、redis fencing 测试）

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor(store): 所有权坍缩为 redis-only, 删 pg/shadow 后端与 advisory 锁池"
```

---

## Task 4: 管理后台归属改读 Redis + owner_node DROP

owner_node 不再是死镜像——admin 账号列表/筛选/计数在读它。先把 admin 三处改从 Redis `owner:{jid}` 查，去掉 `ClaimAccount` 的 owner_node 写，再 DROP 列。

**Files:**
- Modify: `internal/store/ownership_redis.go`（新增批查/枚举方法）
- Modify: `internal/store/node.go:80-84`（`ClaimAccount` 去 owner_node 写，保留 last_connected_at）
- Modify: `internal/api/admin_api.go`（列表 SELECT L901、stats FILTER L935、deviceRow 注解）
- Modify: `internal/api/resources_query.go:43-48`（`Online` 筛选改用 redis 归属 jid 集）
- Create: `migrations/0018_drop_owner_node.sql`
- Modify: `internal/api/admin_api.go`/`resources_query.go` 对应测试

**Interfaces:**
- Produces:
  - `(m *Manager) OwnersFor(ctx context.Context, jids []string) (map[string]string, error)` — MGET `owner:{jid}`，返回 jid→"node:fence" 中的 nodeID（截 `:` 前）。缺失键不入 map。
  - `(m *Manager) OwnedJIDs(ctx context.Context) ([]string, error)` — 枚举全部当前被 redis 持有的 jid（SCAN `owner:*`）。
- Consumes（admin）：上述两方法。

- [ ] **Step 1: 写 OwnersFor / OwnedJIDs 的失败测试**

在 `internal/store/ownership_redis_test.go` 新增：
```go
func TestOwnersFor_And_OwnedJIDs(t *testing.T) {
	m := newTestManagerRedis(t) // 已存在的 redis-backend 构造器
	ctx := context.Background()
	h1, err := m.AcquireDeviceLock(ctx, "jid-A", "node-1")
	if err != nil { t.Fatalf("acquire A: %v", err) }
	defer h1.Release(ctx)
	h2, err := m.AcquireDeviceLock(ctx, "jid-B", "node-2")
	if err != nil { t.Fatalf("acquire B: %v", err) }
	defer h2.Release(ctx)

	owners, err := m.OwnersFor(ctx, []string{"jid-A", "jid-B", "jid-missing"})
	if err != nil { t.Fatalf("OwnersFor: %v", err) }
	if owners["jid-A"] != "node-1" || owners["jid-B"] != "node-2" {
		t.Fatalf("owners = %v", owners)
	}
	if _, ok := owners["jid-missing"]; ok { t.Fatalf("missing jid should be absent") }

	all, err := m.OwnedJIDs(ctx)
	if err != nil { t.Fatalf("OwnedJIDs: %v", err) }
	if len(all) != 2 { t.Fatalf("OwnedJIDs len = %d, want 2", len(all)) }
}
```

- [ ] **Step 2: 运行确认失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestOwnersFor_And_OwnedJIDs`
Expected: FAIL（方法未定义）

- [ ] **Step 3: 实现 OwnersFor / OwnedJIDs**

在 `internal/store/ownership_redis.go` 追加（`ownerKey(jid)` 已有，格式 `owner:{jid}`，值 `nodeID:fence`）：
```go
// OwnersFor batch-reads the current redis owner nodeID for each jid.
// Missing (unowned) jids are absent from the returned map.
func (m *Manager) OwnersFor(ctx context.Context, jids []string) (map[string]string, error) {
	out := make(map[string]string, len(jids))
	if len(jids) == 0 || m.cfg.Redis == nil {
		return out, nil
	}
	keys := make([]string, len(jids))
	for i, j := range jids {
		keys[i] = ownerKey(j)
	}
	vals, err := m.cfg.Redis.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("OwnersFor MGet: %w", err)
	}
	for i, v := range vals {
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		if idx := strings.IndexByte(s, ':'); idx >= 0 {
			out[jids[i]] = s[:idx]
		} else {
			out[jids[i]] = s
		}
	}
	return out, nil
}

// OwnedJIDs scans all currently-held owner:{jid} keys.
func (m *Manager) OwnedJIDs(ctx context.Context) ([]string, error) {
	if m.cfg.Redis == nil {
		return nil, nil
	}
	var out []string
	iter := m.cfg.Redis.Scan(ctx, 0, "owner:*", 512).Iterator()
	for iter.Next(ctx) {
		out = append(out, strings.TrimPrefix(iter.Val(), "owner:"))
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("OwnedJIDs scan: %w", err)
	}
	return out, nil
}
```
确认 `ownership_redis.go` 已 import `"strings"`、`"fmt"`（无则加）。

- [ ] **Step 4: 运行确认通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestOwnersFor_And_OwnedJIDs`
Expected: PASS

- [ ] **Step 5: ClaimAccount 去掉 owner_node 写**

`internal/store/node.go:81-84`，改：
```go
`UPDATE account_devices SET owner_node=$2, last_connected_at=now() WHERE account_jid=$1`,
```
为（去 owner_node 与其 `$2` 参数）：
```go
`UPDATE account_devices SET last_connected_at=now() WHERE account_jid=$1`,
```
同步删除函数签名里的 `nodeID` 参数或保留但不用——**检查调用点** `internal/node/orchestrator.go:74` `o.mgr.ClaimAccount(ctx, jid, o.nodeID)`：若删参数则同步改为 `o.mgr.ClaimAccount(ctx, jid)`；测试 `node_test.go`/`takeover_test.go`/`pgbouncer_protocol_test.go`（Task 7 会删）中的 `ClaimAccount(ctx, jid, node)` 调用同步。推荐保留 2 参签名不变、内部忽略 nodeID，减少调用点改动——但注释标注 nodeID 现无效。**决策：删参数，彻底**（改 orchestrator.go:74 与 node_test.go 各调用点）。

- [ ] **Step 6: admin 列表/stats 改用 redis 归属（先改测试）**

`internal/api/resources_query.go`：`deviceFilter` 的 `Online *bool` 语义改为"是否被 redis 持有"。`buildDeviceWhere` 删除 L43-48 的 `owner_node IS NOT NULL/NULL` 条件——owner 筛选移到 SQL 外（由 handler 用 redis jid 集做 `account_jid = ANY($n)` / `NOT (... = ANY($n))`）。为此 `buildDeviceWhere` 增加一个可选参数或由 handler 追加条件。

具体：`buildDeviceWhere` 签名改为 `buildDeviceWhere(f deviceFilter, ownedJIDs []string) (string, []any)`；当 `f.Online != nil` 时，追加：
```go
if f.Online != nil {
	args = append(args, ownedJIDs) // pgx 支持 []string → text[]
	n := len(args)
	if *f.Online {
		conds = append(conds, fmt.Sprintf("account_jid = ANY($%d)", n))
	} else {
		conds = append(conds, fmt.Sprintf("NOT (account_jid = ANY($%d))", n))
	}
}
```

- [ ] **Step 7: handleAdminListDevices 接线 redis**

`internal/api/admin_api.go` `handleAdminListDevices`（L894 起）：
- 若 `f.Online != nil`，先 `ownedJIDs, err := s.mgr.OwnedJIDs(ctx)`（`s.mgr` 为已有 Manager 引用；确认字段名）。传入 `buildDeviceWhere(f, ownedJIDs)`。
- 列表 SELECT（L901）去掉 `owner_node`；扫描（L914）去掉 `&d.OwnerNode`。
- 拉到本页 `out` 后，对页内 jids 调 `owners, _ := s.mgr.OwnersFor(ctx, pageJIDs)`，回填 `out[i].OwnerNode`（`*string`，无归属则 nil）。
- stats（L933-939）：`count(*) FILTER (WHERE owner_node IS NOT NULL)` 改为 `st.Online = len(ownedJIDsAll)`，其中 `ownedJIDsAll, _ := s.mgr.OwnedJIDs(ctx)`（无 Online 过滤时也要取一次用于 stats）。SQL 的该 FILTER 行删除，改为在 SQL 后用 redis 计数覆写 `st.Online`。

- [ ] **Step 8: DROP owner_node 迁移**

Create `migrations/0018_drop_owner_node.sql`（确认最新迁移序号，若非 0017 则顺延）：
```sql
-- owner_node 展示镜像退役：所有权真相迁至 Redis owner:{jid}，
-- 管理后台归属列/筛选/计数改从 Redis 读。
DROP INDEX IF EXISTS idx_acc_owner_node;
ALTER TABLE account_devices DROP COLUMN IF EXISTS owner_node;
```

- [ ] **Step 9: 验证 + gate**

Run: `go build ./... && TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/... ./internal/api/... ./internal/node/...`
Expected: PASS（admin 归属经 redis；owner_node 列已无引用）
额外自检：`grep -rn "owner_node" internal/ cmd/ --include=*.go | grep -v _test` 应零命中。

- [ ] **Step 10: Commit**

```bash
git add -A
git commit -m "refactor(admin): 归属改读 Redis owner:{jid}, DROP owner_node 列"
```

---

## Task 5: 会话存储坍缩为 badger-only

**Files:**
- Modify: `internal/store/manager.go`（switch L113-132 → badger-only、删 sqlstore import L16）
- Modify: `internal/store/config.go`（`SessionStore` 默认 L79-81 → badger 或删字段）
- Modify: `internal/config/config.go`（`WADIST_SESSION_STORE` 投影 L257）
- Modify: `internal/store/config_test.go`（SessionStore 默认断言）
- 保留：`internal/store/wabadger/difftest_test.go`/`difftest_store_test.go`（test-only 对拍，仍 import sqlstore）

**Interfaces:**
- Consumes: `wabadger.Open(dir)`、`wabadger.NewContainer(bdb, logger)`（已存在）。

- [ ] **Step 1: 更新默认断言（先失败）**

`internal/store/config_test.go`：把 SessionStore 默认由 `"pg"` 改断言为 `"badger"`（若保留字段），或删除该断言（若删字段）。**决策：删 `SessionStore` 字段与 env**，彻底单一——删对应测试断言。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/store/ -run TestConfigDefaults`
Expected: FAIL

- [ ] **Step 3: manager.go switch → badger-only**

`internal/store/manager.go` L113-132 改为（删 default/pg 分支与 sqlstore）：
```go
bdb, err := wabadger.Open(cfg.BadgerDir)
if err != nil {
	_ = sqlDB.Close()
	return nil, fmt.Errorf("open badger: %w", err)
}
badgerDB = bdb
container = wabadger.NewContainer(bdb, logger)
// NOTE: sqlDB 仍为业务表打开；whatsmeow session 表不再使用。
```
删 import `"go.mau.fi/whatsmeow/store/sqlstore"`（L16）。`badgerDB` 关闭逻辑（Close L286-288）、`closeBadger` 闭包保留。

- [ ] **Step 4: 删 SessionStore 字段/env**

- `internal/store/config.go`：删 `SessionStore string`（L49）字段与 withDefaults L79-81；`BadgerDir` 默认 L82-84 保留。
- `internal/config/config.go`：删 Store() 投影里 `SessionStore: os.Getenv("WADIST_SESSION_STORE")`（L257）；`BadgerDir`（L258）保留。

- [ ] **Step 5: 验证 + gate**

Run: `go build ./... && TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/...`
Expected: PASS（difftest 仍编译——它自建 sqlstore 容器作对拍参考，不受生产 switch 影响）

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor(store): 会话存储坍缩为 badger-only, 删 sqlstore 生产分支"
```

---

## Task 6: 代理层坍缩为 redis-only

删各方法的 `proxyAlloc==nil` PG 分配分支，redis 成唯一后端。PG 仍是 durable 真相源，`proxy_pool`/`chk_bindings`/`releaseWithinTx`/`GetBoundProxy` 保留。

**Files:**
- Modify: `internal/store/proxy.go`（`BindProxy`/`ReleaseProxy`/`ReportProxyFailure`/`ReportProxySuccess` 去 pg-alloc 分支）
- Modify: `internal/store/manager.go:251-260`（proxyAlloc 无条件构造、require redis）
- Modify: `internal/store/config.go`（`ProxyBackend` 默认/字段）
- Modify: `internal/config/config.go`（`WADIST_PROXY_BACKEND` 投影 L259）
- Modify: `internal/store/proxy_dispatch_test.go`、`proxy_test.go`、`config_test.go`

**Interfaces:**
- Consumes: `newRedisProxyAllocator(rdb, cooldown)`、其 `bind`/`releaseBinding`/`markDead`/`markAlive`/`rebuildFromPG`（已存在）。

- [ ] **Step 1: 更新代理后端测试（先失败）**

`internal/store/proxy_dispatch_test.go`：删 `TestManager_ProxyBackendDefaultPG`（L80，断言默认 `proxyAlloc==nil`）；`TestManager_ProxyBackendDispatch`（L53）改为无需设 `ProxyBackend=redis` 也走 redis。`internal/store/config_test.go:41` 删/改 `ProxyBackend` 默认 `"pg"` 断言。`proxy_test.go` 的 `TestBindProxy_NoOversellUnderConcurrency`（L66，PG 路径）移植到 redis 后端或删除（`proxy_zset_integration_test.go` 已覆盖 redis 无超配）。

- [ ] **Step 2: 运行确认失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run 'Proxy'`
Expected: FAIL

- [ ] **Step 3: proxy.go 去 pg-alloc 分支**

`internal/store/proxy.go`：
- `BindProxy`（L31）：删 `if m.proxyAlloc == nil { ... FOR UPDATE SKIP LOCKED ... }` 的 PG 分配分支（L35-82 附近的 SKIP LOCKED CTE），仅留 `return m.proxyAlloc.bind(...)`（L32 路径）。
- `ReleaseProxy`（L114）：删 pg 分支，留 `m.proxyAlloc.releaseBinding(...)`（L115）。
- `ReportProxyFailure`（L128）：留 `m.proxyAlloc.markDead(...)`（L144）。
- `ReportProxySuccess`（L153）：删 `proxyAlloc==nil` 的纯 Exec 分支（L158-164），留 `m.proxyAlloc.markAlive(...)`（L157）。
- **保留** `releaseWithinTx`（L86）、`GetBoundProxy`（node.go:155）、`proxy_pool` 写——redis bind/release 内部仍写 PG durable。

- [ ] **Step 4: manager.go proxyAlloc 无条件构造**

`internal/store/manager.go:251-260` 改为（去 `ProxyBackend=="redis"` 条件，require redis）：
```go
if cfg.Redis == nil {
	return nil, fmt.Errorf("store: redis is required (proxy allocator)")
}
m.proxyAlloc = newRedisProxyAllocator(cfg.Redis, cfg.ProxyCooldown)
m.proxyAlloc.rebuildFromPG(rctx, m.bizPool, now) // boot 重建热索引
```

- [ ] **Step 5: 删 ProxyBackend 字段/env**

`internal/store/config.go`：删 `ProxyBackend string`（L54）与 withDefaults L88-90；`ProxyCooldown`（L57，默认 60s L91-93）保留。`internal/config/config.go`：删 Store() 投影 `ProxyBackend: os.Getenv("WADIST_PROXY_BACKEND")`（L259）；`ProxyCooldown`（L260）保留。

- [ ] **Step 6: 验证 + gate**

Run: `go build ./... && TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/...`
Expected: PASS（`proxy_zset_integration_test`/`proxy_redis_*` 覆盖 redis 唯一路径）

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor(store): 代理坍缩为 redis-only, 删 pg SKIP LOCKED 分配(PG 仍 durable)"
```

---

## Task 7: 移除 PgBouncer（direct-only）

lockPool 已在 Task 3 删除，本任务清掉剩余 pgbouncer 脚手架。

**Files:**
- Modify: `internal/config/config.go`（删 `PgPoolMode` L104/L226、`PgQueryMode` L106/L227、Store() 投影 L261-262）
- Modify: `internal/store/config.go`（删 `PoolMode` L34、`QueryMode` L36、withDefaults L94-98、`validate()` pgbouncer 分支 L106-116）
- Modify: `internal/store/manager.go`（删 `applyQueryMode` L25-33 + 3 处调用 L145/193/222）
- Modify: `cmd/wadist/main.go:144`（删 pgbouncer 启动日志分支）
- Delete: `docker-compose.pgbouncer.yml`、`internal/store/pgbouncer_protocol_test.go`
- Modify: `internal/store/config_test.go`（删 pgbouncer/applyQueryMode/caps 测试）

- [ ] **Step 1: 删 pgbouncer 相关测试（先失败/先删）**

`internal/store/config_test.go`：删 `TestConfigValidate_PgbouncerRequiresRedis`(L65)、`TestConfigValidate_PgbouncerWithRedisOK`(L74)、`TestConfigValidate_CapsMaxConns`(L82)、`TestApplyQueryMode`(L93)；`TestConfigDefaults_DirectUnchanged`(L121) 改为不引用 PoolMode/QueryMode。删 `internal/store/pgbouncer_protocol_test.go` 整文件。

- [ ] **Step 2: 删 applyQueryMode 与调用**

`internal/store/manager.go`：删 `applyQueryMode`（L25-33）；删 3 处调用（L145/193/222）——这些行直接删除（poolCfg 无需再改 QueryExecMode）。

- [ ] **Step 3: 删 validate pgbouncer 分支与 Pool/Query 字段**

`internal/store/config.go`：`validate()`（L106-116）——pgbouncer 分支删除后 `validate()` 若无其他逻辑，body 变为 `return nil`（或若 validate 无其他调用者，连函数一起删，确认 `grep validate`）。删 `PoolMode`(L34)、`QueryMode`(L36) 字段与 withDefaults(L94-98)。
`internal/config/config.go`：删 `PgPoolMode`(L104/L226)、`PgQueryMode`(L106/L227)、Store() 投影 `PoolMode`/`QueryMode`(L261-262)。

- [ ] **Step 4: 删 main.go 启动日志 + compose 文件**

`cmd/wadist/main.go:144` 删 `if cfg.PgPoolMode == "pgbouncer" { ...日志... }` 块。删文件 `docker-compose.pgbouncer.yml`。

- [ ] **Step 5: 验证 + gate**

Run: `go build ./... && TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/... ./cmd/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor(store): 移除 PgBouncer, 池化坍缩为 direct-only"
```

---

## Task 8: 反封控制环固化为常驻（删布尔主门控，留数值调参）

**Files:**
- Modify: `cmd/wadist/main.go`（L470/480 governor、L252/360 ghost reaper、L139/141 backoff、L175/256 antifp 无条件化）
- Modify: `internal/sendgate/admission.go`（删 `!a.bp.On` 早退 L85、`if bp.On` L70）
- Modify: `internal/cluster/conn_whatsmeow.go`（`EnableAutoReconnect=false` 常置、去 `manageLifecycle` 参数 L42/L74）
- Modify: `internal/config/config.go`（删 `RiskGovernor`/`SegmentGovernor`/`GhostReaper`/`BackoffOn`/`AntifpOn` 字段+env；保留 Gov*/Seg*/Ghost*/Backoff*/BootRamp 数值）
- Modify: `internal/config/config_test.go`（删 on/off 默认断言 L292/313/330）

**Interfaces:**
- Consumes: `dispatch.NewGovernor(...)`、`node.NewGhostReaper(...)`、`sendgate` `BackoffParams`、`cluster.NewRoutingSenderWithPolicy(...)`、`cluster.NewWAConn(...)`（签名改：去 `manageLifecycle bool`）。

- [ ] **Step 1: 更新 config 默认断言（先失败）**

`internal/config/config_test.go`：删 `TestLoad_AntifpDefaults`(L292)、`TestConfig_RiskGovernorDefaults`(L313)、`TestConfig_SegmentGovernorDefaults`(L330) 中对布尔门控默认值的断言（这些字段将不存在）。保留对数值默认（GovSLO=0.02 等）的断言——若这些断言引用被删字段则改为引用保留的数值字段。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/config/`
Expected: FAIL（编译错误——断言引用将删字段）

- [ ] **Step 3: admission.go 去 On 门**

`internal/sendgate/admission.go`：删 L70 `if bp.On {` 条件（初始化恒执行）；删 L85 `if !a.bp.On { return ... }` 早退分支（admission 恒走 backoff 逻辑）。`BackoffParams` 结构删 `On bool` 字段，保留 `Factor`/`Max`/`TTL` 数值。

- [ ] **Step 4: conn_whatsmeow 常关自动重连**

`internal/cluster/conn_whatsmeow.go`：`NewWAConn`（L42）签名删 `manageLifecycle bool` 参数；L74 `client.EnableAutoReconnect = false` 无条件执行（ghost reaper 常驻，由 reaper 回收而非 whatsmeow 自动重连）。LoggedOut/StreamReplaced 事件处理器（置 permanent 标志）保持常装。

- [ ] **Step 5: main.go 无条件构造控制环**

`cmd/wadist/main.go`：
- L470 `if cfg.RiskGovernor == "on" {` → 去条件，`gov := dispatch.NewGovernor(...)` + `sup.Go(gov.Run)` 恒执行。
- L480 `if cfg.SegmentGovernor == "on" {` → 去条件，`gov.WithSegments(...)` 恒执行。
- L252 `cluster.NewWAConn(device, ..., cfg.GhostReaper=="on")` → `cluster.NewWAConn(device, ...)`（去末参）。
- L360 `if cfg.GhostReaper == "on" {` → 去条件，`reaper := node.NewGhostReaper(...)` + `sup.Go(reaper.Run)` 恒执行。
- L139 `sendgate.BackoffParams{On: cfg.BackoffOn, ...}` → 去 `On` 字段；L141 `if cfg.BackoffOn {` 日志块删除或改无条件。
- L175 `if cfg.AntifpOn {` → 去条件，恒 `cluster.NewRoutingSenderWithPolicy(...)`（删 else 裸 `NewRoutingSender`）。
- L256 `if cfg.AntifpOn {` → 去条件，恒 `SetPresence(true)`。

- [ ] **Step 6: 删布尔门控字段/env**

`internal/config/config.go`：删 `RiskGovernor`(L79/L205)、`SegmentGovernor`(L88/L214)、`GhostReaper`(L97/L220)、`BackoffOn`(L108/L229)、`AntifpOn`(L48/L183) 字段与加载。**保留**：所有 Gov*/Seg*/Ghost*/Backoff* 数值（L83-84/92-93/98-100/109-111 及加载）、`BootRamp`(L102/L224)、`FenceOnSend`(L49/L184)、typing/dwell/linger 数值。

- [ ] **Step 7: 验证 + gate**

Run: `go build ./... && TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/config/... ./internal/sendgate/... ./internal/cluster/... ./cmd/...`
Expected: PASS（governor/reaper 组件测试不读 flag，不受影响）

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor: 反封控制环固化为常驻(删 5 布尔门控, 留数值调参)"
```

---

## Task 9: Redis 强制 fail-fast + 部署清单

Redis 成硬依赖；补 badger 持久卷与 redis AOF。

**Files:**
- Modify: `cmd/wadist/main.go`（redis 连接失败 fail-fast；确认 config 层已 require）
- Modify: `docker-compose.worker.yml`（badger 卷挂载）
- Modify: redis 服务配置（AOF）— `docker-compose.prod.yml` 或 redis 命令行
- Modify: `docs/V1.0-HANDOVER-zh.md`（更新 env 清单：删除的 flag、新增硬依赖）

- [ ] **Step 1: main.go redis fail-fast**

`cmd/wadist/main.go` redis client 创建后立即 `Ping`，失败 `log.Fatal`：
```go
if err := rdb.Ping(ctx).Err(); err != nil {
	log.Fatalf("redis required but unreachable: %v", err)
}
```
（Task 6 的 manager 层 `cfg.Redis == nil` 守卫为第二道。）

- [ ] **Step 2: worker compose 挂 badger 卷**

`docker-compose.worker.yml` `wadist` 服务加：
```yaml
    volumes:
      - wadist_badger:/var/lib/wadist/badger
```
文件末尾加顶层：
```yaml
volumes:
  wadist_badger:
```

- [ ] **Step 3: redis 开 AOF**

确认 `docker-compose.prod.yml` 的 redis 服务 command 含 `--appendonly yes`（无则加）。

- [ ] **Step 4: 更新 HANDOVER 文档**

`docs/V1.0-HANDOVER-zh.md`：从 env 清单删除 `WADIST_DISPATCH_MODE`/`WADIST_ASYNQ_CONCURRENCY`/`WADIST_OWNERSHIP_BACKEND`/`WADIST_MAX_LOCK_CONNS`/`WADIST_SESSION_STORE`/`WADIST_PROXY_BACKEND`/`WADIST_PG_POOL_MODE`/`WADIST_PG_QUERY_MODE`/`WADIST_RISK_GOVERNOR`/`WADIST_SEGMENT_GOVERNOR`/`WADIST_GHOST_REAPER`/`WADIST_BACKOFF_ON`/`WADIST_ANTIFP`；标注 Redis+AOF、Badger 卷为硬依赖；保留数值调参清单（SendRate/Gov*/Seg*/Ghost*/Backoff*/BootRamp）。

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "chore(deploy): redis fail-fast + badger 卷 + AOF, 更新 HANDOVER env 清单"
```

---

## Task 10: 全量 gate + 端到端闭环验证

**Files:** 无代码改动（验证任务）；如发现 `go mod tidy` 可移除依赖则提交 go.mod/go.sum。

- [ ] **Step 1: go mod tidy**

Run: `go mod tidy`
Expected: asynq 仍在（takeover 用）；sqlstore 属 whatsmeow 子包（difftest 用）；无意外移除。

- [ ] **Step 2: 全量 make gate**

Run: `make gate`
Expected: `tidy vet test-race labels vuln` 全绿。**四条不变式回归**（billing 恒等、sent_today 对称、message_id 幂等、`TestTakeoverChaos_Redis`）必须在 test-race 中通过。

- [ ] **Step 3: 残留门控自检**

Run:
```bash
grep -rn "DispatchMode\|AsynqConcurrency\|OwnershipBackend\|MaxLockConns\|SessionStore\|ProxyBackend\|PgPoolMode\|PgQueryMode\|RiskGovernor\|SegmentGovernor\|GhostReaper\|BackoffOn\|AntifpOn\|owner_node" internal/ cmd/ --include=*.go | grep -v _test
```
Expected: 零命中（数值调参字段如 SendRate/Gov*/Seg* 不在此列表，应仍在）。

- [ ] **Step 4: 真机首发端到端（部署后，运营协同）**

在真机部署新镜像 → 一个账号重扫码登录 → 发一条真实消息，确认闭环：pump 取令牌 → redis 所有权 acquire（fence）→ badger 会话读写 → 发送 → PG `recipients` 记账 + `sent_today` +1 + billing hold。检查 admin 后台账号列表归属列显示正确（读 redis）。

- [ ] **Step 5: 标定 ramp（不阻塞合并，运营执行）**

`WADIST_SEND_RATE` 从 17 → 50 → 100 → 167 逐级抬，每级稳态观测 `FleetBanRate < GovSLO(0.02)`、Ghost Reaper 回收速率、代理冷却队列，探到不招封的单机吞吐天花板。日发百万只需稳态 ~12/s，阶段 1（17/s）即覆盖，ramp 用于确认余量。

---

## Self-Review

**Spec 覆盖核对**：
- §4 删除清单 → Task 2(发送)/3(所有权)/5(会话)/6(代理)/7(pgbouncer)/8(控制环) 逐层覆盖；崩溃恢复保留 = Task 2 Step 4 保留 ReclaimOrphanedAssignments。✅
- §4a 删 PgBouncer → Task 7。✅
- §4b 控制环常驻 → Task 8。✅
- §5 Redis 必须/AOF/Badger 卷 → Task 9；全量重扫码 = 无迁移工具（计划无此任务，符合）；owner_node DROP + admin 改读 Redis → Task 4。✅
- §3 四条不变式 → Global Constraints + Task 10 Step 2 回归。✅
- §6 验证 → Task 10。✅
- §7 spec→plan→subagent TDD → 本计划结构。✅

**占位符扫描**：无 TBD/TODO；删除项均含确切文件:行号或符号名；新增代码（OwnersFor/OwnedJIDs、迁移 SQL、compose 卷）均给完整内容。✅

**类型一致性**：`SendBodyResolver`（Task 1 迁 types.go，Task 2 消费）、`ErrDeviceLocked`（Task 1 迁 ownership.go，Task 3 redis 后端用）、`OwnersFor`/`OwnedJIDs`（Task 4 定义并消费）、`NewWAConn` 去 `manageLifecycle`（Task 8 Step 4 定义签名、Step 5 更新调用点）、`ClaimAccount` 去 nodeID 参数（Task 4 Step 5 定义、同步 orchestrator.go:74）。✅

**已知需实施期确认的软点**（非占位符，是需 grep 核实的边界）：
- Task 2 main.go 行号（L154-169 等）以实现时实际文件为准（config 删字段会使后续行号漂移，按符号定位）。
- Task 3 `validate()` 删空后是否还有调用者（Task 7 处理）。
- Task 7 `applyQueryMode` 删除后 poolCfg 构造无其他副作用。
- Task 4 `s.mgr` 在 api.Server 中的实际字段名（实现时确认 Manager 引用路径）。
