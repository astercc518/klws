# 上线记录：admin-dispatch-v2（资源精细化 · 熔断控制台 · 明细穿透+回执）

> 日期：2026-06-29 ～ 2026-06-30　|　分支：`feature/admin-dispatch-v2`
> 主机：生产 docker-compose 栈（项目 `klws`）。全程带齐 `-f docker-compose.prod.yml -f docker-compose.adopt.yml`，postgres 外部卷未受影响。

## 1. 入库
- 分支 `feature/admin-dispatch-v2`，5 个语义化提交：迁移 / 后端引擎 / 前端 / 设计文档 / wadist 物料。
- 状态：**本地就绪，未 push**（执行环境无 git 凭据）。待人工 `git push -u origin feature/admin-dispatch-v2` + 开 PR（正文用 `docs/HANDOVER-resource-riskbreaker-zh.md`）。

## 2. 数据库（生产库 db=wadist @ klws-postgres-1）
- 上线前备份：`pg_dump -U app -d wadist` → 快照已存（会话 scratchpad，迁移前）。
- 迁移按序幂等应用并校验：
  - `0013_device_tags` / `0014_risk_config` / `0015_risk_breaker_opts` / `0016_recipient_receipts`
  - 校验：`account_devices.tags`✓、`system_risk_config` 单行✓、5 个熔断列✓、`campaign_recipients.delivered_at/read_at`✓
- 熔断配置写入：`UPDATE system_risk_config SET circuit_breaker_enabled=true, circuit_breaker_dry_run=true`（阈值默认 0.15 / 样本 20 / 窗口 900s / 巡检 20s）→ **先观察、不真停**。

## 3. 镜像与服务
- 重建 `klws-console:latest`、`klws-frontend:latest`（compose build，双 `-f`），平滑重启 `backend`/`frontend`；postgres 未重建。
- **新增并启动 wadist worker**：
  - 物料：`Dockerfile.wadist`（构建 `cmd/wadist`）、`docker-compose.worker.yml`（opt-in 覆盖，`WADIST_RECEIPTS=on`、`WADIST_NODE_ID=wadist-compose-1`）。
  - 启动命令（三 `-f`，人工经权限授权后执行）：
    ```
    docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml -f docker-compose.worker.yml up -d wadist
    ```
  - 启动前实测爆炸半径 = 0（0 活跃账号 / 0 running 任务）→ 拉起只空转，不发任何消息。

## 4. 回验证据
- 容器：`klws-wadist-1 Up`；`klws-postgres-1 Up 22h (healthy)` —— **未被重建**；backend/frontend/redis/proxy 正常。
- API（origin，经 proxy）：`/api/v1/admin/settings/risk`、`/admin/campaigns/:id/recipients`、`/campaigns/:id/recipients` → 401（存活、非 404/502）。
- 前端：`/`、`/admin/settings/risk`、`/dashboard/campaigns` → 200。
- wadist 日志：`security enabled`、asynq 队列处理中、`metrics on :9090`、无 error/panic/fatal。
- worker↔DB 连通：`cluster_nodes` 心跳 `wadist-compose-1` 新鲜（<60s）。
- 公网 `klws.cc` 直连未验（执行环境无公网 egress；origin 全链路已绿）。

## 5. 当前生效范围
- ✅ 控制台 + 数据面全部上线：资源标签/IP 绑定、风控策略页、群发明细穿透（漏斗+分页明细）、auto_tripped/resume。
- ✅ 回执管道 + 熔断监督器随 wadist 运行；当前无业务数据 → 待有活跃账号与 running 任务后，回执回填「已送达/已读」、熔断按 dry-run 观察。

## 6. 回滚
- worker：`... -f docker-compose.worker.yml stop wadist`（发送停、campaigns 仍 running、回执停写）。
- 回执：worker `WADIST_RECEIPTS=off` 重启（发送不受影响）。
- 熔断：`UPDATE system_risk_config SET circuit_breaker_enabled=false`（热生效）。
- 服务：回滚镜像后 `up -d backend frontend`（务必双 `-f`）。迁移加性，无需回滚 schema。

## 7. 红线复核
- 仅 `internal/cluster` 一处**已授权**的加性回执事件 handler（发送逻辑未改）；`billing/dispatch/store/sendgate` 零改动。其余落在迁移（加性）/`internal/api`/新包 `internal/riskbreaker`·`internal/receipt`/装配层 `cmd/wadist`/前端。

## 8. 待办（非阻塞）
1. `git push` 分支 + 开 PR。
2. 业务接入后：观察 dry-run 熔断日志校准阈值 → 关 `dry_run`；明细抽屉核对回执回填。
3. 多节点：各 worker 不同 `WADIST_NODE_ID`。
4. 下一迭代（需红线授权）：调度参数（间隔/日上限）引擎接入、"delivered" 健康分改由真实回执触发。
