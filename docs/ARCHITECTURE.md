# wadist 架构文档

**WhatsApp 企业级多账号并发分发系统** —— 多租户 SaaS,基于 whatsmeow,支撑 500+ 账号并发、每账号绑定独立动态 4G 代理、事务级精确扣费与失败审核退款、故障自愈接管、零中断滚动发布。

> 本文是系统级架构说明。各子系统的逐任务实现计划见 [`docs/superpowers/plans/`](superpowers/plans/);总路线图见 [`2026-06-23-wa-distribution-roadmap.md`](superpowers/plans/2026-06-23-wa-distribution-roadmap.md)。

---

## 1. 设计目标与约束

| 目标 | 落地机制 |
|---|---|
| **500+ 账号并发**,每账号独立 4G 代理隔离 | 每账号一条固定 advisory-lock 连接(`MaxLockConns` = 单节点账号上限);`store.ApplyProxy` 连接前注入代理 |
| **事务级精确扣费**,无重复/无悬挂 | 冻结资金模型(Hold→Settle/RequestRefund),`message_id` 幂等 + `ON CONFLICT` |
| **失败退款需管理员审核** | 退款进 `refund_pending` 审核队列,Approve/Reject 双人动作 + append-only 审计 |
| **极端稳定 / 内存可控** | 有界并发(errgroup `SetLimit`)、有序优雅停机、每会话内存基线门禁 |
| **强网络隔离 / 防封号** | 温号配额曲线 × 健康度折扣 × Redis Lua 原子准入 × 自动隔离 |
| **故障自愈** | advisory-lock fencing 防双开 + 心跳过期检测 + 跨节点接管(asynq Unique 去重) |
| **多租户数据隔离与合规** | 信封加密 + crypto-shredding + Postgres RLS 双角色 + PII 盲索引 + append-only 审计 |
| **零中断发布** | `/readyz` 排空 + preStop 延迟 + `maxUnavailable:0` 滚动 + M9 接管迁移账号 |

**技术栈**:Go 1.26、whatsmeow、PostgreSQL 16(pgx/v5)、Redis 7(hibiken/asynq)、Prometheus、Kubernetes。

---

## 2. 分层与包结构

依赖方向自上而下(上层可依赖下层,反之禁止;`internal/node` 是整合层,可依赖全部下层)。

```
cmd/wadist                    进程入口:装配 + 信号 + 排空
        │
internal/node                 分布式接管编排(整合 store+cluster+asynq)
        │
internal/cluster              会话注册表 / Supervisor 有序停机 / RoutingSender
        │
internal/dispatch             分发编排:选号 / 渲染 / 批量派发 / 发送 worker
        │
internal/billing  internal/sendgate  internal/capacity  internal/canary
   冻结资金计费      防封号准入门          容量计算          金丝雀分桶
        │
internal/store  internal/crypto  internal/audit  internal/metrics
   PG 连接池/锁/代理   信封加密/盲索引   append-only 审计   Prometheus
        │
internal/config   internal/log
   env→配置          zap→waLog 适配
```

**包职责一览(15 个):**

| 包 | 职责 | 关键导出 |
|---|---|---|
| `config` | env → 强类型配置 | `Config`、`Load()` |
| `log` | zap → whatsmeow `waLog.Logger` 适配 | `New`、`Production` |
| `store` | PG 双池(biz/lock)+ whatsmeow sqlstore;advisory-lock 互斥与 fencing;代理绑定;RLS `WithTenant`;PII 双写 | `Manager`、`DeviceLock`、`WithTenant`、`AcquireDeviceLock` |
| `crypto` | AES-256-GCM 信封加密 + 分租户 DEK + crypto-shred + 轮换;HMAC 盲索引;驻留 Router | `Cipher`、`KeyRepo`、`BlindIndex`、`Router` |
| `audit` | append-only 审计写入(支持同事务) | `AuditWriter`、`AuditEntry` |
| `billing` | 冻结资金计费:Hold/Settle/RequestRefund/Approve/Reject;对账;RLS 接入 | `Repo` |
| `sendgate` | 温号配额曲线 + Redis Lua 准入 + 健康度评分/熔断 | `SendGate`、`Admission` |
| `dispatch` | 活动→健康度选号→模板渲染→批量派发→`SendWorker` 全链路 | `Dispatcher`、`SendWorker` |
| `cluster` | 账号会话注册表;Supervisor 有序停机 + `SetLimit`;按 JID 路由发送 | `Registry`、`Supervisor`、`Session`、`RoutingSender` |
| `node` | 锁感知起号 + guardSession fencing;心跳;接管扫描/handler | `Orchestrator`、`StartAccountWithLock` |
| `metrics` | 有界 label 指标;DB 聚合采集器;`/metrics` `/healthz` `/readyz` HTTP | `Metrics`、`DBCollector`、`Server` |
| `capacity` | 节点/连接/Redis 内存容量计算(纯函数) | `Compute`、`Inputs`、`Plan` |
| `canary` | `fnv64a` 确定性 cohort 分桶 | `InCohort`、`Cohort` |
| `deploy` | k8s 清单字段校验测试 | — |

---

## 3. 数据模型(8 个迁移)

| 迁移 | 表/类型 | 说明 |
|---|---|---|
| 0001 | `account_devices`、`ban_status_t` | 账号设备(JID、tenant、proxy_id、health、owner_node、ban_status) |
| 0002 | `proxy_pool`、`proxy_type_t` | 代理池(原子绑定计数,SKIP LOCKED) |
| 0003 | `tenant_wallets`、`billing_charges`、`wallet_ledger`、`refund_requests` + 3 枚举 | 冻结资金:钱包/charge/不可变分录/退款审核 |
| 0004 | `reconciliation_runs` | 对账运行记录 |
| 0005 | (account_devices 加列)`effective_quota()` | 温号配额曲线(Go+SQL 双份) |
| 0006 | `campaigns`、`campaign_recipients`、`campaign_templates`、`media_uploads` + 2 枚举 | 分发编排 |
| 0007 | `cluster_nodes` | 节点心跳(分布式接管) |
| 0008 | `tenant_keys`、`suppression_list`、`audit_log` + RLS 角色/策略 + PII 列 | 安全合规 |

**租户隔离表(9 张,启用 RLS)**:account_devices、tenant_wallets、billing_charges、wallet_ledger、refund_requests、reconciliation_runs、campaign_templates、campaigns、campaign_recipients。策略 `tenant_id = current_setting('app.current_tenant_id', true)::bigint`,`FORCE` + `WITH CHECK`。

---

## 4. 核心数据流

### 4.1 发送链路(端到端)

```
campaign(running)
   │  Dispatcher.DispatchRunning  (受管循环,ticker)
   ▼
dispatchBatch  ──┐ FOR UPDATE SKIP LOCKED 拉取 pending recipients
                 │ suppression 盲索引拦截 → 命中置 skipped(绝不发送)
                 │ selectAccount(健康度加权:effective_quota - sent_today)
                 │ bump sent_today + claim message_id=campaignID:recipientID
                 │ 入队 asynq(事务内)
                 ▼
asynq queue(Redis,noeviction)
                 ▼
SendWorker.ProcessSend
   ① 终态幂等守卫(sent/failed → no-op)
   ② sendgate.Admit(Redis Lua:日配额 + pacing 原子准入)
   ③ billing.Hold(余额→冻结,message_id 幂等)
   ④ 渲染模板 + resolveMedia(按账号缓存)
   ⑤ RoutingSender.Send → 按 JID 路由到活跃 Session(whatsmeow)
   ⑥ 成功 → billing.Settle(消费冻结) + markSent;记 cohort 指标
      拒绝 → requeue(回退 sent_today,不扣费)
      封号信号 → ApplyHealthSignal(wa_warning) + RequestRefund + markFailed
```

**关键不变式**:
- `sent_today` 对称记账:派发时 +1,非发送退出(拒绝/失败)−1,真实发送保留 → 不会单调漂移耗尽配额。
- `message_id` 全链路幂等:asynq at-least-once 重投与 dispatchBatch 重跑都不会重复扣费/重复发送。

### 4.2 资金流(冻结资金模型)

```
Topup ──► balance↑(写 ledger)
Hold  ──► balance→frozen      (发送前预扣)
  ├─ Settle        frozen→消费        (发送成功)
  └─ RequestRefund frozen 保留 → 审核队列(发送失败)
        ├─ ApproveRefund  frozen→balance   (管理员同意)
        └─ RejectRefund   frozen→消费       (管理员拒绝)
```
不变式(对账双重校验,单快照 SQL):`balance = Σ ledger.delta_balance`;`frozen = Σ 未结 charge.amount`。漂移 → 告警 + 可选锁钱包。

### 4.3 故障接管(防双开)

```
节点 A 持 advisory 锁 + owner_node=A + 心跳
   │  A 进程死亡 → PG 连接断 → session 级 advisory 锁自动释放;A 心跳过期
   ▼
节点 B RunTakeoverScanner(ticker)
   StaleOwnedAccounts(owner=过期节点)∪ ListUnownedActiveAccounts
   → asynq Unique 入队(集群级去重)
   ▼
TakeoverHandler → StartAccountWithLock(jid)
   AcquireDeviceLock → 锁空闲则取得;被活节点占用 → ErrDeviceLocked → 跳过
   → ClaimAccount(owner=B) → Registry.Add → guardSession
```

**核心安全保证 —— advisory 锁是唯一仲裁者,心跳只是提示**:
- 网络分区下 stale-but-alive 节点仍持锁 → B 接管时 `AcquireDeviceLock` 被拒 → **账号不被窃取**。
- `guardSession` 周期 `DeviceLock.Healthy`(查 `pg_locks` 确认本 backend 仍持锁,非仅 Ping)→ 丢锁立即断会话防双开。双开窗口 ≤ guardInterval。

---

## 5. 并发与连接模型

- **每账号一条固定连接**:`AcquireDeviceLock` 从 `lockPool` 取一条连接,`pg_try_advisory_lock(fnv64a(jid))`,持有至会话结束。`MaxLockConns` = 单节点账号上限(默认 300)。
- **业务池 / 系统池 / 租户池**:`bizPool`(默认 50)走常规查询;`tenantPool`(app_tenant,受 RLS)+ `systemPool`(app_system,BYPASSRLS)在配双 DSN 时物理分离,否则别名 bizPool。
- **起号限流**:`Supervisor.StartAccounts` 用 `errgroup.SetLimit(MaxConcurrentStarts)` 限并发起号,避免雷鸣群。
- **asynq worker 并发**:`AsynqConcurrency`(默认 32)。

**容量公式**(`capacity.Compute`,与实际池逐项吻合):
```
accountsPerNode      = MaxLockConns
connsPerNode         = MaxLockConns + MaxOpenConns(biz) + MaxOpenConns(sqlDB)
                       (+ 2×MaxOpenConns 若双 RLS DSN)
requiredMaxConnections = connsPerNode × nodesNeeded + headroom
```

---

## 6. 安全与合规

- **信封加密**:主密钥 KEK(env `WADIST_MASTER_KEY`,base64 32 字节)包裹每租户随机 DEK(存 `tenant_keys`);密文 `version(4B) || nonce(12B) || ct+tag`,版本前缀支持轮换。
- **crypto-shredding**:删租户 DEK → 密文永久不可恢复。⚠️ 当前 PII 明文列**暂留**(M11 收缩前),故 `Shred` 单独**不**满足 GDPR 擦除——已在 `KeyRepo.Shred` 代码注释显式警告。
- **RLS 双角色**:`app_tenant`(NOSUPERUSER,受 RLS)+ `app_system`(BYPASSRLS,跨租户作业);`WithTenant(ctx, tenantID)` 在事务内 `set_config('app.current_tenant_id', ..., local)`。**当前 billing 租户路径已接入 RLS**,dispatch/sendgate/store/metrics 的迁移为文档化分期债。
- **PII**:`phone`/`phone_number` 双写明文 + `*_enc`(per-tenant DEK)+ `phone_bidx`(全局 HMAC 盲索引),去重用 `UNIQUE(campaign_id, phone_bidx)`。
- **审计**:`audit_log` append-only(`REVOKE UPDATE, DELETE`),退款审批同事务原子写入。
- **压制**:`suppression_list` 按盲索引在 dispatchBatch 拦截。

---

## 7. 可观测性

- **HTTP 端点**(`internal/metrics/Server`,默认 `:9090`):`/metrics`(Prometheus)、`/healthz`(liveness 恒 200)、`/readyz`(readiness,排空时 503)。
- **指标**(全部有界 label,`scripts/check_metric_labels.sh` 硬门禁禁 jid/tenant_id/message_id/phone):
  - 计数:`wadist_send_outcomes_total{outcome}`、`wadist_gate_decisions_total{allow,reason}`、`wadist_billing_ops_total{op,outcome}`、`wadist_health_signals_total{signal}`、`wadist_cohort_sends_total{cohort,outcome}`、`wadist_proxy_ops_total`、`wadist_lock_ops_total`、`wadist_dispatch_no_capacity_total`。
  - 直方图:`wadist_dispatch_batch_assigned`。
  - DB 聚合 gauge(TTL 缓存):`wadist_active_sessions`、`wadist_accounts{ban_status}`、`wadist_accounts_health{bucket}`、`wadist_wallet_balance_total`/`_frozen_total`/`_locked`、`wadist_proxy_slots_used`/`_free`/`wadist_proxy_dead`、`wadist_recipients_state{state}`、`wadist_charges_state{state}`、`wadist_refunds_pending`。

---

## 8. 生命周期与发布

**优雅停机顺序**(`cluster.Supervisor.Shutdown`):
```
SIGTERM → stop()
  SetReady(false)          /readyz → 503(k8s ~2s 摘端点)
  sleep(PreStopDelay 5s)   给端点传播留窗口
  Supervisor.Shutdown:
    ① StopIntake           asynq 停进料(等在途 handler 排空)
    ② cancel+await 后台循环 (心跳/扫描/分发)
    ③ CloseAll             断所有会话(先断连后释锁)
    ④ AfterSessions        DeregisterNode(清 owner_node + 删节点行)
    ⑤ CloseStore           关池
    ⑥ Flush                刷日志
```

**零中断滚动**:`maxUnavailable:0` + `maxSurge:1`;新 pod 就绪后旧 pod 才排空;在途发送由 Supervisor 排空,**账号连续性由 M9 接管迁移**(asynq 工作是队列拉取,非 HTTP 路由——`/readyz` 门控的是指标端点/LB)。`terminationGracePeriodSeconds:45 ≥ preStop(5)+PreStopDelay(5)+ShutdownTimeout(30)`。清单见 [`deploy/`](../deploy/)。

---

## 9. 质量门禁

`make gate` = `tidy + vet + test-race + labels + vuln`。CI(`.github/workflows/release-gate.yml`)额外:
- `migrate_twice` —— 迁移幂等证明。
- `check_metric_labels.sh` —— 高基数 label 硬门禁。
- `TestTakeoverChaos -count=5` —— 节点死亡→接管混沌门禁(testcontainers)。
- `-tags memory_gate TestMemoryBaseline` —— 每会话内存基线。
- `govulncheck` —— 漏洞扫描。

全部集成测试经 testcontainers 起真实 PG16/Redis7,`-race` 干净。代码 ~4.2k 行 + 测试 ~6.5k 行。

---

## 10. 承接债(已记录,非阻塞)

- **M10**:RLS 全量改造(剩 dispatch/sendgate/store/metrics ~24 处)、PII 明文列收缩 + 完整 GDPR 擦除路径、per-tenant 盲索引键(跨租户关联隐私)。
- **M11**:PII 导入路径接线(`main.go` `TODO(PII import path)`)、生产角色口令轮换(迁移内为 dev 口令)、真实多区域 DSN 接入(`crypto.Router` 当前桩)。
