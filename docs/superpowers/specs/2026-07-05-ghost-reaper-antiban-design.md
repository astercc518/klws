# 反封层 / 有界 Ghost Reaper + Boot 斜坡 设计

> 里程碑 M6（承接单机高密度重构 spec 第 9 节 M4「反封层」；决策 #2「有界 Ghost Reaper」的落地）。
> 日期 2026-07-05。全程配置门控、默认=今日行为、**零爆炸半径**。

## 1. 背景与问题

whatsmeow device 在本系统中**温驻留**（常驻连接，Little's Law R=τ×T_hold）。whatsmeow `NewClient` 默认 `EnableAutoReconnect=true` 且 `NewWAConn` 从不覆盖，因此一条半死连接（如代理已死）会在 whatsmeow 内部**无限自愈重连**，持续占着 websocket FD、内部读/keepalive/autoReconnect goroutine、以及出站代理 TCP socket，而 wadist 收不到任何信号。现有健康环 `guardSession`（`internal/node/orchestrator.go`）**只查 PG advisory 锁**（跨节点所有权），对「锁还在、WA socket 已死」的 ghost 完全无感。全仓库除 `events.Receipt` 外**不处理任何 whatsmeow 生命周期事件**。

在 10 万常驻账号规模下，未回收的 ghost 会累计泄漏 FD/goroutine/代理 socket，直接击穿单机密度目标。

## 2. 目标

1. **夺回连接生命周期**：关闭 whatsmeow 自动重连，改由 wadist 控制层显式管理连接与重连。
2. **有界 Ghost Reaper**：检测 ghost 连接，在硬上限（`ghost_max` 并发、`ghost_ttl≤30s` dwell）下强制回收，释放资源。
3. **Boot 斜坡**：启动/重启后把 warm 洪峰摊到一个窗口内，避免重连风暴（其本身即像封号触发特征）。
4. **零爆炸半径**：全部能力配置门控，默认等于今日生产行为。

## 3. 已锁决策（2026-07-05 brainstorm）

1. **auto-reconnect**：✅ **关掉，夺回生命周期**——事件处理器作主信号 + 轮询兜底静默 ghost。
2. **永久信号处置**：✅ `LoggedOut`/`StreamReplaced` → **回收 + PG 标记 `logged_out`，不再自动 re-warm**（防对被封/登出号狂连加速封杀）；瞬态 ghost → 回收 + re-warm。
3. **硬上限默认**：✅ `ghost_ttl=20s`、`ghost_max=128`（均可环境变量覆盖，`ttl` 在 30s 上限内）。
4. **Boot jitter**：✅ **启动 warm 速率斜坡**（WorkingSet 内，`WADIST_BOOT_RAMP_MS` 门控，默认关）。
5. **liveness 缝**：✅ 新增 `LivenessConn` capability 接口，沿用 `PresenceConn`/`SessionSender` 的类型断言惯例，不污染 `Conn`。

## 4. 架构

### 4.1 Liveness 缝（`internal/cluster`）

```go
// types.go
type Liveness struct {
    Alive     bool // socket connected 且 logged-in（瞬时）
    Permanent bool // 曾收到 LoggedOut / StreamReplaced
}
type LivenessConn interface { Liveness() Liveness }
```

- `waConn`（`conn_whatsmeow.go`）实现：`Alive = client.IsConnected() && client.IsLoggedIn()`；`Permanent` 由事件处理器用原子标志置位。
- `fakeConn`（`session_test.go` / `orchestrator_test.go`）加 `alive`、`permanent` 字段，实现 `Liveness()`，供纯单测驱动分类逻辑。
- dwell（「not-alive 持续多久」）**不**放在 `waConn`——由 Reaper 自己维护时间戳映射，保持 `waConn` 只报瞬时状态。

### 4.2 夺回生命周期（`NewWAConn`，门控）

`NewWAConn` 增一个 `manageLifecycle bool` 参（由工厂闭包传入 `cfg.GhostReaper=="on"`）：

- **on**：`NewClient` 后置 `client.EnableAutoReconnect=false`；`AddEventHandler` 追加分派 `events.Disconnected`（记录可观测断开）、`events.LoggedOut` / `events.StreamReplaced`（原子置 `Permanent`）。
- **off**：保持 `EnableAutoReconnect=true`、仅现有 `events.Receipt` 处理器 = **今日逐字行为**。

事件处理器只更新 `waConn` 内的原子 liveness 状态，不直接触发回收（回收由 Reaper 统一按上限调度）。

### 4.3 Ghost Reaper 循环（`internal/node`，纯逻辑 + 回调注入）

仿 `internal/control/janitor.go` 的有界扫描模板，但为纯逻辑以便假时钟单测。依赖经回调注入：

```go
type GhostCandidate struct { JID string; Alive, Permanent bool }
type GhostReaperDeps struct {
    Now        func() time.Time
    Snapshot   func() []GhostCandidate          // 由 reg.Snapshot()+LivenessConn 断言构建
    Reap       func(ctx context.Context, jid string) error // reg.Remove + sess.Close（断连+释放 fence）
    RequestWarm func(jid string) error           // 瞬态：交回 WorkingSet 重新 warm
    MarkLoggedOut func(ctx context.Context, jid string) error // 永久：PG ban_status='logged_out'
    Metrics    interface{ IncGhostReaped(kind string); SetGhostInFlight(n int) }
}
```

每 `WADIST_GHOST_TICK_MS`（默认 5s）一 tick：

1. 取 `Snapshot()`，逐候选分类，并清理已不在快照中的 dwell 记录：
   - **Alive** → 删除该 JID dwell。
   - **Permanent** → 立即列为永久 ghost（无需 dwell）。
   - **not-alive 非永久** → 若无 dwell 记录则记 `Now()`；`Now()-dwell ≥ ghost_ttl` → 列为瞬态 ghost。
2. 汇总 ghost，本 tick 至多回收 `ghost_max` 个（`errgroup.SetLimit(ghost_max)` 并发封顶），超出留到下一 tick。
3. 每个 ghost：`Reap(ctx, jid)`（幂等，与 `guardSession` 正交；`Session.Close` 用 `closed` 布尔守卫，无双关竞争）；
   - 瞬态 → `RequestWarm(jid)`，`IncGhostReaped("transient")`；
   - 永久 → `MarkLoggedOut(ctx, jid)`（**不** re-warm），`IncGhostReaped("permanent")`。
4. `SetGhostInFlight(本 tick 回收数)`。

回收后 dwell 记录随之删除。纯逻辑（注入 `Now`/`Snapshot`/回调）→ 全路径可用假时钟单测，无需真 whatsmeow / 容器。

### 4.4 Boot warm 速率斜坡（`internal/control/workingset.go`，门控）

`WADIST_BOOT_RAMP_MS`（默认 0=关；建议 60000）。`WorkingSet` 记录 `bootStartMs`（首个 `Tick` 的 `nowMs`）。开启时，ACTIVE-warm 阶段的每-tick 目标从 0 线性放开：

```
effTarget = Target                                  当 ramp==0 或 elapsed≥ramp
effTarget = ceil(Target * elapsed / ramp)           当 0<elapsed<ramp
```

`elapsed = nowMs - bootStartMs`。active-warm 只补到 `effTarget`（而非直冲 `Target`），把重启后的重连洪峰摊到窗口内，窗口后满速。PASSIVE-warm（`warm:req`）与 EVICT 不受斜坡影响。复用 `Tick` 已有的 `nowMs` 入参 → 纯函数单测。

### 4.5 store 层新方法（`internal/store/node.go`）

```go
func (m *Manager) MarkAccountLoggedOut(ctx context.Context, jid string) error
// UPDATE account_devices SET ban_status='logged_out', ban_checked_at=now() WHERE account_jid=$1
```

复用**已有** `ban_status_t` enum 的 `'logged_out'` 值（`migrations/0001`）。`ListActiveAccounts` 已按 `ban_status='active'` 过滤，故置为 `logged_out` 即自动被 WorkingSet 播种/Reconciler 排除——**无需新迁移**。

## 5. 配置（`internal/config/config.go`，全部默认=旧行为）

| env | 默认 | 作用 |
|---|---|---|
| `WADIST_GHOST_REAPER` | `off` | 主门控：关自动重连 + 装生命周期事件处理器 + 跑 reaper |
| `WADIST_GHOST_TTL_MS` | `20000` | 静默 ghost 判定 dwell |
| `WADIST_GHOST_MAX` | `128` | 单 tick 回收并发上限 |
| `WADIST_GHOST_TICK_MS` | `5000` | reaper 扫描周期 |
| `WADIST_BOOT_RAMP_MS` | `0` | 启动 warm 斜坡窗口（0=关） |

沿用现有 `getenv`/`intEnv`/`msEnv` 惯例；`main.go` 中 `sup.Go(reaper.Run(...))` 与事件/关重连的工厂参均门控在 `cfg.GhostReaper=="on"`（仿 `RiskGovernor` 于 `main.go` 的接法）。

**零爆炸半径**：全部默认 off/0 → 自动重连仍开、仅 receipt 处理器、无 reaper goroutine、无斜坡 = 与当前生产完全一致。

## 6. 不变式与正交性

- **幂等回收**：`Session.Close` 幂等（`closed` 布尔 + `mu`）；Reaper 与 `guardSession`（查锁）可能同时命中同一 JID，一个胜出另一个 no-op，无双 Close/双释放。
- **所有权 fence**：Reap 经**活 `Session` 的锁句柄** `Close` 释放 fence（无 JID-only 释放路径）；`Close` 不清 PG `owner_node`，由既有 takeover 扫描器再收养——与今日 `guardSession` 一致。
- **权衡（记录在案）**：关自动重连后，<`ghost_ttl` 的瞬时抖动也要等 dwell 满才回收+重连，该账号在此窗口不可发。这是「夺回控制」的代价；如需更快恢复可调低 `ghost_ttl`。永久信号走 `Permanent` 立即回收，不受 dwell 影响。

## 7. 测试

- **Reaper 纯单测**（假时钟）：dwell<ttl 不杀 / dwell≥ttl 杀+rewarm / permanent 立即杀+MarkLoggedOut 且不 rewarm / 单 tick 超 `ghost_max` 溢出到下一 tick / alive 清 dwell / 快照移除的 JID 清 dwell。
- **Boot ramp 单测**：`nowMs` 步进穿越窗口，`effTarget` 线性放开、窗口后满 `Target`；ramp=0 时等于旧行为。
- **LivenessConn**：`fakeConn` liveness 断言路径（Alive/Permanent 组合）。
- **`MarkAccountLoggedOut` + 排除**（testcontainer PG）：置 `logged_out` 后 `ListActiveAccounts` 不返回该 JID。
- `conn_whatsmeow.go` 的事件/关重连无单测（无真 WhatsApp 设备 + 4G 代理，同该文件既有说明）；靠门控逻辑足够简单兜底。
- 全程 `make gate`（含 -race）绿。

## 8. 分模块顺序（逐 task，可回滚）

| Task | 交付 |
|---|---|
| 1 | `LivenessConn` 接口 + `waConn.Liveness()` + `fakeConn` liveness 字段 + 断言单测 |
| 2 | `NewWAConn` 门控生命周期接管（关重连 + 事件处理器置 `Permanent`/记断开） |
| 3 | `Manager.MarkAccountLoggedOut` + `ListActiveAccounts` 排除集成测 |
| 4 | Ghost Reaper 纯逻辑 + 全分类单测（假时钟、`ghost_max` 溢出、dwell 清理） |
| 5 | WorkingSet boot warm 斜坡 + 单测 |
| 6 | 配置字段 + `main.go` 门控接线（工厂参 + `sup.Go(reaper.Run)`）+ 指标 |
