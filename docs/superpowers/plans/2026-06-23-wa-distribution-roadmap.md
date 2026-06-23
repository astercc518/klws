# WhatsApp 企业级多账号并发分发系统 — 开发主路线图

> **For agentic workers:** 本文件是**主路线图**,不直接实现。每个里程碑(M0–M11)对应一份独立的详细实现计划(`docs/superpowers/plans/2026-06-23-mN-*.md`),用 superpowers:subagent-driven-development 或 superpowers:executing-plans 逐份执行。

**Goal:** 构建一套基于 whatsmeow 的多租户 SaaS,支撑 500+ 账号并发、动态 4G 代理隔离、事务级扣费与失败审核退款、故障自愈接管。

**Architecture:** 节点无状态(状态全externalize到 Postgres/Redis);正确性收敛到单点(扣费→ledger、互斥→advisory lock、配额→Redis 原子准入);纵深防御 + 加密即删除。

**Tech Stack:** Go 1.22+,whatsmeow,PostgreSQL 16 (pgx/v5),Redis 7 (hibiken/asynq),Prometheus,testcontainers-go,goleak。

---

## Global Constraints(每个里程碑的任务都隐含遵守)

- Go **1.22+**;模块路径 `github.com/acme/wadist`(占位,按实际改)。
- 金额一律 **整数最小货币单位**(int64),严禁 float。
- 数据库占位符 PostgreSQL 风格 `$1`;方言字符串恒为 `"postgres"`。
- 所有外部调用必须带 `context.Context` 超时;无裸 `context.Background()` 进入热路径。
- 业务连接以**非 superuser、非 BYPASSRLS** 角色 `app_tenant`;跨租户后台作业用 `app_system`(BYPASSRLS),两个连接池物理分离。
- 错误一律 `fmt.Errorf("...: %w", err)` 包裹;不吞错(除显式幂等放行处,需注释说明)。
- 迁移必须**可重入**(连续 apply 两次 schema diff 为空)且遵循 **expand-contract**。
- 每个 PR 必须过 CI 门禁(见 §验收门禁),`release-gate` 为 required check。
- 测试默认 `go test -race`。

---

## 子系统依赖图

```
M0 项目骨架/基础设施
 ├─> M1 存储/会话持久化 ──> M2 代理池/绑定 ─┐
 │                          │              │
 ├─> M3 计费/审核退款 ──> M4 对账           │
 │        │                                │
 │        └──────────> M5 防封号免疫 ───────┼─> M6 分发编排
 │                          (依赖 M1+M3)    │
 ├─> M10 安全/合规(横切,尽早奠基)─────────┘
 │
 └─> M8 优雅停机(依赖 M1+M2)──> M9 分布式接管
                                       │
        M7 可观测性(横切,M1 起持续接入)
        M11 容量规划/灰度发布(运维,末期)
```

## 排期(波次 / 并行度;假设 3–4 名 Go 工程师)

| 波次 | 里程碑 | 可并行 | 交付物(可独立运行测试) | 粗估 |
|------|--------|--------|--------------------------|------|
| W1 | **M0** 骨架 | — | repo + docker-compose(PG/Redis)+ 迁移工具 + config/log + CI 雏形 | 1w |
| W2 | **M1** 存储/会话 · **M3** 计费 · **M10a** 加密底座(Cipher+RLS 角色) | 三路并行 | DeviceStore+advisory lock;钱包/charge/ledger/退款审核;信封加密+RLS 策略 | 2w |
| W3 | **M2** 代理 · **M4** 对账 · **M5** 防封号 | 三路并行 | BindProxy 抢占;对账不变式;温号/准入/健康度 | 2w |
| W4 | **M6** 分发编排 · **M8** 优雅停机 | 两路并行 | campaign→撮合→发送闭环;Registry+Supervisor 停机 | 2w |
| W5 | **M9** 接管 · **M7** 可观测 · **M10b** 合规(crypto-shred/驻留/审计) | 三路并行 | 心跳+fencing+扫描+接管;metrics+collector;删除权/驻留/审计日志 | 2w |
| W6 | **M11** 容量/灰度 · 硬化 | — | 容量计算器;k8s 滚动+preStop;混沌门禁全绿;安全评审 | 1.5w |

> 关键路径:M0→M1→M2→M6 与 M0→M3→M5→M6。M6 是收口节点,W4 之前所有依赖必须就绪。M7/M10 是横切关注点,越早接入返工越少(尤其 M10 的分租户密钥粒度,**第一天定对**)。

## 里程碑清单(每项 = 一份待展开的详细计划)

- **M0 项目骨架** ✅ — go module、`internal/config`(env→store.Config)、`internal/log`(zap 适配 waLog)、`docker-compose.yml`(postgres:16 max_connections=700 + redis:7 noeviction)、`scripts/migrate_twice.sh` + `check_metric_labels.sh`、`.github/workflows/release-gate.yml`、Makefile `gate` 目标。
- **M1 存储/会话持久化** ✅ *已展开为详细计划* → `2026-06-23-m1-storage-session.md`
- **M2 代理池/绑定** — `proxy_pool` schema、`BindProxy`(CTE + FOR UPDATE SKIP LOCKED)、`ReleaseProxy`、`ReportProxyFailure/Success`、`ApplyProxy`、死代理重绑。
- **M3 计费/审核退款** — `tenant_wallets/billing_charges/wallet_ledger/refund_requests` schema、`Hold/Settle/RequestRefund/Approve/RejectRefund`、`moveWallet/insertLedger`、asynq 接线。
- **M4 对账** — `reconciliation_runs`、`ReconcileTenant`(单语句单快照)、`ReconcileAll`(集合式)、`DriftHandler`(告警+可选锁钱包)、夜间 cron。
- **M5 防封号免疫** — warmup 配额曲线、`effective_quota`(Go+SQL 双份)、Redis 准入 Lua、`Admission/Ticket`、健康度评分+熔断、回血作业、`SendGate.Admit`。
- **M6 分发编排** — `campaigns/campaign_recipients/campaign_templates/media_uploads` schema、健康度加权 `selectAccount`、`RunDispatcher/dispatchBatch`、媒体上传缓存、模板渲染、`SendWorker` 全链路接线。
- **M7 可观测性** — Prometheus `Metrics`(有界 label)、`PublishRegistryState`、`DBCollector`(TTL 缓存)、业务埋点、`/metrics` HTTP。
- **M8 优雅停机** — `Registry`、`Session`、`Supervisor.Shutdown`(asynq 先停进料→断会话→关池)、信号接线、`SetLimit(32)` 限并发。
- **M9 分布式接管** — `cluster_nodes`、`RunHeartbeat`、`DeviceLock.Healthy`+`guardSession`(fencing)、`RunTakeoverScanner`(SKIP LOCKED + asynq Unique)、`TakeoverHandler`、`StartAccountWithLock`、`deregisterNode`。
  - ⚠️ **M1 遗留前置(必须先处理)**:`store.DeviceLock.Healthy` 当前仅做连接存活探测(`conn.Ping`),不校验 advisory lock 所有权。`guardSession` 必须补真正的所有权再校验(查 `pg_locks` 按本 backend pid,或 fencing epoch),否则连接被透明重连时 `Healthy` 会误报 true → 双开 → 封号(路线图风险#4)。
- **M10 安全/合规** — `Cipher`(AES-256-GCM 信封+版本轮换)、RLS 策略+双角色+`WithTenant`、`tenant_keys`+crypto-shredding、PII 加密+盲索引、`suppression_list`、数据驻留路由、`audit_log`(只追加)。
- **M11 容量/灰度** — 容量计算器 `Plan()`、postgresql/redis 配置基线(`max_connections≥A`、`maxmemory noeviction`)、k8s 滚动(`maxSurge:1/maxUnavailable:0`)、`preStop` drain、PDB、cohort 金丝雀。

## 验收门禁(CI `release-gate`,贯穿所有里程碑)

| 门禁 | 命令 | 归属里程碑 |
|------|------|-----------|
| lint+vet+staticcheck | `go vet ./... && staticcheck ./...` | M0 |
| 高基数 label 扫描 | `./scripts/check_metric_labels.sh` | M7 |
| 迁移可重入 | `./scripts/migrate_twice.sh` | M0 |
| 单元+竞态 | `go test -race -count=1 ./...` | 全部 |
| 对账属性测试 | `go test -race -run TestBilling_Invariants_Property` | M4 |
| goroutine 泄漏 | `go test -run TestGracefulShutdown_NoLeak` | M8 |
| 接管混沌时序 | `go test -race -run TestTakeoverChaos -count=30` | M9 |
| 内存基线 | `go test -tags memory_gate -run TestMemoryBaseline_PerSession` | M11 |
| 依赖漏洞 | `govulncheck ./...` | M0 |

> 上线前置硬条件:**M9 混沌门禁绿** —— 灰度滚动 = 计划内节点故障,只有接管被证明安全,才敢滚。

## 顶层风险与缓解

1. **advisory lock 占用 ≥A 条常驻连接**(容量主成本)。缓解:`max_connections≥A`、锁连接直连不走 PgBouncer txn 池;若 A 涨到数千,预留"租约表替代锁"的演进口子(M9 设计时隔离锁接口)。
2. **whatsmeow 会话表明文**。缓解:磁盘级加密 + 最小权限 + VPC 隔离;在威胁模型显式标注明文边界(M10)。
3. **分租户密钥粒度改不起**。缓解:M10a 第一天就按租户切 DEK,撑起后续 crypto-shredding/驻留。
4. **接管 fencing 慢于接管 = 双开封号**。缓解:`fencing 周期 < 心跳超时`,M9 混沌测试断言重叠 ≤ fencing 上界。
5. **群发扎堆单号**。缓解:M6 健康度加权 selectAccount + M5 拟人节奏,双层打散。
