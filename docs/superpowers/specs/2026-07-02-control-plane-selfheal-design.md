# 控制面自愈缺口修复设计（死代理自动重绑 + 调度闭环对账）

> 状态：设计稿（2026-07-02）。承接 [`2026-06-30-control-plane-impl-design.md`](2026-06-30-control-plane-impl-design.md) 落地后暴露的两个运营态缺口。分支建议 `docs/control-plane-selfheal`。
> 实现计划见 [`../plans/2026-07-02-proxy-janitor.md`](../plans/2026-07-02-proxy-janitor.md)、[`../plans/2026-07-02-scheduler-loop-closure.md`](../plans/2026-07-02-scheduler-loop-closure.md)。

## 1. 背景与问题定性

控制面（`internal/control`）V1 已合并（commit 9699757）：温驻留工作集 `WorkingSet` + 去同步调度 `Scheduler`（due:{cc} ZSet）+ 代理粘性 `StickyBindProxy`，全程回调注入、零引擎依赖。上线核查发现**两个自愈缺口**——不是选型错误，而是编排闭环没合上：

### 缺口 A — 死代理无自动重绑（🔴）

- `store.ReportProxyFailure`（[proxy.go:121](../../../internal/store/proxy.go#L121)）连续失败 5 次自动 `is_alive=FALSE`。
- `store.ListAccountsByDeadProxies`（[proxy.go:147](../../../internal/store/proxy.go#L147)）已实现，**全代码库零调用方**。
- `control.StickyBindProxy`（[proxyaffinity.go:11](../../../internal/control/proxyaffinity.go#L11)）：账号仍绑在死代理上时 `getBound→true`，**直接 skip**——单纯重新 warm 不会换代理。
- **后果**：某代理死亡后，绑它的活跃账号会一直绑死、带着不可用代理重连或不发信，无人工介入不恢复。

### 缺口 B — 主动调度闭环未合上（🔴）

- `Scheduler.Enqueue`（[scheduler.go:16](../../../internal/control/scheduler.go#L16)）**全非测试代码零调用方**。due-ZSet 只有启动 `SeedDue` 写一次，`WorkingSet.Tick` 的 `PopDue`（[workingset.go:94](../../../internal/control/workingset.go#L94)）只出不进。
- **后果**：启动播种后每账号只被主动预热一次，ZSet 永久排空。启动后新建/解封的账号、进程重启后、被驱逐后的账号**永不再进主动预热**，只能靠 dispatch 命中冷账号时 RoutingSender 触发的 `warm:req` 被动补偿——存在"先有鸡还是先有蛋"。

## 2. 设计原则

1. **不碰五大引擎包核心逻辑**（[[klws-redlines]]）。新增编排一律进 `internal/control`，回调注入，零 import node/cluster/store。
2. **复用既有原语**：`ReleaseProxy` + `Evict`（GracefulClose）+ `warm:req` + `StickyBindProxy` + `NextEligibleMs` 已全部就位。两个修复都是"把已有零件接上",不发明新机制。
3. **幂等 + 无 churn**：`StartAccountWithLock` 对本节点已持锁账号返回 `(nil,nil)`（[orchestrator.go:59](../../../internal/node/orchestrator.go#L59)），重复 warm 无害——故 B 用"周期对账"而非"发送后重入队"，避免对已温账号反复重连。

## 3. 缺口 A 设计 — ProxyJanitor

新增 `control.Janitor`，周期（默认 30s）执行：

```
accts := ListDeadProxyAccounts(ctx, batch)   // 跨租户，(jid, cc) 对
for each (jid, cc):
    Release(ctx, jid)          // 解绑死代理 → StickyBindProxy 的 getBound 转 false
    Evict(ctx, jid, 0)         // linger=0 立即断掉跑在死代理上的活会话
    RequestWarm(ctx, jid, cc)  // RPUSH warm:req "jid|cc" → 下个 WorkingSet.Tick 走
                               //   StickyBindProxy（现未绑）→ BindProxy 挑新 alive 代理
                               //   → Warm 带新代理重连（ApplyProxy 在 Connect 前注入）
```

**关键**：Janitor 只编排三个已有回调，重绑动作由既有 `StickyBindProxy`→`BindProxy`→`ApplyProxy` 链路完成，Janitor 自身不含任何重绑/选代理逻辑。

### 3.1 唯一红线擦边点（需签字）

`ListAccountsByDeadProxies` 形参强制 `tenantID` 且走 `bizPool`，而无任何租户列举导出方法。系统级 janitor 需跨租户。方案：

- **（推荐，本设计采用）** 新增只读方法 `store.ListDeadProxyAccountsAll(ctx, limit) ([]DeadProxyAccount, error)`：**逐字镜像**现有 `ListAccountsByDeadProxies` 的查询，去掉 tenant 过滤、追加 `p.country_code` 返回。属"新增非改逻辑"，与 B2 新增 Ownership 层同性质。不触碰任何计费/防封/绑定事务。
- 备选（不改 store）：Janitor 侧遍历已知租户 ID 逐租户调用——但需要一个租户列举来源，当前不存在，反而更绕。

## 4. 缺口 B 设计 — Reconciler（调度闭环对账）

新增 `control.Reconciler`，周期（默认 5min）执行：

```
active := ListActive(ctx)                 // store.ListActiveAccounts，天然跨租户
resident := SMEMBERS ctl:resident         // 一次拉进 Go set
for each cc: dueMembers[cc] := ZRANGE due:{cc} 0 -1   // 一次拉进 Go set
for each jid in active:
    if jid in resident:  continue         // 已温，无需调度
    cc := CountryOf(ctx, jid)
    if jid in dueMembers[cc]: continue    // 已在队列等待
    Enqueue(ctx, jid, cc, NextDue(cc))     // 既非温、又未排队 → 重新纳入主动预热
```

覆盖：①启动时账号列表为空；②运行中新建/解封账号；③进程重启丢失调度项；④被驱逐后回收。`NextDue` = `NextEligibleMs(now, randQuota, Window{9,22}, rng)`，由 main 注入。

**幂等性**：resident 账号已从 due 移除（PopDue 时 ZREM），未温账号要么在 due 等待、要么被本次补入——重复执行不产生重复项（ZADD 同 member 覆盖 score）。

### 4.1 范围外（明确不做，后续独立 spec）

- **工作集轮换（KeepWarmHorizon 驱逐）**：当前 `WorkingSet.Tick` 驱逐步仅移除非温账号，`KeepWarmHorizon` 未启用 → 100K 池中先温的 1500 个会长期占位、其余拿不到轮次。这是 reconciler 之外的**独立增强**（温龄到期主动驱逐让位），本轮不含，另开 spec。Reconciler 只保证"该被调度的都在被调度"，不保证"公平轮换"。
- Risk Governor（AIMD 反压，对应风险 #3）、短链、国家分片——各自后续。

## 5. 接口总览（新增，全在 control）

```go
// janitor.go
type DeadProxyAccount struct{ JID, CC string }
type JanitorDeps struct {
    ListDeadProxyAccounts func(ctx context.Context, limit int) ([]DeadProxyAccount, error)
    Release               func(ctx context.Context, jid string) error
    Evict                 func(ctx context.Context, jid string, linger time.Duration)
    RequestWarm           func(ctx context.Context, jid, cc string) error
}
func NewJanitor(cfg JanitorConfig, deps JanitorDeps) *Janitor
func (j *Janitor) Tick(ctx context.Context) (int, error)   // 返回处理条数
func (j *Janitor) Run(ctx context.Context, interval time.Duration) error

// reconciler.go
type ReconcileDeps struct {
    ListActive func(ctx context.Context) ([]string, error)
    CountryOf  func(ctx context.Context, jid string) string
    NextDue    func(cc string) int64
}
func NewReconciler(rdb *goredis.Client, sched *Scheduler, ccs []string, deps ReconcileDeps) *Reconciler
func (r *Reconciler) Tick(ctx context.Context, nowMs int64) (int, error)  // 返回补入条数
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) error

// store/proxy.go（新增只读，红线擦边，需签字）
type DeadProxyAccount struct{ JID, CountryCode string }
func (m *Manager) ListDeadProxyAccountsAll(ctx context.Context, limit int) ([]DeadProxyAccount, error)
```

## 6. 配置（新增 env）

| env | 默认 | 用途 |
|---|---|---|
| `WADIST_PROXY_JANITOR_INTERVAL_MS` | 30000 | Janitor tick 周期 |
| `WADIST_PROXY_JANITOR_BATCH` | 256 | 单轮处理死代理账号上限 |
| `WADIST_RECONCILE_INTERVAL_MS` | 300000 | Reconciler tick 周期（5min） |

## 7. 可观测性

复用 metrics 有界 label 门禁。新增计数：
- `wadist_proxy_rebind_total`（Janitor 触发的重绑次数）
- `wadist_schedule_reconciled_total`（Reconciler 补入 due 的账号数）

两者均无高基数 label，过 `scripts/check_metric_labels.sh`。

## 8. 灰度与验收

- 两处均**默认开**（自愈是纠错，非行为变更；关闭反而回到缺陷态）。可用 interval 环境变量调节频率，设 0 关闭（实现中 `<=0` 跳过 Run）。
- 验收门：
  - `control` 包仍零 import node/cluster/store（`go list -deps` 核对）。
  - Janitor 集成测试：绑代理→打死→跑一轮→账号 `proxy_id` 指向新 alive 代理 + 旧 session 被 evict + `warm:req` 有该 jid。
  - Reconciler 集成测试：空启动→插一个 active 账号→跑一轮→该 jid 出现在 `due:{cc}` 且 score 在 `[now+60s, now+maxGap]`。
  - `make gate` 绿。

相关：[[klws-redlines]] [[klws-dispatch-throughput-design]]
