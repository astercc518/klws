# wadist — WhatsApp 企业级多账号并发分发系统

多租户 SaaS,基于 [whatsmeow](https://github.com/tulir/whatsmeow),支撑 **500+ 账号并发**、每账号绑定独立动态 4G 代理、**事务级精确扣费**与失败审核退款、advisory-lock fencing 防双开的**故障自愈接管**、信封加密 + RLS 的**多租户合规**、**零中断滚动发布**。

> 系统级架构、数据流、不变式、并发模型详见 **[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)**。

## 状态:M0–M11 全部完成 ✅

| | | | |
|---|---|---|---|
| **M0** 项目骨架 | **M3** 计费/审核退款 | **M6** 分发编排 | **M9** 分布式接管 |
| **M1** 存储/会话 | **M4** 对账 | **M7** 可观测性 | **M10** 安全/合规 |
| **M2** 代理池/绑定 | **M5** 防封号免疫 | **M8** 优雅停机 | **M11** 容量/灰度/硬化 |

15 个 Go 包、8 个迁移、全程真实 PG16/Redis7 集成测试 + `-race` + 高基数门禁。逐里程碑实现计划见 [`docs/superpowers/plans/`](docs/superpowers/plans/)。

## 快速开始

```bash
make up            # 起 postgres:16 (max_connections=700) + redis:7 (noeviction)
make test-race     # 全套测试(集成测试经 testcontainers 起临时 PG/Redis)
make gate          # 完整本地门禁:tidy + vet + test-race + labels + vuln
make memory-gate   # 每会话内存基线门禁(-tags memory_gate)
make chaos         # 节点死亡→接管混沌门禁(TestTakeoverChaos)
make down
```

要求:**Go 1.26+**、**Docker**(集成测试用 testcontainers)。沙箱若 Ryuk 起不来,命令已默认带 `TESTCONTAINERS_RYUK_DISABLED=true`。

运行二进制:
```bash
go build -o bin/wadist ./cmd/wadist && WADIST_POSTGRES_DSN=... ./bin/wadist
# /metrics /healthz /readyz 默认在 :9090
```
未设 `WADIST_MASTER_KEY` 时安全特性禁用(security disabled),便于本地无密钥启动。

## 配置(环境变量)

| 变量 | 默认 | 说明 |
|---|---|---|
| `WADIST_POSTGRES_DSN` | — (必填) | 业务 PG DSN |
| `WADIST_REDIS_ADDR` | `localhost:6379` | asynq + 准入 Lua |
| `WADIST_NODE_ID` | 主机名 | 集群节点标识 |
| `WADIST_NODE_REGION` | `default` | 数据驻留区域 |
| `WADIST_MAX_OPEN_CONNS` | `50` | 业务池大小 |
| `WADIST_MAX_LOCK_CONNS` | `300` | advisory-lock 池 = **单节点账号上限** |
| `WADIST_MAX_CONCURRENT_STARTS` | `32` | 起号并发上限(SetLimit) |
| `WADIST_ASYNQ_CONCURRENCY` | `32` | asynq worker 并发 |
| `WADIST_METRICS_ADDR` | `:9090` | /metrics /healthz /readyz |
| `WADIST_HEARTBEAT_INTERVAL` | `10s` | 节点心跳 |
| `WADIST_NODE_STALENESS` | `30s` | 节点过期阈值(>心跳间隔) |
| `WADIST_TAKEOVER_SCAN_INTERVAL` | `15s` | 接管扫描周期 |
| `WADIST_SHUTDOWN_TIMEOUT` | `30s` | 排空超时 |
| `WADIST_PRESTOP_DELAY` | `5s` | SIGTERM 后摘端点等待 |
| `WADIST_CANARY_PERCENT` | `0` | 金丝雀 cohort 百分比 |
| `WADIST_MASTER_KEY` | — | 信封加密 KEK(base64,32 字节);空=安全禁用 |
| `WADIST_BLIND_INDEX_KEY` | — | PII 盲索引 HMAC 键(base64,32 字节) |
| `WADIST_APP_TENANT_DSN` | — (回退主 DSN) | RLS 受限角色连接 |
| `WADIST_APP_SYSTEM_DSN` | — (回退主 DSN) | BYPASSRLS 跨租户连接 |

## 核心设计要点

- **每账号一条固定 advisory-lock 连接** —— 互斥 + fencing 防双开;`MaxLockConns` 即单节点账号上限。
- **冻结资金计费** —— Hold→Settle/RequestRefund;`message_id` 幂等;对账双不变式 `balance=Σledger`、`frozen=Σ未结charge`。
- **advisory 锁是接管唯一仲裁者** —— 心跳只是提示;`Healthy` 查 `pg_locks` 确认仍持锁(非仅 Ping),网络分区下不窃号。
- **防封号** —— 温号配额曲线 × 健康度折扣 × Redis Lua 原子准入 × 健康度熔断隔离。
- **合规** —— 信封加密 + crypto-shred + RLS 租户隔离 + PII 盲索引 + append-only 审计 + 发送前压制。
- **零中断滚动** —— `/readyz` 排空 + preStop + `maxUnavailable:0` + M9 接管迁移账号([`deploy/`](deploy/))。

## 仓库结构

```
cmd/wadist/        进程入口(装配 + 信号 + 排空)
internal/          15 个包(见 docs/ARCHITECTURE.md §2)
migrations/        0001–0008(全幂等,migrate_twice 验证)
deploy/            k8s Deployment(滚动)+ PDB + 基线说明
scripts/           check_metric_labels.sh / migrate_twice.sh
docs/              ARCHITECTURE.md + superpowers/plans/(逐里程碑计划)
.github/workflows/ release-gate.yml(含 chaos + memory + 幂等门禁)
```

## 承接债(非阻塞,见 [ARCHITECTURE.md §10](docs/ARCHITECTURE.md))

RLS 全量改造、PII 明文列收缩 + 完整 GDPR 擦除、per-tenant 盲索引键、PII 导入路径接线、生产角色口令轮换、多区域 DSN 接入。
