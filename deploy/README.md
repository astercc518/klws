# wadist 部署与运维手册

k8s 滚动部署、容量规划、生产配置基线、发布/回滚/金丝雀 runbook、告警建议。清单见 [`deployment.yaml`](deployment.yaml)、[`pdb.yaml`](pdb.yaml);系统架构见 [`../docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md)。

---

## 1. 容量模型(`internal/capacity.Compute`)

```
accountsPerNode        = MaxLockConns                         // 每账号一条固定 advisory-lock 连接
connsPerNode           = MaxLockConns + MaxOpenConns + MaxOpenConns   // lock + biz + sqlDB
                         (+ 2×MaxOpenConns 若 app_tenant/app_system 用独立 DSN)
nodesNeeded            = ceil(accounts / accountsPerNode)
requiredMaxConnections = connsPerNode × nodesNeeded + HeadroomConns
redisMemoryMB          = ceil((accounts×(dailyKeyBytes+pacingKeyBytes) + queueDepth×taskBytes) × 2 / 1MiB)
```

### Sizing 示例(MaxLockConns=300、MaxOpenConns=50、headroom=50、单 DSN)

| 账号规模 | 节点数 | 每节点连接 | PG `max_connections` ≥ | Redis(粗估) |
|---|---|---|---|---|
| 300 | 1 | 400 | 450 | 512 MB 基线 |
| 600 | 2 | 400 | 850 | 512 MB |
| 1,500 | 5 | 400 | 2,050 | 512 MB–1 GB |
| 3,000 | 10 | 400 | 4,050 | 1 GB |
| 10,000 | 34 | 400 | 13,650 | 2 GB |

> 双 RLS DSN 时每节点连接 = 500(+2×50),`max_connections` 相应放大。大规模(>5 节点)建议用 **PgBouncer(transaction 池化)** 收敛业务池,但 **advisory-lock 池不可经事务池化**(锁随会话,必须直连),故 lockPool 连接数始终 = 单节点账号数,是 `max_connections` 的主导项。

校验:`go test -tags memory_gate ./internal/capacity/`(每会话内存基线门禁)。

---

## 2. 生产配置基线

### PostgreSQL

| 参数 | 基线 | 依据 |
|---|---|---|
| `max_connections` | 由 `Compute` 算出(见上) | lockPool 主导;务必覆盖峰值节点数 |
| `shared_buffers` | 物理内存 25% | 标准 |
| `work_mem` | 16–32 MB | 对账/聚合查询;连接数高时取保守值 |
| `idle_in_transaction_session_timeout` | **0(禁用)** | advisory-lock 连接长期持有事务外锁,**不可**被 idle 超时杀掉 |
| `tcp_keepalives_*` | 启用 | 及时发现断连 → 锁释放 → 加速接管 |

> `docker-compose.yml` 的 `max_connections=700` 仅为本地单节点开发基线(400<700),生产按 `Compute` 设定。

### Redis

| 参数 | 基线 | 依据 |
|---|---|---|
| `--maxmemory` | 512 MB 起(按规模上调) | asynq 队列 + 准入 Lua 计数键 |
| `--maxmemory-policy` | **`noeviction`(强制)** | asynq 任务被驱逐 = 已扣费消息丢失 |
| 持久化 | AOF `appendfsync everysec` | 接管去重键/队列需跨重启存活 |

Redis 内存推导(单节点 300 账号):`300×(64+64) + 1000×512 = 550 KB ×2 ≈ 2 MB` 实际占用;512 MB 基线为瞬时载荷 + 碎片留充足余量。

---

## 3. 滚动更新(零中断)

`maxUnavailable:0` + `maxSurge:1`:新 pod 就绪后旧 pod 才被终止。

```bash
# 部署/升级
kubectl apply -f deploy/deployment.yaml -f deploy/pdb.yaml
kubectl set image deployment/wadist wadist=wadist:<新tag>
kubectl rollout status deployment/wadist --timeout=300s   # 观察逐 pod 就绪

# 回滚(秒级)
kubectl rollout undo deployment/wadist
kubectl rollout history deployment/wadist
```

**停机时序**(每 pod,详见架构 §8):k8s preStop sleep 5s(端点传播窗口)→ SIGTERM → `/readyz`→503(~2s 摘端点)→ PreStopDelay 5s → `Supervisor.Shutdown` 排空在途发送 + `DeregisterNode` → 存活/新 pod 经 advisory-lock fencing 接管该 pod 账号。`terminationGracePeriodSeconds:45` ≥ 5+5+30+5。

> **本系统"零中断" = 账号会话连续性(M9 接管)**,非 HTTP 请求排空——asynq 工作是队列拉取。滚动期间被迁移账号在 ≤(扫描周期 15s + 起号)内被接管。

---

## 4. 金丝雀发布

cohort 为**观测维度**(`fnv64a(jid)%100<pct` 确定性分桶);灰度本身由 k8s 部署层完成,cohort label 用于**对比金丝雀 vs 稳定的发送质量**。

```bash
# 1. 给金丝雀负载设 10% cohort,新版本镜像
kubectl set env deployment/wadist-canary WADIST_CANARY_PERCENT=10
kubectl set image deployment/wadist-canary wadist=wadist:<新tag>

# 2. 观察对比(Prometheus):金丝雀 cohort 的失败/封号率不劣于稳定
#    sum(rate(wadist_cohort_sends_total{cohort="canary",outcome="sent"}[5m]))
#      / sum(rate(wadist_cohort_sends_total{cohort="canary"}[5m]))
#    对比 cohort="stable" 同比;若 canary send 成功率显著下降或 wa_warning 升高 → 回滚

# 3. 放量或回滚
kubectl set env deployment/wadist WADIST_CANARY_PERCENT=100   # 全量
# 或 kubectl rollout undo ...
```

判停指标(canary 显著劣于 stable 即止):`wadist_cohort_sends_total{cohort,outcome}` 的 sent 占比、`outcome="send_failed"` 增速、`wadist_health_signals_total{signal="wa_warning"}`。

---

## 5. 告警建议(PromQL,基于真实指标)

| 告警 | 表达式(示意) | 含义 |
|---|---|---|
| 账号容量逼近上限 | `wadist_active_sessions / on() WADIST_MAX_LOCK_CONNS > 0.9` | 单节点账号数接近 lockPool 上限,需扩节点 |
| 封号信号激增 | `rate(wadist_health_signals_total{signal="wa_warning"}[5m]) > 0` 持续 | 触发隔离的封号警告上升 |
| 准入拒绝率高 | `sum(rate(wadist_gate_decisions_total{allow="false"}[5m])) / sum(rate(wadist_gate_decisions_total[5m])) > 0.5` | 配额/pacing/隔离导致大量拒发 |
| 发送失败率 | `sum(rate(wadist_send_outcomes_total{outcome=~"send_failed|settle_failed"}[5m])) > 0` | 投递/结算失败 |
| 无容量积压 | `rate(wadist_dispatch_no_capacity_total[5m]) > 0` 持续 | 账号配额耗尽,recipient 滞留 pending |
| 钱包被锁(对账漂移) | `wadist_wallet_locked > 0` | 对账发现不变式漂移,资金已锁 |
| 退款审核积压 | `wadist_refunds_pending > <阈值>` | 失败退款待管理员审核堆积 |
| 代理枯竭 | `wadist_proxy_slots_free < <阈值>` 或 `wadist_proxy_dead` 上升 | 可用代理不足 |
| 账号健康恶化 | `wadist_accounts_health{bucket="at_risk"}` 上升 | 大量账号健康分逼近隔离阈值 |
| 节点失联 | `up{job="wadist"}` 缺失 / `/healthz` 失败 | 进程或节点故障(接管应自动发生) |

> 高基数护栏:指标按 `outcome/reason/state/cohort/bucket` 等有界维度聚合,**绝不含** jid/tenant_id/message_id/phone(`scripts/check_metric_labels.sh` 硬门禁)。租户级排障走日志/trace,不走指标。

---

## 6. 故障演练(混沌门禁)

`make chaos`(`TestTakeoverChaos -count=5`)端到端验证:杀掉持锁节点的连接 → advisory 锁自动释放 → 对端扫描器入队 → handler 重抢锁 + 改写 owner_node。CI(`release-gate.yml`)每次跑。生产演练可 `kubectl delete pod <持锁pod> --grace-period=0`(模拟硬死)观察接管时延(应 ≤ 扫描周期 + 起号)。

---

## 7. 前后端分离部署(klws.cc 同源拓扑)

前端 Next.js 与后端 Gin API **共用一个域名 `klws.cc`**,由 ingress 按路径分流,因此**无需任何 CORS / 跨域配置**:

```
Cloudflare ──HTTPS──▶ Ingress(klws.cc, console.yaml,装 klws-cc-tls)
    ├─ /api/*  ─▶ wadist-console  Service ─▶ Gin API Pod(:8080,路由本身即 /api/v1/...)
    └─ /       ─▶ wadist-frontend Service ─▶ Next.js Pod(:3000)
```

前端在**构建期**已把 `NEXT_PUBLIC_API_BASE_URL=/api/v1`(相对路径)烤进客户端包,运行期请求自然同源命中 `klws.cc/api/v1/...`。

### 构建镜像

```bash
# 后端(仓库根 Dockerfile,静态二进制 + distroless,内含 /console 与 /seed)
docker build -t wadist-console:latest .

# 前端(frontend/Dockerfile,standalone 产物)
docker build -t wadist-frontend:latest ./frontend
```

### 应用清单

```bash
kubectl apply -f deploy/console.yaml     # 后端 Deployment/Service + 同源 Ingress(/api 与 /)
kubectl apply -f deploy/frontend.yaml    # 前端 Deployment/Service
```

> 前置 Secret(`wadist-console-secrets`)、TLS(`klws-cc-tls`)见 [`console-secrets.example.yaml`](console-secrets.example.yaml)。同源拓扑下后端 `WADIST_CORS_ORIGIN` 留空即可(无需跨域)。

### 注入演示/初始数据(一次性 Job)

镜像内含 `/seed`,可作为一次性 Job 运行(复用同一套 DSN Secret):

```bash
kubectl run wadist-seed --rm -i --restart=Never \
  --image=wadist-console:latest --command -- /seed
# 或写成 batch/v1 Job,envFrom 引用 wadist-console-secrets。
```

---

## 8. 单机 docker-compose 一键编排(systemd 的替代方案)

[`../docker-compose.prod.yml`](../docker-compose.prod.yml) 把 **pg + redis + backend + frontend + proxy** 五件套收进一个文件,全部 `restart: always` + 健康检查 + 依赖排序,免去手工 systemd/容器管理。代理配置走 compose 服务名见 [`proxy/nginx.conf`](proxy/nginx.conf)。

```bash
cp .env.example .env       # 填密钥与 DSN(DSN 主机用服务名 postgres:5432 / redis:6379)
# 放置 Cloudflare Origin 证书:deploy/proxy/certs/{klws.cc.pem,klws.cc.key}
docker compose -f docker-compose.prod.yml up -d --build
docker compose -f docker-compose.prod.yml ps
```

**从现有 systemd 部署切换(有短暂停机)**:
```bash
# 1) 停掉手工栈,腾出 80/443/8080/3000
systemctl disable --now klws-backend klws-frontend
docker rm -f wadist-proxy
# 2) 起 compose(pg/redis 若复用现有数据,把卷指过去;否则先迁移+建 RLS 角色+seed)
docker compose -f docker-compose.prod.yml up -d --build
```

> 首次起 backend 前数据库需已迁移 + 建好 `app_tenant`/`app_system` 角色 + 跑 `cmd/seed`(否则 backend 因缺 RLS 角色 fail-closed)。`docker-compose.yml`(无 .prod)仍是 `make up` 用的 dev pg/redis,二者勿同时占端口。

**镜像内跑 seed**(backend 镜像内含 `/seed`,连内网 postgres):
```bash
docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml \
  run --rm --entrypoint /seed backend
```
> ⚠️ 必须带 `--entrypoint /seed`:backend 的 ENTRYPOINT 是 `/console`,不覆盖会被跑成 `/console /seed`(启动 API 服务器而非 seed,命令一直挂住)。

