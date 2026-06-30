# Redis 租约所有权(方案 B2)实现设计

- 日期：2026-06-30
- 状态：实现设计草案，待评审
- 关联：[2026-06-30-高吞吐发送编排-design.md](2026-06-30-高吞吐发送编排-design.md) 第 5 节并发瓶颈的根因解
- 目标：用 Redis 租约 + fencing 取代 PG advisory 锁，拆掉 `MaxLockConns` 并发天花板（~2000 → ~1.5 万），让单机在 Cruise 安全姿态下达 10K/分钟。
- 红线：改动**确实落在 `internal/store` + `internal/node`**（用户已选 B 并接受）。核心遏制点：经已存在的 `cluster.DeviceLockHandle` 接口缝隔离，`cluster`/`dispatch`/`sendgate`/`billing` **零改动**。

---

## 1. 范围与不改清单

**改（红线）：**
- `internal/store`：新增所有权策略层；`AcquireDeviceLock`/`UpsertNodeHeartbeat`/`StaleOwnedAccounts`/`ListUnownedActiveAccounts`/`DeregisterNode` 委托给可插拔后端。新增 Redis 实现。删除 `lockPool`/`MaxLockConns`（redis 后端下）。
- `internal/node`：无逻辑改动（消费接口），仅 chaos 测试新增 Redis 平价用例。

**不改（零改动）：**
- ✅ `cluster` 整包（`DeviceLockHandle` 接口、`guardSession`、`Registry`、`Supervisor`）
- ✅ `dispatch` / `sendgate` / `billing`（幂等/计费/限速/防封事务）
- ✅ `node` 的 `StartAccountWithLock`/`guardSession`/`RunHeartbeat`/`RunTakeoverScanner` 结构
- ✅ cmd/wadist 的 factory 闭包（lock 透传）

---

## 2. 接口缝与改动边界（精确）

现有缝（已验证）：

```go
// cluster/types.go —— 不动
type DeviceLockHandle interface {
    Healthy(ctx context.Context) bool
    Release(ctx context.Context)
}
// orchestrator.go: lock, err := o.mgr.AcquireDeviceLock(ctx, jid); 透传给 factory(接口)；
//                  factory 失败时 lock.Release(ctx)；guardSession 调 sess.Healthy()
```

只要 `AcquireDeviceLock` 返回的句柄满足 `DeviceLockHandle`，上层全不动。

### 2.1 新增 `store.Ownership` 策略接口

```go
// internal/store/ownership.go (新增)
type LockHandle interface {            // 兼容 cluster.DeviceLockHandle
    Healthy(ctx context.Context) bool
    Release(ctx context.Context)
}

type Ownership interface {
    Acquire(ctx context.Context, jid, nodeID string) (LockHandle, error) // 失败 ErrDeviceLocked
    Heartbeat(ctx context.Context, nodeID string) error
    Deregister(ctx context.Context, nodeID string) error
    StaleOrUnowned(ctx context.Context, staleness time.Duration) ([]string, error)
}
```

`Manager` 持有 `ownership Ownership` 字段；现有方法改为薄委托：

```go
func (m *Manager) AcquireDeviceLock(ctx, jid string) (LockHandle, error) {
    return m.ownership.Acquire(ctx, jid, m.nodeID)
}
```

后端由 `cfg.OwnershipBackend ∈ {pg, redis, shadow}` 选择：
- `pgOwnership`：**现有代码逐字搬入**（advisory 锁 + cluster_nodes + owner_node）。backend=pg 时行为与现状字节一致 → 现有测试全绿。
- `redisOwnership`：本 spec 新增。
- `shadowOwnership`：包装 pg(权威)+redis(影子)，比对决策、打分歧指标。

> **owner_node 彻底移除（已定）**：终态不保留 `account_devices.owner_node`。但**移除是 cutover 后的最后一步迁移**——shadow/canary 阶段 `pgOwnership` 仍依赖它做权威比对，故 owner_node + `cluster_nodes` 表保留到 redis 成为唯一权威且稳定后，再用一支迁移 `DROP COLUMN owner_node` + `DROP TABLE cluster_nodes`。移除后所有权可观测改由读 Redis `owner:{jid}` 的 admin/metrics 端点提供（不再依赖 PG 列）。现有 chaos 断言相应改为断言 Redis owner（见第 12 节）。

---

## 3. Redis Key Schema

```
owner:{jid}        = "nodeID:fence"     无 TTL，所有权台账
node:hb:{nodeID}   = "1"  PX = hbTTL    唯一带 TTL；N 个节点，无续租风暴
owned:{nodeID}     = SET<jid>           节点持有集，O(1) 列举接管候选
fence:{jid}        = INCR 计数器         单调 fencing token
```

内存：10 万 owner key ≈ 12MB + owned 集 + fence 计数 ≈ 30–50MB。

---

## 4. 三个原子 Lua（全文）

**Acquire** `KEYS=[owner:{jid}, fence:{jid}, owned:{me}]  ARGV=[me, hbPrefix]`
```lua
local cur = redis.call('GET', KEYS[1])
if cur then
  local node = string.match(cur, "^(.-):")
  if redis.call('EXISTS', ARGV[2]..node) == 1 then
    return {0, cur}                         -- 活节点持有 → 拒（ErrDeviceLocked）
  end
end
local fence = redis.call('INCR', KEYS[2])   -- 死节点/无主 → 抢占
redis.call('SET', KEYS[1], ARGV[1]..':'..fence)
redis.call('SADD', KEYS[3], string.match(KEYS[1], "owner:(.+)"))
return {1, fence}
```

**StillOwner**（guardSession/`Healthy` 调）`KEYS=[owner:{jid}, node:hb:{me}]  ARGV=[me, fence]`
```lua
if redis.call('GET', KEYS[1]) ~= ARGV[1]..':'..ARGV[2] then return 0 end -- 被抢/fence 变
if redis.call('EXISTS', KEYS[2]) == 0 then return 0 end                  -- 自身 hb 死 → fail-closed
return 1
```

**Release** `KEYS=[owner:{jid}, owned:{me}]  ARGV=[me, fence, jid]`
```lua
if redis.call('GET', KEYS[1]) == ARGV[1]..':'..ARGV[2] then
  redis.call('DEL', KEYS[1]); redis.call('SREM', KEYS[2], ARGV[3])
end
return 1
```

**StaleOrUnowned**（接管扫描）：`SMEMBERS owned:{node}` ∀ `node:hb` 已死的 node ∪ owner 指向不存在 hb 的；用 Lua 或 pipeline 批扫。返回 jid 列表喂现有 `RunTakeoverScanner`（asynq.Unique 去重不变）。

---

## 5. Redis 句柄实现

```go
// internal/store/ownership_redis.go (新增)
type redisLockHandle struct {
    rdb   *goredis.Client
    jid, nodeID string
    fence int64
}
func (h *redisLockHandle) Healthy(ctx) bool { /* StillOwner Lua → ==1 ；redis 错误 → false(fail-closed) */ }
func (h *redisLockHandle) Release(ctx)      { /* Release Lua（best-effort）*/ }
var _ cluster.DeviceLockHandle = (*redisLockHandle)(nil)
```

`StartAccountWithLock`/`guardSession`/factory **一行不改**。

---

## 6. 心跳 / 接管的 Redis 化（对照现有，结构不变）

| 现状（PG） | B2（Redis） | 调用方变化 |
|---|---|---|
| `UpsertNodeHeartbeat`→cluster_nodes | `SET node:hb:{me} 1 PX hbTTL` | RunHeartbeat 不变 |
| `StaleOwnedAccounts`+`ListUnownedActiveAccounts` | `StaleOrUnowned`（扫死 hb 的 owned 集） | scanOnce 合并为一调 |
| `DeregisterNode`（清 owner_node + 删行） | `DEL node:hb:{me}`；惰性清 owned；owner_node 镜像照清 | AfterSessions 不变 |
| advisory 锁瞬时释放 | hb TTL 过期（~staleness） | failover 仍受 staleness 限——**无回归**（现状 takeover 本就 heartbeat-bound）|

TTL 关系：`hbTTL = NodeStaleness`，`HeartbeatInterval = hbTTL/3`，`guardInterval`/`TakeoverScanInterval` 复用现有配置。

---

## 7. Fencing：四重防线 + 可选 per-send 校验

GC 暂停隐患（owner 卡顿→租约过期→被抢→醒来误发）的防线：

1. **fence token**：醒来 `StillOwner` 见 owner 已变 → 返回 0 → guardSession 在 ≤guardInterval(10s) 内断连。
2. **WhatsApp 单设备连接**：新 owner 一连，WhatsApp 踢旧 socket → 旧会话自然死（外部仲裁）。
3. **message_id 幂等**：`campaignID:recipientID` + worker state 守卫 → 同消息不双发。
4. **billing Hold `ON CONFLICT(message_id)`** → 不双扣。

防线 2/3/4 已独立挡住最坏后果，故 fencing 负担轻。**`OWNERSHIP_FENCE_ON_SEND` 默认开（已定）**：`RoutingSender.Send` 在真正 `SendMessage` 前先调一次句柄 `Healthy()`（redis 后端 = StillOwner Lua），失去所有权则不发、走 requeue（+1 Redis RTT/send，167/s 可忽略），避免从"即将被 WhatsApp 踢"的会话发信（防封卫生）。

> 落点说明：该校验在 `cluster/sender.go` 的 `RoutingSender.Send` 内（用户已授权编辑 `cluster`），调用 `session` 持有的 `DeviceLockHandle.Healthy()` —— 与反指纹 spec（presence/typing）同处一个 RoutingSender 改动，见 [2026-06-30-cluster-antifingerprint-impl-design.md](2026-06-30-cluster-antifingerprint-impl-design.md)。

---

## 8. 配置项

| env | 默认 | 含义 |
|---|---|---|
| `WADIST_OWNERSHIP_BACKEND` | `pg` | `pg`/`redis`/`shadow` |
| `WADIST_MAX_LOCK_CONNS` | 300 | 仅 pg 后端有意义；redis 下忽略 |
| `WADIST_OWNERSHIP_FENCE_ON_SEND` | **true** | 发送前 fencing 校验（已定默认开）|
| Redis（compose） | — | **新增 `--appendonly yes --appendfsync everysec`**，`--maxmemory` 512mb→2gb |

---

## 9. 容量 / ops / 内存

- **并发新天花板** = whatsmeow 客户端内存（~1.5–2MB/个）→ 128G 现实 **~1.2 万–2 万并发**；PG `max_connections=700` 仅剩 bizPool+sqlstore+RLS(~150)，海量余量。
- **Cruise（T_hold=90s）**：τ = 15000/90 ≈ **167/s = 10000/分钟（最安全姿态）**。
- **Redis ops**：心跳 1 个/(hbTTL/3)；guardSession StillOwner 1.5 万账号/10s ≈ 1500 ops/s（可批成每拍一条 Lua → ~0.1 ops/s/节点）；Redis 余量充足（10 万+ ops/s）。
- **Redis 内存** ≈ 30–50MB + asynq/sendgate 现有，2GB 上限充裕。

---

## 10. 故障与边界

| 风险 | 对策 |
|---|---|
| Redis 成所有权 SPOF | Redis 本就 mandatory（asynq+sendgate）+ noeviction；新增 **AOF everysec**：重启 owner 存活、hb 过期→短暂接管自愈；真丢数据→全体重抢（幂等挡双扣）|
| 时钟偏移 | 仅用 Redis 单时钟（hb 的 PX），无跨节点比较 |
| GC 暂停双连接 | 窗口 ≤ guardInterval；fence + WhatsApp 单连接 + 幂等三重兜底 |
| 网络分区（多节点） | 少数派 hb 过期→多数派接管；少数派 StillOwner 读 Redis 失败→**fail-closed 断连**（镜像现状 Healthy 出错→false）|
| 单机（当前） | 无分区无竞态；纯收益=拆 PG 连接天花板；B2 为多机/国家分片预留 |

---

## 11. 灰度迁移（最敏感机制，强制 shadow）

1. **接口实现 + 开关**，redis 句柄满足 `DeviceLockHandle`。
2. **Shadow 双跑**（backend=shadow）：pg 权威 + redis 影子，逐账号比对 Acquire/Healthy 决策，打 `ownership_shadow_divergence_total{op}` 指标。N 小时零分歧 → 切权威。
3. **Canary**：`canary.InCohort` 切 X% 账号到 redis，盯双发指标 + 封号率。
4. **Cutover**（backend=redis），pg 路径留回滚。

---

## 12. 测试与验收门槛

- **现有 `TestTakeoverChaos`(pg) 保持绿**（backend=pg 行为字节不变）。
- **新增 `TestTakeoverChaos_Redis`**：node-A Redis Acquire+hb → `ExpireNodeForTest`(DEL node:hb:node-A) 模拟死亡 → node-B scanOnce 发现 → StartAccountWithLock CAS 抢占 → 断言 **Redis `owner:{jid}==node-B:fence`** 且 registry 有 session（owner_node 列移除后不再断言 PG 列）。`-count=5`。
- **新增 `TestAcquireRace`**：2 节点并发 Acquire 同 jid → 恰一个成功（无双 owner）。
- **新增 `TestFencingGCPause`**：node-A 持有→删 hb→node-B Acquire→断言 node-A `StillOwner`==false（醒来不发）。
- **Shadow parity 测试**：同序列操作下 pg 与 redis 决策一致。
- `make gate` / `make chaos` / `-race` 全绿。

---

## 13. 红线影响评估

- 改 `internal/store`：新增 ownership 策略层 + Redis 实现；现有 pg 逻辑**逐字保留**为 `pgOwnership`，行为不变。**未改计费/防封/RLS 事务**。
- `internal/node`：仅测试新增，逻辑零改。
- `cluster`/`dispatch`/`sendgate`/`billing`：零改动。
- 结论：红线爆炸半径 = store 锁实现 + 心跳/接管查询，全部经 `DeviceLockHandle`/`Ownership` 接口隔离；advisory 锁从"扛 10 万级所有权的错配角色"卸下。

---

## 14. 不做（YAGNI）/ 待决策 / 风险

**不做**：多节点 send 路由（per-node 队列）——单机不需要，留国家分片时做。

**已决策（2026-06-30）**：
1. ✅ owner_node 列**彻底移除**——cutover 后最后一支迁移 `DROP COLUMN owner_node` + `DROP TABLE cluster_nodes`（vestigial）；shadow/canary 期间保留供 pg 权威比对；可观测改读 Redis `owner:{jid}`。
2. ✅ `FENCE_ON_SEND` **默认开**——落点 `cluster/sender.go` RoutingSender，见反指纹 spec。

**风险**：所有权是系统最安全敏感的机制；缓解=shadow 双跑 + chaos parity 硬门槛 + pg 回滚路径常驻。

---

## 15. 验收标准

- backend=redis 下并发不再受 `MaxLockConns` 限，单机可达 ~1.5 万常驻会话。
- 全部 chaos/race/fencing/parity 测试 `-count=5` 绿；`make gate` 绿。
- shadow N 小时零分歧后方可 cutover。
- 全程未改 `cluster`/`dispatch`/`sendgate`/`billing` 及计费/防封事务。
