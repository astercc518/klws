# 单一高密度配置 — 删除 legacy 双栈（Single-Config High-Density Collapse）

**日期**: 2026-07-09
**分支**: `feat/mvp-business-loop`（spec 落库）→ 实现另起分支
**状态**: 设计已确认，待实现

---

## 1. 背景与动机

wadist 的立项主需求就是**单机日发百万**。M1–M7 高density重构已把整套能力建成并合入 main，但每一层都做成了 **env 门控、默认走 legacy 路径**（`asynq`/`pg`/`off`/`direct`）——这是当初为「灰度上线不惊动线上」搭的迁移脚手架，不是终态。

维持两套配置的代价：

- 认知负担：读代码要同时想「pg 分支 / redis 分支」两种世界。
- 维护面翻倍：每个改动要照顾两条路径。
- 错觉：默认配置扛不到十万账号量级（`MaxLockConns=300` 卡死并发会话），却因为「代码在 main 里」而产生「已支持百万」的错觉。

**决策**：删掉 legacy 双栈，高density成为唯一路径，flag 全部移除。终态 = 一条路、零门控 flag、代码干净。

## 2. 目标 / 非目标

**目标**
- 移除全部 legacy 发送/所有权/会话/代理路径及其门控 flag。
- 高density栈（pump + redis 所有权 + BadgerDB + redis 代理冷却）成为**唯一**且**默认即生效**的路径。
- 反封控制环固化为常驻能力（非可关 feature）。

**非目标 / 明确不做**
- 不写 sqlstore→badger 迁移工具（决策：**接受账号全量重扫码**，见 §5）。
- 不引入 PgBouncer 作为唯一池化层（决策：删除，见 §4a）。
- 不做多机 reconcile、国家分片、per-ASN 段级、变量短链（既有 backlog，超出本次范围）。
- 不改动标定 ramp 的运营流程（上线后运营做，不阻塞代码合并）。

## 3. 必须存活的正确性不变式（红线擦除下的护栏）

[高density重构记忆] 已显式解除五大引擎（billing/dispatch/store/sendgate/cluster）禁改约束，允许重写内部。但以下不变式在删除过程中**必须继续成立**，由 `make gate` 回归守护：

1. **计费**：`balance = Σledger`、`frozen = Σ未结charge`。
2. **幂等**：`message_id` 幂等去重。
3. **对称记账**：`sent_today` 加减对称，不漏计/不重复计。
4. **防双开**：单账号单会话，fence token 保证唯一所有权。
5. **门禁**：`make gate` 全绿（含 `TestTakeoverChaos_Redis` 混沌测试）。

## 4. 精确删除清单（符号级，已核对当前代码）

| 层 | 删除 | 独留（唯一路径） |
|---|---|---|
| **发送** | `internal/dispatch/asynqadapter.go`（`AsynqEnqueuer`/`RegisterSendHandler`/`TypeSend`）、main.go 的 asynq **发送**分支、`WADIST_DISPATCH_MODE`、`WADIST_ASYNQ_CONCURRENCY` env | pump（`internal/dispatch/pump.go`）；`SendRate`/`SendWorkers`/`PumpBuffer` 保留为**数值调参**。**注意：asynq 库不整删**——takeover 队列（`internal/node/takeover.go`）仍跑在 asynq 上，`asynqSrv`/`mux`/`asynqClient` 因 takeover 保留，其 Concurrency 改用常量 |
| **所有权** | `internal/store/lock.go`、`internal/store/ownership_pg.go`、shadow 后端及其 divergence 回调、`MaxLockConns`/lockPool/`acquirePGLock`、`WADIST_OWNERSHIP_BACKEND`、`owner_node` 列（DROP 迁移）| `internal/store/ownership_redis.go` + `ownership.go` 接口 + fence token |
| **会话存储** | `internal/store/manager.go` 里 sqlstore 分支与 `sqlstore` import、`WADIST_SESSION_STORE`；whatsmeow 16 张 PG session 表停用（保留或后续 DROP，不阻塞）| `internal/store/wabadger`（`WADIST_BADGER_DIR` 保留为路径配置，须挂 NVMe 持久卷）|
| **代理** | pg proxy 分配分支（`FOR UPDATE SKIP LOCKED` 路径）、`WADIST_PROXY_BACKEND` | `internal/store/proxy_redis.go`（ZSET 冷却圈）；`PROXY_COOLDOWN_MS` 保留；**PG `chk_bindings` 原子兜底保留**（防超配的 durable 约束）|
| **池化** | `WADIST_PG_POOL_MODE`/`WADIST_PG_QUERY_MODE`、`docker-compose.pgbouncer.yml`、pgbouncer 相关 validate 分支 | direct pgxpool（见 §4a）|
| **控制环门控** | `WADIST_RISK_GOVERNOR`/`WADIST_SEGMENT_GOVERNOR`/`WADIST_GHOST_REAPER`/`WADIST_BACKOFF_ON` 四个**布尔主门控** | 控制环常驻；数值调参（`GOV_*`/`SEG_*`/`GHOST_*`/`BACKOFF_*`）保留（见 §4b）|
| **崩溃恢复** | —（**不得删**）| `internal/dispatch/reclaim.go` 的 `ReclaimOrphanedAssignments` + PG `recipients.state='pending'` 重扫 + `batch.go` 的 SKIP LOCKED —— 这是删除 asynq durable 队列后**唯一**的持久恢复机制 |

### 4a. 边界决策：删除 PgBouncer，只留 direct 池

**决策**：删除 PgBouncer 支持，唯一池化策略为 direct pgxpool。

**理由**：
- redis 所有权后不再有 per-session advisory 锁 → 业务连接被 `MaxOpenConns` 钳住，direct pgxpool 完全够单机使用。
- PgBouncer 在本机[无真实服务、未做集成测]（`docker-compose.pgbouncer.yml` 头注释自述）；保留它=独留一个未验证的池化层作唯一路径，违背「删未验证、留已验证」原则。
- 多机/连接爆炸场景再引入（YAGNI）。

### 4b. 边界决策：控制环固化为常驻，数值调参保留

**决策**：删除 Governor / SegmentGovernor / GhostReaper / Backoff 的**布尔主门控**，四者常驻生效；`AntiFP` 同样固化为常开。保留全部**数值 env**（SLO / rate / factor / TTL / window / sample 等）。

**理由**：
- 高density即产品，反封控制环是产品的一部分，不该是可关 feature。
- 数值旋钮在标定 ramp（§6）里必须可调；**保留数值 env ≠ 两套配置**，是运行期调参。

## 5. 硬依赖升级与迁移

- **Redis 从可选变必须**：所有权 fence + 代理冷却是热真相层。无 redis → 启动直接 fail-fast。Redis 须开 **AOF**（崩溃不能丢 fence/冷却状态）。
- **Badger 目录须挂持久卷**进 wadist 容器（`docker-compose.worker.yml` 现未挂载，需补 `wadist_badger:/var/lib/wadist/badger`）。
- **PG 退成纯 durable 真相源**：账号、计费、campaign、recipients；不再存 Signal 会话。
- **会话迁移 = 全量重扫码**（决策）：切 badger 后现有账号登录态不迁移，账号重新扫码登录。仅在当前池可重建/基本为 demo 时可接受——已确认可接受。
- **owner_node DROP 迁移 + 管理后台改读 Redis**（决策，修订自初稿）：owner_node 不再是死镜像——管理后台账号列表/「已归属」筛选/归属计数在读它（`internal/api/admin_api.go` 账号列表 SELECT、`internal/api/resources_query.go` 的 `owner_node IS NOT NULL/NULL` 筛选、stats FILTER）。本次**执行 DROP 迁移**，同时把这三处 admin 读取**改为从 Redis `owner:{jid}` 查真实所有权**：新增 Manager 方法枚举/批查 redis 归属，账号列表按页 MGET 注解 OwnerNode/Online，「已归属/未归属」筛选与计数按 redis 归属的 jid 集过滤。删 pg 所有权后端时随之删除的是**读 owner_node 做所有权判定**的 `staleOwnedAccountsPG`/`listUnownedActiveAccountsPG`；orchestrator 的 `ClaimAccount` 对 owner_node 的写入也随列 DROP 移除（保留 last_connected_at 写入）。

## 6. 验证策略（无 fallback，安全底线）

1. **`make gate` 全绿**：§3 四条不变式的回归测试必须继续通过，尤其 `sent_today` 对称、billing 恒等、`message_id` 幂等、`TestTakeoverChaos_Redis` 混沌接管。
2. **真机首发端到端**：账号重扫码 → 发一条真实消息走通闭环（pump → redis 所有权 → badger 会话 → PG 记账），确认单条闭环正确。
3. **标定 ramp**（不阻塞代码合并，上线后运营做）：`SEND_RATE` 从 17/s（≈1K/min）逐级抬到 167/s（≈10K/min），监控 `FleetBanRate < GOV_SLO`，探到不招封的单机吞吐天花板。

## 7. 执行方式

删除面横跨五大引擎包 + `cmd/wadist` 装配，属大重构。走 **spec → plan → subagent 驱动 TDD**（与 M1–M7 同套路）：每 task 实现 + 两阶段复核 + 修复循环，全程 `make gate` 绿。一次性删完（单分支，非分模块灰度）。

**风险自陈**：本次删除的是「已验证代码」（asynq durable 队列、PG advisory + `TestTakeoverChaos`），独留的是「未在真机满负载跑过」的高density路径，且删后无内置 fallback。此风险已知并接受，理由是：日发百万即项目立身之本，双栈非终态；且当前池可重建（接受重扫码），删除爆炸半径小。缓解手段为 §6 的 make gate + 真机首发闭环验证。

---

相关记忆：`klws-highdensity-refactor`（M1–M7 各模块门控与实现）、`klws-dispatch-throughput-design`（吞吐/防封数学）、`klws-deployment-topology`（compose 部署铁律）、`klws-redlines`（红线解除范围）。
