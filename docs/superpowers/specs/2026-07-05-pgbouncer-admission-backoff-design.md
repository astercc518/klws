# PgBouncer 隔离 + admission 指数退避 设计

> 里程碑 M7（承接单机高密度重构 spec 第 7 节 Task 5「Lua 智能退避 + PgBouncer 隔离」）。
> 日期 2026-07-05。一个里程碑、两组正交 task。全程配置门控、默认=今日行为、**零爆炸半径**。

## 1. 背景与目标

单机 10 万号需把 PG 连接收敛到 PgBouncer transaction 模式后面。前置条件已由前序里程碑满足：M1 移除 whatsmeow 的 PG 会话（走 Badger），M2 提供 `redisOwnership`（fence token）替代 pg advisory 锁。剩余阻碍是当前代码两处与 txn 池化不兼容：① pgx 默认 `QueryExecModeCacheStatement`（跨事务命名 prepared stmt）；② `DeviceLock` 在 `lockPool` 连接上持 **session 级 advisory lock 跨查询**。

同时给 admission 加事件驱动的段级指数退避：某段收到 `wa_warning` 时快速加宽该段 min_gap，比 Risk Governor 的窗口 AIMD 更快压制正在恶化的段。

**目标**：(A) 存储层兼容 PgBouncer txn 模式；(B) admission 段级指数退避。二者无代码耦合。

## 2. 已锁决策（2026-07-05 brainstorm）

1. **范围**：合为一个里程碑 M7，一个 plan 内分 A/B 两组 task。
2. **池模式门控**：新增 `WADIST_PG_POOL_MODE=direct|pgbouncer`（默认 direct=今日行为）。pgbouncer 模式跳过 lockPool、**fail-fast 强制 `OwnershipBackend=redis`**、设 QueryExecMode、钳 MaxConns≤200。
3. **pgx 协议**：`WADIST_PG_QUERY_MODE=exec|simple`（默认 exec，仅 pgbouncer 模式生效）。
4. **退避强度**：×2/warning、封顶 8×、TTL 300s 每次重置；键 `backoff:cc:{cc}` + `backoff:net:{/24}`，admission 取两者 max 乘数放大 min_gap。
5. **与 Risk Governor 正交**：退避只在 admission 加宽 min_gap；governor 仍走窗口封号率 AIMD（M4/M5）。不新增耦合。
6. **验收边界**：本机无真 PgBouncer → 测 simple/exec 协议查询正确性 + redis 版 takeover chaos 平价（`chaos_redis_test.go` 已有）；`docker-compose.adopt.yml` 加 PgBouncer 服务（标注未集成测、上线翻开）。

## 3. A 组：PgBouncer txn 模式兼容（`internal/store`）

### 3.1 配置（`internal/store/config.go` + `internal/config/config.go`）

`store.Config` 新增：
```go
PoolMode  string // "direct" (default) | "pgbouncer"
QueryMode string // "exec" (default) | "simple"; only honored when PoolMode=="pgbouncer"
```
`withDefaults()`：`PoolMode` 空→"direct"，`QueryMode` 空→"exec"。**校验**（`withDefaults` 或 `NewManager` 入口）：`PoolMode=="pgbouncer"` 时若 `OwnershipBackend != "redis"` → 返回错误 `pgbouncer mode requires WADIST_OWNERSHIP_BACKEND=redis`（fail-fast，拒绝启动）；且 `MaxOpenConns > 200` → 钳到 200 并 log 告警。

`config.Config`（进程层）加 `PgPoolMode`、`PgQueryMode` 字段，`Load()` 读 `WADIST_PG_POOL_MODE`(default "direct")、`WADIST_PG_QUERY_MODE`(default "exec")，`Store()` 投影进 `store.Config`。

### 3.2 池构造（`internal/store/manager.go`）

抽一个 helper 把 QueryExecMode 施加到 `*pgxpool.Config`：
```go
func applyQueryMode(poolCfg *pgxpool.Config, poolMode, queryMode string) {
    if poolMode != "pgbouncer" { return } // direct: leave pgx default (CacheStatement) — 今日行为
    switch queryMode {
    case "simple":
        poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
    default: // "exec"
        poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
    }
}
```
对 bizPool / tenantPool / systemPool 三个池的 `ParseConfig` 结果各调一次。

**lockPool**：pgbouncer 模式**不创建**（`OwnershipBackend=="pg"|"shadow"` 才创建，且这两者在 pgbouncer 模式已被 §3.1 校验拒绝）——具体门控为 `if cfg.PoolMode != "pgbouncer" { 创建 lockPool }`，`m.lockPool` 在 pgbouncer 模式为 nil。redis 所有权从不调用 `AcquireDeviceLock`，故 nil 安全；防御性地在 `AcquireDeviceLock` 开头 `if m.lockPool == nil { return error }`。direct 模式 lockPool 照旧创建 → 零爆炸半径。

### 3.3 部署（`docker-compose.adopt.yml`）

加 `pgbouncer` 服务：`edoburu/pgbouncer` 或等价镜像，`POOL_MODE=transaction`、`MAX_CLIENT_CONN`、`DEFAULT_POOL_SIZE`，指向 `postgres` 服务；app 的 `WADIST_POSTGRES_DSN` 指向 pgbouncer:6432。**标注：本机未集成测，上线随 two-file（prod.yml + adopt.yml）翻开**。app 侧须同时置 `WADIST_PG_POOL_MODE=pgbouncer`、`WADIST_OWNERSHIP_BACKEND=redis`。

### 3.4 A 组测试

- **单测**（无容器）：`applyQueryMode` 在 direct 不改 mode、pgbouncer+exec→Exec、pgbouncer+simple→SimpleProtocol；`withDefaults`/`NewManager` 校验 pgbouncer+非 redis→err、MaxConns>200→钳位。
- **集成测**（testcontainer PG，无需真 PgBouncer——simple/exec 协议对普通 PG 也生效）：构 `PoolMode=pgbouncer, QueryMode=simple` 的 Manager，跑一组**高风险查询**（数组参数、enum cast、jsonb、`$n::interval`/`::type` 显式转换的代表性语句：billing Hold、campaign 写入/查询、account_devices CRUD、proxy 绑定）证明 simple 协议下无隐式转换崩。再对 `QueryMode=exec` 重复关键子集。
- redis 版 takeover chaos 平价：`chaos_redis_test.go` 已覆盖防双开 fence，确认在 pgbouncer 模式（redis 所有权）下仍绿。

## 4. B 组：admission 段级指数退避（`internal/sendgate`）

### 4.1 退避键写入（`Admission.RecordWarning`）

`Admission` 新增：
```go
func (a *Admission) RecordWarning(ctx context.Context, segKeys ...string) error
```
对每个段键（调用方传 `backoff:cc:{cc}`、`backoff:net:{/24}`）执行原子小 Lua：读当前乘数（缺省 1），`mult = min(BACKOFF_MAX, mult*BACKOFF_FACTOR)`，`SET seg mult EX BACKOFF_TTL`（每次刷新 TTL）。TTL 到期整键消失 → 回 1×。参数由 `Admission` 持有（`NewAdmission(rdb, BackoffParams{On, Factor, Max, TTLms})`）。

调用点：wa_warning 处理路径（与 `SendGate.ApplyHealthSignal` 并列），由持有段上下文的调用方解析 cc 与账号绑定代理的 /24 后调 `RecordWarning`。`ApplyHealthSignal`（PG health−30）保持不变；退避（redis）独立。`BackoffParams.On==false` 时 `RecordWarning` 直接返回 nil（不写键）。

### 4.2 admission 读退避放大 min_gap（`admitLua`）

`NewAdmission` 按 `BackoffParams.On` 选脚本：
- **off**：现有 `admitLua`（2 KEYS）——**逐字今日行为**。
- **on**：`admitLuaBackoff`（4 KEYS：day, pace, backoff:cc, backoff:net）。在现日配额/pacing 判定前计算 `mult = max(1, tonumber(GET KEYS[3]) or 1, tonumber(GET KEYS[4]) or 1)`，`eff_gap = ARGV[2] * mult`，pacing 用 `eff_gap` 替 `ARGV[2]`。其余逻辑不变。

`Admit` 签名扩展为 `Admit(ctx, jid, quota, minGap, now, ccKey, netKey string)`；off 模式忽略 cc/net（脚本只引 KEYS[1,2]），调用方可传空。段键不存在 → GET nil → mult 1 → min_gap 不变 → 零爆炸半径。

### 4.3 B 组测试

- `RecordWarning` 单测：连续 warning → 乘数 1→2→4→8→8（封顶）；TTL 到期后回 1；多段各自独立；On=false 不写键。
- `admitLuaBackoff` 单测：无退避键时 min_gap 与旧脚本一致（放行时序相同）；cc=4×、net=2× → 取 max 4× 放大 min_gap（pacing 更严）；配额/幂等逻辑不受影响。
- off 模式：Admit 走旧脚本，现有 admission 测试逐字通过。

## 5. 配置汇总（全部默认=旧行为）

| env | 默认 | 作用 |
|---|---|---|
| `WADIST_PG_POOL_MODE` | `direct` | direct=今日；pgbouncer=设 QueryExecMode+删 lockPool+强制 redis 所有权+钳 MaxConns |
| `WADIST_PG_QUERY_MODE` | `exec` | pgbouncer 模式下 pgx 协议：exec / simple |
| `WADIST_BACKOFF_ON` | `off` | admission 段级指数退避开关 |
| `WADIST_BACKOFF_FACTOR` | `2` | 每 warning 乘数翻倍因子 |
| `WADIST_BACKOFF_MAX` | `8` | 乘数上限 |
| `WADIST_BACKOFF_TTL_MS` | `300000` | 退避键衰减 TTL |

## 6. 正确性不变式（保留）

- **防双开**：pgbouncer 模式下所有权走 redis fence（`chaos_redis_test.go` 平价证明任一 JID 至多一条活跃会话）。
- **计费**：`balance=Σledger`、`frozen=Σ未结charge`、`message_id` 幂等——simple/exec 协议不改 SQL 语义，保留 billing 测试。
- **发送幂等**：`message_id` 全链路、`sent_today` 对称记账——退避只改 pacing 时序，不碰记账。
- 全程 `make gate`（含 -race、指标高基数门禁、govulncheck）绿。

## 7. 分组 task 顺序（逐 task，可回滚）

| Task | 组 | 交付 |
|---|---|---|
| 1 | A | `store.Config`/`config.Config` 加 PoolMode/QueryMode 字段 + `withDefaults` 校验（pgbouncer→强制 redis、钳 MaxConns）+ 单测 |
| 2 | A | `applyQueryMode` helper + Manager 池构造按模式设 QueryExecMode、pgbouncer 跳过 lockPool、`AcquireDeviceLock` nil 守卫 + 单测 |
| 3 | A | 高风险查询 simple/exec 协议集成测（构 Manager pgbouncer 模式跑代表性查询） |
| 4 | B | `Admission.RecordWarning` + 退避小 Lua（×factor/cap/TTL）+ 单测 |
| 5 | B | `admitLuaBackoff` 脚本 + `NewAdmission` 按 On 选脚本 + `Admit` 扩 cc/net 键 + 单测 |
| 6 | — | config 接线（main.go 传 PoolMode/QueryMode/Backoff*；wa_warning 路径调 RecordWarning）+ `docker-compose.adopt.yml` PgBouncer 服务 |
