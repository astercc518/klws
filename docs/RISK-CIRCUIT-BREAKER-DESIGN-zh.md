# 设计文档：封号率自动熔断（Ban-Rate Circuit Breaker）引擎接入

> 状态：**待评审 / 待授权**（涉及红线包，需用户明确授权后方可实施）
> 关联：`system_risk_config` 控制面（已落地，见 `migrations/0014_risk_config.sql`、`internal/api/admin_api.go` 的 `handleAdmin{Get,Update}RiskConfig`）
> 作者：架构助手　日期：2026-06-29

---

## 0. TL;DR

- 目标：当某个群发任务（campaign）的**封号率**超过管理员在「风控策略中心」配置的阈值 `ban_rate_circuit_breaker` 时，系统**自动把该任务挂起**（`campaigns.state → 'paused'`），并审计、告警。
- 核心结论：**这件事可以几乎不碰红线引擎包完成。** 熔断动作复用 dispatcher 已经尊重的 `paused` 语义；封号率可纯用 SQL 在现有表上算出。
- 推荐架构：新增一个独立包 `internal/riskbreaker`（**全新代码，不属红线五包**），在 `cmd/wadist/main.go`（装配层，非红线包）里起一个独立 goroutine 周期性评估 + 熔断。`internal/dispatch`、`internal/sendgate`、`internal/store` **一行不改**。
- 唯一需要的"引擎侧"动作是在 `cmd/wadist` 装配处加几行启动代码（assembly，不是改引擎逻辑）。

---

## 1. 背景与现状（取证依据）

### 1.1 已有的事实
- **campaign 状态机**（`migrations/0006_dispatch.sql:4`）：
  `campaign_state_t = ('draft','running','paused','completed','failed')`。
- **dispatcher 只处理 running**（`internal/dispatch/scheduler.go:13-41`）：
  ```sql
  SELECT id FROM campaigns WHERE state='running' ORDER BY id
  ```
  每秒 tick 一次（`cmd/wadist/main.go:208-222`），每 campaign 每 tick 派发最多 100 条。
- **挂起 = 改状态**（`internal/api/admin_api.go:770-787`，`handleAdminStopCampaign`）：
  ```sql
  UPDATE campaigns SET state='paused' WHERE id=$1 AND state='running'
  ```
  走 `SystemPool()`。一旦改成 `paused`，dispatcher 下一 tick 自然不再派发——**这正是熔断需要的"挂起任务"动作，已存在且经过验证。**
- **封号信号怎么产生**（`internal/dispatch/worker.go:22-120`）：
  发送返回 error 时，`isBanSignal(err)`（匹配 `wa_warning|banned|403`）为真则
  `gate.ApplyHealthSignal(jid,"wa_warning",6h)`（`internal/sendgate/sendgate.go:41-70`，`health_score -30`，跌破 `QuarantineThreshold=40` 即 `quarantined_until` 隔离）。
  失败记账 `markFailed`（`worker.go:137-150`）把 `campaign_recipients.state='failed'`、`last_error=<错误文本>`，并 `campaigns.failed = failed+1`。
- **`ban_status` 字段**（`'banned'/'flagged'`）目前主要由**外部/管理导入**写入；引擎内部走的是 `health_score`+`quarantined_until` 这条软隔离链路。
- **现有 BanRate 指标**（`internal/api/admin_api.go:42,64-72`）：
  `banned/total`，**全局、账号级**（跨租户，非按 campaign），仅用于 `GET /admin/stats` 展示。
- **配置加载**（`internal/config/config.go:49-126`、`cmd/wadist/main.go:55-75`）：只在启动时从 env 读，**无热加载**。
- **控制面配置已就绪**：`system_risk_config.ban_rate_circuit_breaker`（`migrations/0014_risk_config.sql`）可由管理员热更新，但**引擎当前不读它**（这正是本设计要补的缺口）。

### 1.2 缺口
1. 没有"按任务统计封号率"的度量。
2. 没有任何组件读取 `ban_rate_circuit_breaker` 并据此动作。
3. 没有自动挂起逻辑，也没有"恢复/解除挂起"的 API（当前只有 stop，没有 resume）。

---

## 2. 红线立场（为什么这套设计是安全的）

红线：**严禁修改** `internal/billing`、`internal/dispatch`、`internal/store`、`internal/sendgate`、`internal/cluster` 五包的核心业务逻辑与计费/防封事务。允许：在 `internal/api` 调用导出方法、经 `SystemPool()` 做 INSERT/UPDATE。

本设计据此把所有新增逻辑放在**红线之外**：

| 动作 | 落点 | 是否红线 |
|---|---|---|
| 计算封号率 | 新包 `internal/riskbreaker` 内的 SQL | 否（新代码 + 只读现有表） |
| 读取阈值配置 | 新包读 `system_risk_config` | 否（控制面表） |
| 挂起任务 | `UPDATE campaigns SET state='paused'`（复用已有语义） | 否（与 admin API 同款 seed 式 UPDATE） |
| 审计 | `INSERT INTO audit_log` | 否 |
| 启动 goroutine | `cmd/wadist/main.go` 加几行 | 否（cmd 是装配层，不在五包内） |
| 恢复 API（resume） | `internal/api/admin_api.go` 新 handler | 否（api 层） |

**dispatcher 之所以会"听话"地停下，不是因为我们改了它，而是因为它本来就只发 `running` 的任务。** 我们只是用它已经认的状态去和它通信。

---

## 3. 术语：什么是"批次封号率"

本系统里**没有 sub-batch 实体**：一个"批次/任务"在数据模型上就是一个 **campaign**（`campaign_recipients` 通过 `campaign_id` 归属，没有 `batch_id`）。因此"批次封号率"= **单个 campaign 的封号率**。

给出两种可计算定义（均**只用现有列**，零引擎改动）：

### 定义 A（首选）—— 发送结果归因封号率
"这个任务最近发出去的消息里，有多大比例是因封号信号失败的。"
```sql
-- 参数：$1=campaign_id，$2=窗口起点（如 now() - interval '15 min'）
SELECT
  count(*) FILTER (WHERE state IN ('sent','failed') AND updated_at >= $2)                AS attempted,
  count(*) FILTER (WHERE state = 'failed' AND updated_at >= $2 AND (
        last_error ILIKE '%wa_warning%' OR last_error ILIKE '%banned%' OR last_error ILIKE '%403%'
  ))                                                                                     AS ban_failures
FROM campaign_recipients
WHERE campaign_id = $1;
-- ban_rate = ban_failures / NULLIF(attempted, 0)
```
- 直接对应 `worker.go:isBanSignal` 的判定，语义最贴近"发送导致封号"。
- **窗口化**（用 `campaign_recipients.updated_at`）让熔断对"突发飙升"敏感，而不是被任务生命周期里的历史平均稀释。
- 已知耦合：依赖 `last_error` 文本格式（与 `isBanSignal` 的字符串匹配同源）。缓解见 §9 与 §10。

### 定义 B（辅助/佐证）—— 账号损伤率
"这个任务用过的账号里，现在有多大比例已被封/隔离。"
```sql
WITH used AS (
  SELECT DISTINCT assigned_jid AS jid
    FROM campaign_recipients
   WHERE campaign_id = $1 AND assigned_jid IS NOT NULL
)
SELECT
  count(*)                                                                       AS used_accounts,
  count(*) FILTER (WHERE d.ban_status IN ('banned','flagged')
                      OR (d.quarantined_until IS NOT NULL AND d.quarantined_until > now())) AS impaired
FROM used u JOIN account_devices d ON d.account_jid = u.jid;
-- ban_rate_acct = impaired / NULLIF(used_accounts, 0)
```
- 覆盖了"外部/管理导入直接置 `banned`"以及隔离链路，能抓到定义 A 漏掉的情况。
- 噪声更大（账号可能被别的任务搞坏）。

**推荐策略**：以**定义 A 为主**触发；可选地要求"A 触发 **或** B 触发"二选一，或"A 触发且样本足够"。具体见 §5。

---

## 4. 架构总览

```
                         ┌──────────────────────────────────────────┐
                         │  cmd/wadist/main.go  (装配层, 非红线)        │
                         │   ...                                      │
   每 1s ───────────────▶│   dispatcher.DispatchRunning() [不改]      │
                         │   sup.Go(riskbreaker.Run)  ◀── 新增几行     │
                         └──────────────┬───────────────────────────┘
                                        │ 每 evalInterval (如 20s)
                                        ▼
                  ┌─────────────────────────────────────────────┐
                  │  internal/riskbreaker  (全新包, 非红线)         │
                  │  1. 选主：pg_try_advisory_lock (多节点只跑一个)  │
                  │  2. 读 system_risk_config (阈值/开关, 热加载)    │
                  │  3. for each campaign WHERE state='running':    │
                  │       算 ban_rate (定义A[+B], 含样本/窗口)        │
                  │       if 触发: UPDATE state='paused'            │
                  │                + INSERT audit_log + metric++    │
                  └──────────────┬──────────────────────────────────┘
                                 │ 仅通过 SQL 与既有表通信
                                 ▼
        campaigns / campaign_recipients / account_devices / system_risk_config / audit_log
                                 ▲
                                 │ 状态变 paused 后
   dispatcher 下一 tick 自然跳过该任务（无需感知熔断器的存在）
```

要点：
- 熔断器是**旁路监督者（side-car supervisor）**，与 dispatcher 之间**只通过数据库状态解耦通信**。两者无直接代码依赖。
- 多节点（cluster 部署）下用 **PostgreSQL advisory lock** 选主，保证同一时刻只有一个节点在评估，避免重复/竞争（即便重复，`UPDATE ... WHERE state='running'` 也幂等，advisory lock 只是省功）。

---

## 5. 熔断判定逻辑（含防误触发）

```
评估单个 running campaign：
  cfg = readRiskConfig()                     // 单行 SELECT，天然热加载
  if !cfg.enabled: return                    // 总开关（见 §9 建议新增列）
  threshold = cfg.ban_rate_circuit_breaker   // 0..1

  (attempted, banFailures) = 定义A(campaignID, now()-window)
  if attempted < cfg.min_sample: return      // 样本不足不评判（默认 20），防 1/1=100% 误杀
  rateA = banFailures / attempted
  trip  = rateA >= threshold

  // 可选佐证：账号损伤率
  if cfg.use_account_signal:
      (used, impaired) = 定义B(campaignID)
      rateB = impaired / max(used,1)
      trip  = trip || (used >= cfg.min_sample_accounts && rateB >= threshold)

  if trip:
      if cfg.dry_run:  记录"将熔断"日志/metric，但不改状态   // 灰度/观察期
      else:            熔断动作（§6）
```

防误触发三件套：
1. **最小样本** `min_sample`：attempted 不够不评判。
2. **滑动窗口** `window`：只看最近一段时间（默认 15min），对突发敏感、不被历史稀释。
3. **dry-run 观察期**：上线初期只告警不动作，校准阈值后再开真熔断。

---

## 6. 熔断动作

单事务内（`SystemPool`）：
```sql
-- 6.1 幂等挂起（只熔断仍在 running 的）
UPDATE campaigns SET state='paused'
 WHERE id=$1 AND state='running'
RETURNING tenant_id;     -- RowsAffected=0 表示已被别处处理，跳过后续

-- 6.2 审计（audit_log 来自 0008_security.sql）
INSERT INTO audit_log (tenant_id, actor, action, detail, created_at)
VALUES ($tenant, 'system:riskbreaker', 'campaign.circuit_break',
        jsonb_build_object('campaign_id',$1,'ban_rate',$rate,'threshold',$th,
                           'attempted',$att,'ban_failures',$bf,'window',$win), now());
```
> 注：`audit_log` 实际列名以 `migrations/0008_security.sql` 为准，实施时对齐字段。

并发/可观测：
- **Prometheus 指标**：复用现有 metrics（`worker.WithMetrics(m)` 同款 `m`），新增
  `riskbreaker_trips_total{tenant}`、`riskbreaker_eval_seconds`、`riskbreaker_campaign_ban_rate`（gauge）。
- **恢复（resume）**：当前**只有 stop 没有 resume**。本设计需配套在 `internal/api/admin_api.go` 新增
  `POST /admin/campaigns/:id/resume` → `UPDATE campaigns SET state='running' WHERE id=$1 AND state='paused'`，
  让运营在排查后手动恢复。**不建议自动恢复**（封号根因未除，自动恢复会二次伤号）。
  前端「风控与审计」页可加一列"自动熔断"标记 + 恢复按钮。

---

## 7. 配置与热加载

- 熔断器**每个评估周期直接 `SELECT ... FROM system_risk_config WHERE id=1`**（单行，极廉价），因此管理员在「风控策略中心」改阈值后，**最迟一个评估周期内生效，无需重启**——这就解决了 §1.1 的"无热加载"问题，且只对熔断器生效，不影响引擎其它部分。
- 建议给 `system_risk_config` **追加几列**（控制面表，加性迁移，**非红线**）：
  ```sql
  -- migrations/0015_risk_breaker_opts.sql （示意）
  ALTER TABLE system_risk_config ADD COLUMN IF NOT EXISTS circuit_breaker_enabled BOOLEAN NOT NULL DEFAULT false;
  ALTER TABLE system_risk_config ADD COLUMN IF NOT EXISTS circuit_breaker_dry_run BOOLEAN NOT NULL DEFAULT true;
  ALTER TABLE system_risk_config ADD COLUMN IF NOT EXISTS min_sample INT NOT NULL DEFAULT 20;
  ALTER TABLE system_risk_config ADD COLUMN IF NOT EXISTS window_seconds INT NOT NULL DEFAULT 900;
  ALTER TABLE system_risk_config ADD COLUMN IF NOT EXISTS eval_interval_seconds INT NOT NULL DEFAULT 20;
  ```
  默认 `enabled=false, dry_run=true` —— **上线即安全**：不开不动作，开了先观察。

---

## 8. 实施计划（文件级，分阶段）

### 阶段 0｜控制面补全（零引擎、零风险）
- `migrations/0015_risk_breaker_opts.sql`：上面的加性列。
- `internal/api/admin_api.go`：`riskConfig` DTO + Get/Update 扩展上述字段；前端风控页加"启用熔断 / 观察模式 / 最小样本 / 窗口"控件。
- 验收：配置可存取、UI 可改、迁移幂等。**此阶段无任何行为变化**。

### 阶段 1｜熔断器（推荐，红线影响最小）
- **新增包** `internal/riskbreaker/`：
  ```go
  package riskbreaker

  type Breaker struct {
      pool *pgxpool.Pool   // 复用 mgr.SystemPool()
      m    Metrics         // 可选
      log  *log.Logger
  }
  func New(pool *pgxpool.Pool, opts ...Option) *Breaker

  // Run 阻塞运行评估循环，直到 ctx 取消；内部用 advisory lock 选主。
  func (b *Breaker) Run(ctx context.Context) error

  // EvaluateOnce 跑一轮全量评估，返回本轮熔断的 campaign 数（便于测试）。
  func (b *Breaker) EvaluateOnce(ctx context.Context) (tripped int, err error)
  ```
  内部：`readConfig`、`listRunningCampaigns`、`banRate(campaignID, window)`（定义 A/B）、`trip(campaignID, ...)`（§6 事务）。
- **装配** `cmd/wadist/main.go`（在已有 `sup.Go(dispatch loop ...)` 旁边）：
  ```go
  breaker := riskbreaker.New(pool).WithMetrics(m)
  sup.Go(func(lctx context.Context) error { return breaker.Run(lctx) })
  ```
- **resume API** `internal/api/admin_api.go` + 路由 `router.go`：`POST /admin/campaigns/:id/resume`。
- 引擎包 `internal/dispatch`、`internal/sendgate`、`internal/store`：**0 改动**。
- 验收：见 §11。

### 阶段 2｜精度增强（可选，按需，可能触红线）
仅当"基于 `last_error` 文本"的精度不够时再做，且需**单独授权**：
- 在 `campaign_recipients` 增一列 `ban_attributed BOOLEAN`，由 `worker.markFailed` 在 `isBanSignal` 时置真——**这会改 `internal/dispatch/worker.go`（红线）**，故默认不做。
- 或把 `isBanSignal` 的判定 patterns 抽到一个**新的非红线小包** `internal/bansignal`，dispatch 与 riskbreaker 共用，避免文本判定两处漂移——抽取动作本身仍需改 dispatch 的 import（轻度触红线），列为备选。

---

## 9. 数据模型变更汇总

| 变更 | 文件 | 红线 | 必需性 |
|---|---|---|---|
| `system_risk_config` 加 enabled/dry_run/min_sample/window/eval_interval | `migrations/0015_*.sql` | 否（控制面表） | 阶段0 必需 |
| `audit_log` 复用（不改表） | — | 否 | 阶段1 |
| `campaign_recipients.ban_attributed` | 迁移 + `worker.go` | **是（worker.go）** | 阶段2，可选，需单独授权 |
| `campaigns.paused_reason/paused_at`（区分自动 vs 手动挂起） | 迁移（加性） | 否（仅加列；不改引擎逻辑） | 可选增强 |

> 关于 `campaigns` 加列：参考先例 `account_devices.tags`（`migrations/0013`），加性 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` 属 schema 演进，不触碰引擎 Go 逻辑，判定为非红线。但若希望绝对零接触 `campaigns` 写入路径，可只用 `audit_log` 记录熔断原因。

---

## 10. 失败模式与边界

1. **误熔断（假阳性）**：小样本/共享账号/`last_error` 文本误匹配。缓解：min_sample + 窗口 + dry-run 校准 + 定义A 为主。
2. **漏熔断（假阴性）**：封号以 `ban_status` 直写（外部）出现、且发送侧未报错。缓解：开启定义 B 佐证。
3. **`last_error` 文本耦合**：`isBanSignal` 改了 patterns 而 SQL 没跟。缓解：在文档/代码注释里标注"两处同源"，或走阶段2 抽包共享。
4. **多节点竞争**：advisory lock 选主；即便不选主，挂起 UPDATE 幂等，最坏是重复审计（可对 audit 去重）。
5. **挂起风暴**：阈值配太低 → 大面积挂起。缓解：dry_run 先行；可加"单轮最多熔断 N 个"的护栏 + 告警。
6. **与手动 stop 冲突**：都改成 paused，天然兼容；resume 时 `WHERE state='paused'` 幂等。
7. **时区/窗口**：统一用库内 `now()`，窗口用 `updated_at >= now()-interval`，避免应用与库时钟分歧。

---

## 11. 测试计划

- **单元/集成**（用一次性 postgres，沿用 `scripts/migrate_twice.sh` 的隔离方式）：
  - 造一个 campaign + N 条 recipients，部分 `state='failed', last_error='... wa_warning ...'`，断言 `banRate()` 计算正确。
  - `attempted < min_sample` 时不熔断；`>= threshold` 且样本足够时熔断。
  - `EvaluateOnce` 把 `running` 改成 `paused`，已 `paused` 的不重复处理（RowsAffected=0）。
  - dry_run 模式只记录不改状态。
  - resume：`paused → running` 幂等。
- **并发**：两个 `EvaluateOnce` 并跑，advisory lock 保证只有一个生效。
- **回归**：dispatcher 单测不受影响（未改动）。
- **可观测**：metric/audit 断言。

---

## 12. 上线与回滚

- **上线即安全**：`enabled=false`（默认）→ 熔断器空跑无副作用。
- **灰度**：`enabled=true, dry_run=true` 观察 1–2 天，看 `riskbreaker_*` 指标与 audit"将熔断"记录，校准阈值/窗口/样本。
- **正式**：`dry_run=false`。
- **回滚**：把 `enabled=false`（一次 PUT，立即热生效），或在 `cmd/wadist` 不再 `sup.Go(breaker.Run)` 重启。**dispatcher 完全不受影响**，回滚零风险。

---

## 13. 工作量与红线评估

| 阶段 | 工作量(人日, 粗估) | 红线影响 |
|---|---|---|
| 阶段0 控制面 | 0.5 | 无 |
| 阶段1 熔断器 + resume | 2–3 | **无**（新包 + cmd 装配 + api 层；引擎零改） |
| 阶段2 精度增强 | 1–2 | **有**（改 `worker.go`），需单独授权 |

**推荐**：阶段 0 + 阶段 1 即可交付一个**真实生效**的封号率自动熔断，且**不触碰任何红线引擎逻辑**。阶段 2 仅在精度确实不足时再单独评审授权。

---

## 附录 A：另外两个参数（间隔 / 日上限）的接入概要

本文件聚焦熔断。另两个参数若要让引擎实际读取，代价更高、且**必然触红线**，故仅作概览：

- **min/max 发送间隔**：当前是构造期注入的单一 `baseGap=3s`（`cmd/wadist/main.go:124-143`；`NewSendGate`/`NewDispatcher` 签名见 `sendgate.go:37`、`dispatch/types.go:58`），且抖动 `jitter(baseGap)` 写死 ±40%。要支持 min/max 双值 + 热加载，需改 `Dispatcher`/`SendGate` 持有"配置provider"并在 `jitter` 处读取 → **改 dispatch + sendgate（红线）**。
- **单设备日上限**：硬编码号龄阶梯（`sendgate/quota.go`）+ SQL 函数 `effective_quota()`（`migrations/0005`），`selectaccount.go:26` 直接调该函数。要可配置需 Go 与 SQL 双向改 + 表化阶梯 → **改 sendgate + 迁移函数（红线）**。

这两项建议各自单独立项、单独授权，模式上同样可用"配置provider 周期刷新 `system_risk_config`"承接热加载，但**读取点在引擎内部，无法像熔断器那样旁路**。
