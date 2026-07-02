# wadist 详细技术架构（代码级）

> 本文是 [ARCHITECTURE.md](ARCHITECTURE.md) 的代码级补充，基于真实代码（`cmd/wadist/main.go` 装配、16 个迁移、各包内部实现）编写，用于新成员快速建立完整心智模型。子系统深潜见 [ARCHITECTURE-SEND-PIPELINE-zh.md](ARCHITECTURE-SEND-PIPELINE-zh.md)。

## 0. 总览

多租户 WhatsApp 群发 SaaS。**吞吐不是瓶颈，封号率才是**——架构一切围绕「防封 + 精确计费 + 故障不双开」。技术栈：Go 1.26 / whatsmeow / PostgreSQL 16(pgx/v5) / Redis 7(asynq + Lua) / Gin / Next.js 16 / Prometheus。

三类可部署单元：
- **`cmd/wadist`** — 发信/控制面节点（真正连 WhatsApp、扣费、接管、温驻留）。
- **`internal/api`（Gin）** — 面向前端的 JSON API 薄层，只调引擎导出方法 + 改 DB。
- **`internal/console` + `frontend`** — 服务端渲染后台（较早）与 Next.js 前端。

## 1. 进程与运行时拓扑

```
Cloudflare ──► proxy(nginx :80/:443) ──┬─ /api/* ─► backend  :8080  (Gin API + cmd/wadist)
                                        └─ /     ─► frontend :3000  (Next.js)
       共享  PostgreSQL 16   +   Redis 7 (asynq队列 + 准入Lua + 所有权/控制键, AOF everysec, 2gb noeviction)
```

线上以 docker-compose 项目 `klws` 运行，5 容器全 `restart=always`。**铁律：这台机器上跑 compose 必须带齐两个 `-f`（prod + adopt），否则 postgres 会被重建到空卷、线上库瞬间“空掉”**（数据不丢，外部卷仍在，用两文件重跑即恢复）。

**`cmd/wadist` 装配序列**（`run()` 是可测试接缝）：

```
config.Load → metrics/prom registry → redis client
 store.Init(sc)  // 建4池 + 选 Ownership 后端 + shadow divergence 回调
 UpsertNodeHeartbeat(nodeID)
 [安全] MasterKey 非空 → crypto.KeyRepo+Cipher + audit.Writer + crypto.Router
        billingRepo.UseTenantRLS(mgr).UseAudit(aw)   // 否则 "security disabled"
 cluster.Registry → metrics.DBCollector(10s)
 sendgate: Admission(rdb) + SendGate(pool, adm, 3s)
 dispatch: AsynqEnqueuer → Dispatcher(pool, billing, enq, priceFor)
 RoutingSender: AntifpOn ? WithPolicy(fenceOnSend, typing) : plain
 SendWorker(pool, gate, billing, routing, uploader).WithMetrics/Canary/Pricing
 asynq.Server{concurrency, queues: default:6, takeover:1} + RegisterSendHandler
 receipt.Recorder(SystemPool); receiptsOn = WADIST_RECEIPTS!=off
 node.SessionFactory: GetDeviceStore → GetBoundProxy → NewWAConn(onReceipt) → Connect → (Antifp)SetPresence → Session
 Supervisor{StopIntake=asynq.Shutdown, AfterSessions=DeregisterNode, CloseStore, Flush, Limit, timeout}
 Orchestrator(mgr, reg, sup, factory) ; TakeoverEnqueuer ; RegisterTakeoverHandler
 [控制面] Scheduler(rdb) ; StickyBindProxy(GetBoundProxy, BindProxy)
          WorkingSet(rdb, sched, activeCountries, cfg, Deps{Warm,Evict,IsWarm,BindProxy})
          routing.WithWarmRequest(push jid|cc → warm:req)
 后台循环(sup.Go): SeedDue → ws.Run → dispatchLoop(1s tick) → riskbreaker.Run → RunHeartbeat → RunTakeoverScanner
 asynqSrv.Run(mux) ; metrics.Server.Start ; SetReady(true)
 stop(): SetReady(false)→sleep(PreStop)→sup.Shutdown→metrics.Shutdown→close redis/asynq
```

## 2. 分层与包依赖（23 包）

依赖只能自上而下；`internal/control` **零 import 五大引擎**（回调注入）。

```
cmd/wadist  cmd/console  cmd/seed              进程入口/装配
      │
internal/api (Gin)   internal/console          HTTP 层（薄，只调导出方法 + 改DB）
      │
internal/node                                  锁感知起号 + guardSession fencing + 心跳 + 接管
      │                                        ┌ internal/control  温驻留控制面(Scheduler/WorkingSet/ProxyAffinity)
internal/cluster                               │   ↑ 经 Deps 回调注入，无引擎依赖
      │                                        └ internal/riskbreaker 旁路熔断(只改campaign状态)
internal/dispatch ── internal/receipt          分发编排 / 回执回填
      │
internal/billing  sendgate  pricing  spintax  capacity  canary   计费/准入/定价/渲染/容量/分桶
      │
internal/store  crypto  audit  metrics                          PG四池+锁+代理+RLS / 信封加密 / 审计 / Prometheus
      │
internal/config  log                                            env→配置 / zap→waLog
```

**🔴 红线**：`billing / dispatch / store / sendgate / cluster` 五包核心业务逻辑禁改；上层只能调其导出方法、经 `SystemPool()` 做 seed 写入、复用 `WithTenant()` 做隔离。

## 3. 数据模型（16 迁移，全幂等）

| 迁移 | 关键对象 |
|---|---|
| 0001 | `account_devices`（jid/tenant/health/owner_node/ban_status）、`ban_status_t{active,flagged,banned,logged_out,init}` |
| 0002 | `proxy_pool`（is_alive/current_bindings/max_bindings/usage_count/latency）、`proxy_type_t`；devices 加 `proxy_id/proxy_url_cache` |
| 0003 | `tenant_wallets(balance,frozen)`、`billing_charges`、`wallet_ledger`、`refund_requests` + `charge_state_t{held,settled,refund_pending,refunded,rejected}`/`refund_state_t`/`ledger_kind_t` |
| 0004 | `reconciliation_runs`；wallets 加 `locked`（drift 锁钱包） |
| 0005 | devices 加 `registered_at/health_score/quarantined_until`（温号曲线 `effective_quota()`） |
| 0006 | `campaigns`/`campaign_recipients`/`campaign_templates`/`media_uploads` + `campaign_state_t`/`recipient_state_t{pending,sent,failed,skipped}`；devices 加 `sent_today` |
| 0007 | `cluster_nodes`（心跳）；devices `owner_node` 索引 |
| 0008 | **安全**：`tenant_keys`、`suppression_list`、`audit_log`(REVOKE UPDATE/DELETE)；角色 `app_tenant`(NOSUPERUSER)/`app_system`(BYPASSRLS)；9 表 RLS `FORCE` + `WITH CHECK`；PII 列 `phone_number_enc/phone_enc/phone_bidx` |
| 0009–0012 | `tenants`、`console_users`、`tenant_pricing`、序列授权修复（平台表 `REVOKE ALL FROM app_tenant`） |
| 0013 | devices `tags TEXT[]` + GIN 索引（IP/资源分组） |
| 0014–0015 | `system_risk_config`：延迟/日上限 + `circuit_breaker_enabled(默认false)`/`dry_run(默认true)`/`min_sample=20`/`window_seconds=900`/`eval_interval_seconds=20` |
| 0016 | recipients 加 `delivered_at/read_at` + `message_id` 索引（回执管道） |

**RLS 租户隔离表（9 张）**：account_devices、tenant_wallets、billing_charges、wallet_ledger、refund_requests、reconciliation_runs、campaign_templates、campaigns、campaign_recipients。策略 `tenant_id = current_setting('app.current_tenant_id', true)::bigint`。

## 4. 核心数据流

### 4.1 发送链路（端到端）

详见 [ARCHITECTURE-SEND-PIPELINE-zh.md](ARCHITECTURE-SEND-PIPELINE-zh.md)。摘要：

```
campaign(running) → Dispatcher.DispatchRunning(1s, batch=100)
  dispatchBatch(单事务): suppression置skipped → FOR UPDATE SKIP LOCKED拉pending →
    selectAccount(effective_quota-sent_today DESC) → sent_today+1 → claim message_id → 事务内asynq入队
  ↓ asynq(Redis noeviction; default:6/takeover:1)
  SendWorker.ProcessSend(6步幂等): 终态守卫 → Admit → Hold → 渲染+媒体 → RoutingSender.Send → Settle/Refund
```

两大不变式：① `sent_today` 对称记账（分配+1，非发送退出−1，真实发送保留）→ 配额不漂移；② `message_id` 全链路幂等 → at-least-once 重投不重复扣费/发送。

### 4.2 冻结资金状态机（billing）

```
Topup(ref幂等) ─► balance↑
Hold(message_id ON CONFLICT) ─► balance→frozen  [wallet FOR UPDATE; locked→拒; 不足→ErrInsufficientFunds]
   ├ Settle          frozen→消费          幂等: WHERE state='held'
   └ RequestRefund   frozen保留→refund_requests(charge_id UNIQUE)
         ├ ApproveRefund  frozen→balance   （同事务写audit）
         └ RejectRefund   frozen→消费       （同事务写audit）
```

- 幂等三重：`billing_charges(tenant_id,message_id)` 唯一、`wallet_ledger.idem_key` 唯一、`wallet FOR UPDATE`。
- **对账双不变式**（单 SQL、单 MVCC 快照）：`balance=Σledger.delta_balance`、`frozen=Σledger.delta_frozen=Σ(charges.amount WHERE state∈held,refund_pending)`；漂移→告警 + 可 `wallets.locked=TRUE`。

### 4.3 所有权、Fencing 与故障接管

**所有权抽象**（`store/ownership.go`，`WADIST_OWNERSHIP_BACKEND` 切换）：`Ownership{Acquire/Heartbeat/Deregister/StaleOwned/Unowned}`、`LockHandle{Healthy/Release}`。
- **pg（默认）**：`lockPool` 连接执行 `pg_try_advisory_lock(fnv64a(jid))`，持连至会话结束；`Healthy` 查 `pg_locks WHERE pid=pg_backend_pid() AND granted`（验证本 backend 仍持锁，非仅 Ping）；进程崩溃→连接断→PG 自动释放锁。
- **redis（B2, opt-in）**：Lua 原子 check-owner + 递增 fence token + `SADD owned:{node}`；`Heartbeat`=`SET node:hb:{node} EX 30s`；把 `MaxLockConns` 天花板从 ~2000 抬到 ~1.5 万。
- **shadow（灰度）**：以 pg 为准返回，redis 影子跑，不一致调 `OnShadowDivergence(op)`（接 metrics）。

**接管**（`node.Orchestrator`）：`RunTakeoverScanner` 取 `StaleOwned∪Unowned` → asynq `Unique` 入队去重 → `TakeoverHandler` → `StartAccountWithLock`：`Acquire` 成功才 `ClaimAccount+Registry.Add+guardSession`；被占用→`ErrDeviceLocked` 跳过。

**防双开核心**：advisory 锁（或 redis fence）是**唯一仲裁者**，心跳只是提示。分区下 stale-but-alive 节点仍持锁→接管被拒→不窃号。`guardSession` 周期 `Healthy()`，丢锁立即断会话；发送侧 `FenceOnSend` 发前查 `Healthy()`，失败返回 `ErrLostOwnership`（不触发底层 sender）。四重 fencing：fence token + WhatsApp 单连接 + `message_id` 幂等 + `Hold ON CONFLICT`。

### 4.4 控制面温驻留（control，替代全量起号）

工作集恒定规模（Little's Law：工作集 R × 温驻留 T_hold = 稳态发送率）。

- **Scheduler**（Redis ZSet `due:{cc}`，member=jid，score=nextMs）：`SeedDue` 批量播种；`PopDue` 用 Lua 原子 `ZRANGEBYSCORE ... LIMIT + ZREM`；`NextEligibleMs`=活跃窗口(9–22h)秒数/当日配额 + ±40% 抖动，硬下限 60s。
- **WorkingSet.Tick**（`WSTick` 周期）三段：① 反应式 `PopCount(warm:req,batch)`；② 主动式 各 cc `PopDue` 拉至 `ctl:resident` 达 `Target`；③ 驱逐 `IsWarm==false` 的 `SRem`+`Evict(linger)`。
- **回调注入**（main.go 真实装配）：`Warm=orch.StartAccountWithLock`、`Evict=session.GracefulClose(linger)`、`IsWarm=reg.Get`、`BindProxy=StickyBindProxy(GetBoundProxy,BindProxy)`（已绑则跳过 → **代理终身粘性，防每轮换 IP 招封**）。
- **双向收敛**：主动预热 due-ZSet（PULL）+ 发送 miss 推 `warm:req`（PUSH），不改 dispatch 即让活跃账号被预热。

### 4.5 防封号（sendgate + 反指纹）

- **准入 `Admit`**：① `ban_status!=active`→拒；② 在 `quarantined_until` 内→拒；③ Redis Lua 原子查 `q:{jid}:{date}`(日配额)+`p:{jid}`(pacing min_gap)，通过则 INCR/SET 发 Ticket。
- **健康度**：signal→delta（`wa_warning −30 / recipient_block −10 / conn_churn −5 / undelivered −2 / delivered +1`），夹 [0,100]；<40 自动 quarantine；`Heal` 周期 +5 并清过期隔离。
- **温号曲线** `effective_quota=warmupQuota(age)×health/100`（0–2d:20 / 2–4d:50 / 4–8d:100 / 8–15d:250 / 15d+:1000，下限 1）。
- **反指纹**（`AntifpOn`）：`SetPresence(online)` + 发送前 typing 序列（`TypingPolicy{Min,Max}` 抖动）+ `GracefulClose`（presence off→linger→Close，**永不 `Logout()`**）。`typingOn` 标志仅由 `NewRoutingSenderWithPolicy` 置位。

### 4.6 风控熔断（riskbreaker，旁路）

独立 side-car，`Run` 每 `eval_interval_seconds`（热重载 `system_risk_config`）：`enabled=false` 跳过；`pg_try_advisory_lock` 保证单节点评估；对 running campaign 算窗口内封号率=`failed 且 last_error 含 wa_warning|banned|403`/attempted，`≥threshold 且 attempted≥min_sample` 则 `UPDATE campaigns SET state='paused' WHERE state='running'`+同事务 audit。`dry_run=true` 只打印。**只翻 campaign 状态，零改引擎**。

### 4.7 回执管道（receipt）

`waConn` 注册 whatsmeow `events.Receipt` → `(messageIDs,kind,at)` → `Recorder.Record`：`UPDATE ... SET delivered_at=COALESCE(delivered_at,$at)`（read 同时回填 delivered），`WHERE message_id=ANY AND assigned_jid=$jid`。COALESCE 保最早、read 单调回填 → 幂等且 `delivered_at ≤ read_at`。`WADIST_RECEIPTS=off` 可整条回滚。

## 5. 并发与连接模型

**四池**（`store`）：

| 池 | 角色/DSN | 大小 | 用途 |
|---|---|---|---|
| `bizPool` | 主 DSN | MaxOpenConns=50 | 常规查询默认池 |
| `lockPool` | 主 DSN | MaxConns=300(=`MaxLockConns`) | 每把 advisory 锁钉一条连接，MaxConnLifetime=100 年 |
| `tenantPool` | `app_tenant`(RLS) | 同 biz；空则别名 bizPool | `WithTenant` 租户查询 |
| `systemPool` | `app_system`(BYPASSRLS) | 同 biz；空则别名 bizPool | 跨租户/平台表/seed |

- **`MaxLockConns`（默认 300）= 单节点在线账号硬顶**（pg 后端）；`MaxConcurrentStarts`/`AsynqConcurrency` 默认 32。
- **容量公式**（`capacity.Compute` 纯函数）：`accountsPerNode=MaxLockConns`；`connsPerNode=MaxLockConns+MaxOpenConns(biz)+MaxOpenConns(sqlDB)(+2×若双RLS DSN)`；`requiredMaxConnections=connsPerNode×nodesNeeded+headroom`；另算 `RedisMemoryMB`。

## 6. 安全与合规

- **信封加密**：KEK(`WADIST_MASTER_KEY` base64 32B) 包每租户随机 DEK(`tenant_keys`)；密文 `version(4B)||nonce(12B)||ct+tag`，版本前缀支持轮换；crypto-shred=删 DEK。⚠️ PII 明文列仍在（M11 未收缩），Shred 单独不满足 GDPR。
- **RLS 双角色**：`app_tenant`(FORCE RLS)/`app_system`(BYPASSRLS)；`WithTenant(ctx,tid)` 在 tenantPool 事务内 `set_config('app.current_tenant_id',...,local=true)`。**当前仅 billing 租户路径接入 RLS**；dispatch/sendgate/store-node/metrics 仍走 bizPool（分期债 ~24 处）。
- **PII**：`phone` 双写 明文+`*_enc`(per-tenant DEK)+`phone_bidx`(全局 HMAC 盲索引)；`UNIQUE(campaign_id,phone_bidx)` 去重 + suppression 拦截。
- **审计** `audit_log` append-only（REVOKE UPDATE/DELETE），退款审批同事务原子写。

## 7. API / 控制台层

- **Gin**，`/api/v1` 下 auth/tenant/campaigns/sales/admin 五组；中间件 `Recovery→CORS→requireAuth(Bearer=HMAC签名Redis会话ID)→requireRole`。角色 `admin/sales/customer`，密码 argon2id。
- **调引擎方式**：customer 数据必过 `mgr.WithTenant(tid)`；admin 跨租户走 `mgr.SystemPool()`；充值 `billing.Topup`、定价 `pricing.SetPrice/GetPrice`、campaign 插入带 `crypto.BlindIndex` 的 recipients（`ON CONFLICT(campaign_id,phone_bidx) DO NOTHING`）。改 campaign 状态即与 dispatch 通信（stop=paused/resume=running）。
- **Turnstile**：登录/注册/找回密码前置 Cloudflare 人机验证；未配 secret 或 CF 故障 **fail-open**。

## 8. 可观测性与生命周期

- **HTTP**（`metrics.Server` `:9090`）：`/metrics`、`/healthz`(恒200)、`/readyz`(排空时 503)。
- **指标全部有界 label**（`scripts/check_metric_labels.sh` 硬门禁禁 jid/tenant_id/message_id/phone）。
- **优雅停机**（`Supervisor.Shutdown`）：`SetReady(false)`→`sleep(PreStopDelay)`→StopIntake→cancel+await 后台循环→CloseAll(先 Disconnect 后 Release)→AfterSessions(DeregisterNode)→CloseStore→Flush→最后关 metrics HTTP。`maxUnavailable:0`+`maxSurge:1` 零中断，账号连续性靠接管迁移。

## 9. 配置面（33 个 `WADIST_*`）

DSN×4、`REDIS_ADDR`、`NODE_ID/REGION`、池(`MAX_OPEN_CONNS/MAX_LOCK_CONNS/MAX_CONCURRENT_STARTS/ASYNQ_CONCURRENCY`)、集群(`HEARTBEAT_INTERVAL/NODE_STALENESS/TAKEOVER_SCAN_INTERVAL`)、停机(`SHUTDOWN_TIMEOUT/PRESTOP_DELAY`)、金丝雀(`CANARY_PERCENT`)、安全(`MASTER_KEY/BLIND_INDEX_KEY`)、所有权(`OWNERSHIP_BACKEND/OWNERSHIP_FENCE_ON_SEND`)、反指纹(`ANTIFP/TYPING_MIN/MAX_MS/DWELL_MIN/MAX_MS/LINGER_MS`)、控制面(`WARM_TARGET/WARMREQ_BATCH/KEEP_WARM_HORIZON_MS/WS_TICK_MS/DAILY_QUOTA_MIN/MAX/COUNTRIES`)。默认 `OWNERSHIP_BACKEND=pg`、`ANTIFP=on`、`RECEIPTS=on`。

## 10. 已知偏差与债

1. **文档漂移**：原 ARCHITECTURE.md 曾写 15 包（现补正为 23）。
2. **RLS 未全量**：仅 billing 接入，dispatch/sendgate/store/metrics ~24 处待改。
3. **PII/GDPR**：明文列未收缩，crypto-shred 不足以擦除；per-tenant 盲索引键未做。
4. **能力桩**：`placeholderUploader`（无媒体上传）；`priceTable` 兜底；生产角色口令仍是迁移内 dev 值；`crypto.Router` 多区域为桩。
5. **控制面延后项**：redis `hbTTL=30s` 硬编码未与 `HeartbeatInterval` 解耦、Risk Governor AIMD、短链、国家分片未做。
6. **B2 迁移未完**：`owner_node` 列作只读镜像保留，DROP 迁移门控到生产 redis cutover 后（未执行）。
7. **代码只在本机**：`origin/main` 落后本地若干提交，近期工作未推远端。
