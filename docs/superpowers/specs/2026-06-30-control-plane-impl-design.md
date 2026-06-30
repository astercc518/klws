# 控制面编排（温驻留 + 去同步调度 + 代理粘性）实现设计

- 日期：2026-06-30
- 状态：实现设计草案，待评审
- 关联：[2026-06-30-高吞吐发送编排-design.md](2026-06-30-高吞吐发送编排-design.md) 第 4–8 节（本 spec 把其中控制面落到实现级）；依赖 B2（并发天花板）与反指纹（GracefulClose/presence）两计划。
- 目标：在 100K 账号池中维持 ~1500 个"温会话"工作集，按去同步的到期调度滚动轮换，让现有 campaign-dispatch 能选到在线账号；代理终身粘性。
- 红线：**不改** `internal/dispatch`/`internal/sendgate`/`internal/billing` 逻辑。新代码在**新增包** `internal/control`，仅调用导出方法（`node.Orchestrator.StartAccountWithLock`、`Session.GracefulClose`、`store.BindProxy`/`GetBoundProxy`、`store.ListActiveAccounts`）。`cluster.RoutingSender` 的"无会话"钩子属反指纹计划的同一文件，加性。

---

## 1. 核心集成难题与解法（本 spec 的关键决策）

**难题**：现有 `dispatch.selectAccount` 按 `effective_quota - sent_today DESC` 选号，**对会话是否在线无感知**。100K 账号仅 ~1500 温 → dispatch 常选到冷账号 → `RoutingSender` 返回 "no active session" → worker requeue。不能改 dispatch（红线）。

**解法 = 主动 + 被动双向收敛（均不碰 dispatch）**：

1. **主动预热**：Working-Set Manager 持续预热"到期队列头部"账号。到期判据 = 账号有剩余 quota 且距上次发送够久——**这与 `selectAccount` 偏好的 `effective_quota - sent_today` 同源**，所以温集天然趋近 dispatch 想选的账号。
2. **被动补热**：`RoutingSender` 遇 "no active session" 时（反指纹计划已可编辑该文件），向 Redis `LPUSH warm:req <jid>`；控制面消费该队列，立即预热该账号。几个 tick 内温集收敛到 dispatch 的实际需求。

> 关键洞察：主动调度器与 `selectAccount` 都以 `effective_quota/sent_today` 为序，二者**自然对齐**，被动补热消化残差。无需改 dispatch 一行。

---

## 2. 架构（新增包 `internal/control`）

```
Redis ZSet due:{cc}  (score = next_eligible_ms)
        │ 主动：ZPOPMIN 取到期
        ▼
Working-Set Manager ──维持 R≈WarmTarget──▶ node.Orchestrator.StartAccountWithLock
        │  warm 后:(反指纹计划)connect→presence-available→dwell                 │
        │  evict:Session.GracefulClose(linger)                                  ▼
        │                                                            cluster.Registry(温会话)
        ▲ 被动：BRPOP warm:req                                                  │
        │                                                  现有 dispatch/asynq 选号→RoutingSender 发送
        │                                                                       │ 无会话→LPUSH warm:req
        └─ 发送后:Recycler 写回 due:{cc} 新 next_eligible(去同步) ◀─── 发送成功信号(receipt/metrics)
Proxy Affinity:仅首次/代理死亡才 BindProxy;否则复用 GetBoundProxy
```

组件（v1 范围）：
- **Eligibility Scheduler**：`due:{cc}` ZSet 维护与到期弹出。
- **Working-Set Manager**：维持 WarmTarget 个温会话，主动+被动预热、容量驱逐、远端到期驱逐。
- **Proxy Affinity**：粘性绑定（控制 `BindProxy` 调用频率）。
- **Recycler**：发送后按去同步公式写回 next_eligible。

**v1 不做（YAGNI，列为后续独立计划）**：Risk Governor（AIMD 自适应 τ）、短链服务、国家分片多节点 send 路由。理由：先把"温驻留 + 轮换 + 粘性代理"跑通并用 canary 标定，再上自适应控速。

---

## 3. Redis Key Schema（控制面新增）

```
due:{cc}           ZSET  member=jid  score=next_eligible_unix_ms     到期队列(按国家)
warm:req           LIST  jid...                                       被动补热请求
ctl:resident       SET   jid...                                       当前温集(可观测/驱逐扫描)
```

> 与 B2 的 `owner:{jid}` 等正交。`due:{cc}` 的 cc 取自账号国家（经其粘性代理国家，account_devices↔proxy_pool）。

---

## 4. 组件接口（`internal/control`）

```go
// Scheduler 维护 due:{cc} 到期队列。
type Scheduler struct{ rdb *goredis.Client }
func (s *Scheduler) Enqueue(ctx, jid, cc string, nextEligibleMs int64) error    // ZADD due:{cc}
func (s *Scheduler) PopDue(ctx, cc string, now int64, max int) ([]string, error) // ZPOPMIN 且 score<=now
func (s *Scheduler) SeedDue(ctx, accounts []AccountCC, now int64) error          // 启动播种(打散相位)

// WorkingSet 维持温会话集。warm/evict 通过注入的回调驱动 node 层（control 不直接 import node 以免环）。
type WorkingSet struct {
    rdb       *goredis.Client
    sched     *Scheduler
    target    int
    warmFn    func(ctx, jid string) (bool, error)         // = orch.StartAccountWithLock 包装(true=已温)
    evictFn   func(ctx, jid string, linger time.Duration) // = registry.Get + Session.GracefulClose
    isWarmFn  func(jid string) bool                        // = registry.Get 存在性
    bindProxy func(ctx, jid, cc string) error              // Proxy Affinity(粘性)
    cfg       Config
}
func (w *WorkingSet) Tick(ctx context.Context, now int64) error // 一拍：主动预热+被动补热+驱逐
func (w *WorkingSet) Run(ctx context.Context, interval time.Duration) error

// Recycler 发送后重排 next_eligible(去同步)。
func NextEligibleMs(now int64, quotaToday int, localWindow Window, rng *rand.Rand) int64
```

> `control` 包**不 import `node`/`cluster`**，而是接收 `warmFn`/`evictFn`/`isWarmFn`/`bindProxy` 回调（在 `cmd/wadist` 注入），避免依赖环并保持可测。

---

## 5. WorkingSet.Tick 算法

```text
const H = KeepWarmHorizon (90s)
1. 被动补热：BRPOP/LRANGE warm:req（非阻塞批量取 K 个）→ 对每个 warmFn + bindProxy(粘性)
2. 主动预热：while len(resident) < target:
      for cc in activeCountries:
          jids = sched.PopDue(cc, now, batch)
          if empty: continue
          for jid: bindProxy(粘性); ok=warmFn(jid); if ok: SADD ctl:resident
3. 驱逐：for jid in ctl:resident:
      nextDue = peek due score(jid)
      if nextDue - now > H: evictFn(jid, linger); SREM ctl:resident
```

- `bindProxy` 内部：`GetBoundProxy` 成功（已绑）→ 跳过；`ErrProxyNotBound`/代理死 → `BindProxy(jid, cc)`。**粘性**。
- warm 失败（`StartAccountWithLock` 返回 nil,nil = 被他人持有；或 err）→ 不计入 resident，下拍重试或跳过。

---

## 6. 去同步调度公式（Recycler）

```go
func NextEligibleMs(now int64, quotaToday int, w Window, rng *rand.Rand) int64 {
    base := w.SecondsActive() / int64(max(quotaToday,1))      // 均匀基距(秒)
    jitter := int64(float64(base) * (rng.Float64()*0.8 - 0.4)) // ±40%
    delta := base + jitter
    if delta < 60 { delta = 60 }                               // 硬保证 ≥1min
    return now + delta*1000
}
```

`Window` = 账号 cc 本地 09:00–22:00；夜间到期则顺延到次日窗口起点 + 抖动。`quotaToday ∈ [5,10]`（U 随机，每日重置）。每账号独立相位 → 全局近 Poisson，杀掉整点同步波。瞬间发送仍过 `sendgate.Admit` 3s minGap 兜底。

---

## 7. 与现有 startup 的衔接（替换全量预热）

现状 `cmd/wadist` 启动时 `ListActiveAccounts` → `StartAccounts(全部)`。100K 下不可行。**改为**：启动只 `SeedDue`（把 active 账号按打散相位播入 `due:{cc}`），由 `WorkingSet.Run` 增量预热到 target。

> 这是 `cmd/wadist` 装配改动（非红线）：去掉"启动全量 StartAccounts"，换成 SeedDue + WorkingSet.Run 循环（经 `sup.Go` 纳入现有生命周期）。takeover 扫描仍保留（接管 stale）。

---

## 8. 配置

| env | 默认 | 含义 |
|---|---|---|
| `WADIST_WARM_TARGET` | 1500 | 温会话工作集大小 |
| `WADIST_WS_TICK_MS` | 500 | WorkingSet 拍间隔 |
| `WADIST_KEEP_WARM_HORIZON_MS` | 90000 | 远端到期驱逐阈 |
| `WADIST_WARMREQ_BATCH` | 64 | 每拍被动补热条数 |
| `WADIST_DAILY_QUOTA_MIN/MAX` | 5 / 10 | 单账号日条数随机区间 |
| `WADIST_ACTIVE_WINDOW` | 09:00-22:00 | 本地活跃窗 |

---

## 9. 测试策略

- `control` 包纯逻辑，注入 fake 回调，**无需真 WhatsApp**：
  - `TestSchedulerPopDue`：只弹出 score<=now，按 score 升序。
  - `TestNextEligibleSpread`：≥60s、含 ±40% 抖动、落在本地窗。
  - `TestWorkingSetActiveWarm`：resident<target 时按 due 头部预热到 target。
  - `TestWorkingSetReactiveWarm`：warm:req 中的 jid 被预热（fake warmFn 记录）。
  - `TestWorkingSetEvictFarDue`：远端到期账号被 evictFn(graceful)。
  - `TestProxyAffinityStickiness`：已绑账号不再调 BindProxy；未绑/死才调。
- 集成（testcontainer Redis）：ZSet/LIST/SET 操作。
- 现有 dispatch/sendgate/billing 测试不受影响（未改）。

---

## 10. 红线影响

- 新增 `internal/control` 包（非红线），回调注入，不 import node/cluster。
- 改 `cmd/wadist`（装配，非红线）：startup 换 SeedDue + WorkingSet.Run。
- `RoutingSender` 的 warm:req LPUSH 钩子在反指纹计划的同一文件（加性）。
- **未改** dispatch/sendgate/billing/store 逻辑。

---

## 11. 待决策 / 风险

**待决策**：
1. `warm:req` 被动补热用 `BRPOP`（阻塞，单独 goroutine）还是每拍 `LRANGE+LTRIM`（批量）？默认后者（与 Tick 同拍，简单）。
2. v1 是否需要 Send Pump 独立令牌桶？默认否——发送节奏由"温集大小 × sendgate 3s pacing"自然决定；τ 自适应留给后续 Risk Governor。

**风险**：
- 温集与 dispatch 选号的收敛速度依赖被动补热延迟；canary 观测 requeue 率，必要时增大 warm:req batch。
- 全量 startup→SeedDue 的切换要保证 takeover 仍能接管 stale（保留 RunTakeoverScanner）。

---

## 12. 验收

- 100K 播种下，温集稳定维持 ~WarmTarget，requeue 率随被动补热收敛到低位。
- 单账号相邻发送 ≥60s、日条数 ∈[5,10]、按本地窗打散（无整点波）。
- account↔proxy 1:1 终身粘性（无每轮换 IP）。
- 驱逐走 GracefulClose；未改 dispatch/sendgate/billing 逻辑。
- 后续：Risk Governor（AIMD）、短链各自独立 spec+计划。
