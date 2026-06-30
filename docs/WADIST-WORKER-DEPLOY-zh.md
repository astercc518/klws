# 部署接入文档：wadist 发信 worker（激活回执 + 熔断）

> 状态：**物料就绪，待受控上线**（本文件 + `Dockerfile.wadist` + `docker-compose.worker.yml` 已就位；**镜像未构建、worker 未启动**——拉起由人工按下）。
> 背景：V1.0 生产 compose 仅有 `postgres/redis/backend(console)/frontend/proxy`，**没有 wadist worker**，仓库 `./Dockerfile` 也只构建 `console`+`seed`。控制台与数据面已上线；回执漏斗的「已送达/已读」与熔断监督器**需 wadist 运行才会真正生效**。

---

## 0. 严重前提（务必先读）

启动 wadist **不是"打开一个开关"**，而是**拉起真实的 WhatsApp 发信引擎**：
- 连接所有 `account_devices.ban_status='active'` 的账号（whatsmeow 会话）；
- 派发所有 `campaigns.state='running'` 的任务——**真实发消息 + 计费冻结**；
- 运行封号率熔断监督器（已配置 `enabled=true, dry_run=true`，先只观察）；
- 接收 delivered/read 回执，回填 `delivered_at/read_at`。

**含真金白银与封号风险。** 上线前确认：是否真的要在此刻恢复群发？有哪些活跃账号 / 在跑任务会被立即接管？

---

## 1. 物料清单（本次新增，opt-in，启动前零影响）

| 文件 | 作用 |
|---|---|
| `Dockerfile.wadist` | 构建 `klws-wadist:latest`（`cmd/wadist` 静态二进制，distroless，ENTRYPOINT `/wadist`） |
| `docker-compose.worker.yml` | **独立覆盖文件**，定义 `wadist` 服务；不混入默认 `up`，必须显式三 `-f` 才启动 |
| 本文件 | 部署/接入/回滚 runbook |

> 这三者只是"待用配方"，不构建、不启动就**完全不影响**现有控制台栈。

---

## 2. 前置条件（已满足项已标注）

- ✅ 迁移 0013–0016 已应用到生产库（`tags`/`system_risk_config`/`delivered_at`/`read_at` 均在）。
- ✅ 熔断配置已设 `enabled=true, dry_run=true`（worker 起来后会读到并只观察）。
- ✅ `.env` 含 `WADIST_POSTGRES_DSN`/`WADIST_REDIS_ADDR`/`WADIST_MASTER_KEY` 等（DSN 主机为 `postgres:5432`、redis `redis:6379`，与 console 同源）。
- ⛳ 需确认：活跃账号与已绑定代理是否就绪（无活跃账号则 worker 起来后空转，安全；有则会立即接管发送）。
- ⛳ 出网：worker 需能访问 WhatsApp 与各账号绑定的代理（compose 默认 bridge 有 egress）。

---

## 3. 上线步骤（受控）

> 铁律：**任何 compose 命令都带齐 `docker-compose.prod.yml` + `docker-compose.adopt.yml`**，否则 postgres 可能被重建到空卷（见部署备忘）。worker 再追加第三个 `-f`。

```bash
cd /var/klwa

# 1) 构建 worker 镜像（不影响运行中的容器）
docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml -f docker-compose.worker.yml build wadist

# 2) 启动 worker（仅 wadist；postgres/redis 已在跑且配置匹配，不会被重建）
docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml -f docker-compose.worker.yml up -d wadist

# 3) 看日志确认连接 / 派发 / 熔断观察
docker logs -f --tail=100 klws-wadist-1   # 或 compose ps 查实际容器名
```

启动后预期日志（节选）：
- 账号接管 / heartbeat；
- `dispatch loop` 正常 tick；
- 熔断（dry-run）：超阈值任务会打印 `riskbreaker: DRY-RUN would pause campaign <id> (...)`，**不真正挂起**；
- 回执：随真实 delivered/read 到达，`campaign_recipients.delivered_at/read_at` 开始填充。

---

## 4. 验证（worker 起来后）

```bash
# 回执是否开始回填（应随真实送达增长）
docker exec klws-postgres-1 psql -U app -d wadist -At -c \
 "SELECT count(*) FILTER (WHERE delivered_at IS NOT NULL) AS delivered,
         count(*) FILTER (WHERE read_at IS NOT NULL) AS read
    FROM campaign_recipients;"

# 控制台明细抽屉：任务漏斗的「已送达/已读」两级开始有数（前端无需改动）
# 熔断观察：grep worker 日志里的 DRY-RUN 行，据此校准 ban_rate_circuit_breaker 后再考虑关 dry_run
```

正式启用真熔断（观察 1–2 天、校准阈值后）：
```bash
docker exec klws-postgres-1 psql -U app -d wadist -c \
 "UPDATE system_risk_config SET circuit_breaker_dry_run=false WHERE id=1;"   # 热生效，下一巡检周期起真正挂起
```

---

## 5. 回滚

```bash
# 停 worker：发送立即停止；campaigns 仍是 'running'（只是不再被派发），回执停止写入
docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml -f docker-compose.worker.yml stop wadist
```
- 关回执但保留 worker：把 worker 的 `WADIST_RECEIPTS` 设为 `off` 重启（发送不受影响）。
- 关熔断：`UPDATE system_risk_config SET circuit_breaker_enabled=false`（热生效）。
- 数据：迁移加性，无需回滚 schema。

---

## 6. 多节点 / 扩展

- 熔断监督器用 `pg_try_advisory_lock` 选主：多个 wadist 节点同一周期仅一个评估，挂起 UPDATE 幂等。
- 回执到达拥有该连接的节点就地写库，天然分布式，无需协调。
- 多节点时给每个 `WADIST_NODE_ID` 不同值（compose 覆盖文件里改 / 或多份覆盖）。

---

## 7. 与代码的对应（便于评审）

- 引擎接入点（已在 `feature/admin-dispatch-v2`）：`internal/riskbreaker`（旁路监督器，`cmd/wadist` 起 goroutine）、`internal/receipt`（回执记录器）、`internal/cluster/conn_whatsmeow.go`（授权的回执事件 handler）、`cmd/wadist/main.go`（装配 + `WADIST_RECEIPTS` 开关）。
- 设计依据：`docs/RISK-CIRCUIT-BREAKER-DESIGN-zh.md`、`docs/RECEIPT-INGESTION-DESIGN-zh.md`，交接见 `docs/HANDOVER-resource-riskbreaker-zh.md` §10。

---

## 8. 决策点（需你拍板）

1. **是否此刻拉起 worker**（= 恢复真实群发）？还是先只把物料入库、择期上线？
2. 若拉起：先在**低风险窗口**（少量活跃账号）观察，再逐步放量。
3. `WADIST_NODE_ID` 命名与是否多节点。
