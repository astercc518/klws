# wadist 平台总览:技术架构 + 业务需求

> 本文是面向新成员/相关方的中文全景说明,综合自 [ARCHITECTURE.md](ARCHITECTURE.md)、[WHITEPAPER-MVP.md](WHITEPAPER-MVP.md)、各模块 plan(`docs/superpowers/plans/`)与代码现状。深入细节请回到对应源文件。

## 0. 一句话定位

**wadist 是一个多租户的 WhatsApp 批量营销 SaaS**:企业客户上传手机号、写好(带变体的)文案,平台把消息**分散到一池 WhatsApp 账号上、按受控速率发送**,**按条计费**、失败自动退款,并用代理池 + 健康度防封。核心难点是"在不触发 WhatsApp 风控(封号)的前提下规模化群发"。

技术上是**全栈 Go**,无前端构建链:靠 **PostgreSQL advisory lock** 保证"每个账号全集群只有一个节点在发",靠 **RLS** 保证租户数据隔离,靠**冻结资金模型(Hold→Settle/Refund)**保证账务可对账。

---

## 一、业务需求

### 1.1 三类角色与权限边界(安全核心)

| 角色 | 是谁 | 入口 | 能做什么 | 数据可见范围 |
|------|------|------|----------|--------------|
| **管理员 admin** | 平台运营 | `/admin*` | 建租户、配账号/代理、钱包充值(线下转账后台录入)、设各国单价、看所有租户余额/流水、**分配销售归属**、退款审批 | 全部租户(BYPASSRLS 系统池) |
| **销售 sales** | 业务员 | `/sales*` | 只管**自己名下**客户:看余额/用量/流水、按客户设各国单价 | 仅 `tenants.sales_owner_id = 自己`;每个按客户操作先过 `salesOwns()`,非名下一律 **403** |
| **客户 customer** | 付费租户 | `/`、`/send`、`/account` | 看自己钱包(可用+冻结)、上传号码、写 Spintax 文案+预览、看费用预估、提交发送、看自己账单/流水 | 仅自己租户(**数据库 RLS 强制**,9 张表) |

**两层授权**:每个受保护路由先过 RBAC(角色不符直接 403),再过数据层 —— 客户走 `app_tenant`(RLS 受限)池,管理员/销售走 `app_system`(BYPASSRLS)池但应用层按 `sales_owner_id` 过滤。即使应用层漏判,Postgres `FORCE ROW LEVEL SECURITY` 仍会挡住跨租户的行。

### 1.2 钱的模型(冻结资金 / 按条计费)

1. **充值 Topup**:客户线下打款 → 管理员录入金额+流水号 → `balance += amount`,记 `wallet_ledger(kind=topup)`。
2. **预估**:`估价 = 有效号码数 × 单价(租户×国家)`。单价查 `tenant_pricing`,未设则回退全局默认。
3. **冻结 Hold**:提交活动时事务内对钱包行加锁;余额不足 → `ErrInsufficientFunds`(直接拒,不扣);否则 `balance -= 估价, frozen += 估价`,记 ledger。
4. **结算 Settle**:每条成功 → `frozen` 扣减对应单价,charge 置 `settled`。
5. **退款 Refund**:每条失败 → `RequestRefund` 置 `refund_pending` → 管理员审批通过解冻回余额(或拒绝记 `rejected`)。活动结束剩余冻结自动释放。

**关键不变量**(对账依据):`balance ≥ 0`、`frozen ≥ 0`、`balance + frozen = Σ ledger`、`frozen = Σ 未结清 charges`;`billing_charges` 上 `UNIQUE(tenant_id, message_id)` 保证**重试不重复扣费**。每晚 `ReconcileTenant` 校验,漂移则告警 + 可锁钱包(锁后 Hold 拒绝新活动)。

### 1.3 发送业务流程(客户视角)

```
上传号码 → 规范化/去重/过滤退订 → 写文案(Spintax) → 预览变体 → 看预估&余额 → 提交
```

- **号码处理**:规范化(E.164-ish,8–15 位)、去重、**按盲索引过滤退订名单**、报告"有效/重复/已过滤/非法"数。
- **Spintax**:`{Hi|Hello}` 随机选一,`{{name}}` 变量替换;**按 message_id 做种**,重试时同一条消息渲染结果一致。
- **提交**:同一事务内建 campaign/recipients + Hold 冻结;活动置 `running`,后台 dispatcher 自动接管。

### 1.4 业务规则 / 合规

| 规则 | 落地方式 |
|------|----------|
| 不透支 | `CHECK(balance≥0)` + Hold 余额不足即拒 |
| 不重复扣费 | `UNIQUE(tenant_id,message_id)` + ledger 幂等键 |
| 失败必退 | 发送出错先 `RequestRefund` 再返回 |
| 退订永不再发 | `suppression_list` 按盲索引过滤,发送前剔除 |
| 租户隔离 | 9 表 `FORCE RLS`,客户只走 `app_tenant` 池 |
| 可审计 | `audit_log` 追加写(`REVOKE UPDATE/DELETE`),管理动作入账 |
| 防 CSRF | 所有变更 POST 走双提交 token |
| 密码 | argon2id(64MiB / t=1 / p=4) |

---

## 二、技术架构

### 2.1 总体拓扑:两个二进制,共享 PG + Redis

| 二进制 | 角色 | 职责 | 对外 HTTP |
|--------|------|------|-----------|
| **cmd/wadist** | 分发**工作节点**(多副本集群) | 持有 advisory 锁、连 WhatsApp 活会话(whatsmeow)、跑 dispatch 循环、处理发送、节点接管 | 仅 `/metrics /healthz /readyz` |
| **cmd/console** | **Web 管理/门户**(低副本) | 管理员/销售/客户页面、登录会话、写 campaign/钱包/定价 | 全站 HTTP(:8080),带 RLS 隔离 |

**无状态**:状态全在 Postgres(可锁的行)与 Redis(任务队列+限速)。节点通过 ① 轮询 `campaign_recipients`、② asynq 队列、③ 心跳→陈旧检测 发现工作。

### 2.2 各包职责

| 包 | 职责 |
|----|------|
| `config` | 环境变量 → 强类型 Config(DSN/上限/间隔/密钥) |
| `store` | PG 四池(biz/lock/tenant/system)、advisory 锁、代理绑定、whatsmeow sqlstore |
| `crypto` | AES-256-GCM 信封加密(每租户 DEK)、HMAC 盲索引、密钥轮换、区域路由 |
| `audit` | 追加写审计日志(RLS 强制不可改) |
| `billing` | 冻结资金钱包:Hold/Settle/Refund/Topup、对账、退款审批 |
| `sendgate` | 计费前准入闸:预热配额曲线、健康分(0–100)、Redis Lua 原子限速、隔离 |
| `dispatch` | 活动调度:拉收件人(过退订)、选号(健康加权)、渲染、入 asynq;SendWorker 端到端处理 |
| `cluster` | 活会话注册表、Supervisor 生命周期、whatsmeow 封装(WAConn)、路由发送 |
| `node` | 分布式编排:抢锁起会话、fencing 守卫循环、takeover、心跳/扫描 |
| `pricing` | 租户×国家单价查询(最小货币单位) |
| `spintax` | `{a\|b\|c}` 展开,保留 Go `{{.var}}` |
| `metrics` | Prometheus 采集(有界标签)+ DB 聚合循环 + 健康端点 |
| `capacity` | 容量公式(账号→节点→连接→Redis 内存) |
| `canary` | 确定性分桶(fnv64a%100)做灰度 |
| `log` | zap 适配 whatsmeow 日志接口 |
| `console` | Web 服务器、登录/会话、RBAC、RLS 中间件、html/template + htmx |

### 2.3 数据层

**Postgres(pgx/v5,11 张表)四连接池:**

- `bizPool`(~50):业务查询/分析,可走 PgBouncer
- `lockPool`(~300):**每个账号固定占用一条 advisory 锁连接**;`MaxLockConns` = 单节点账号上限
- `tenantPool`(角色 `app_tenant`,RLS 受限):事务内 `set_config('app.current_tenant_id')`
- `systemPool`(角色 `app_system`,BYPASSRLS):跨租户管理/后台

**9 张租户表 `FORCE ROW LEVEL SECURITY`**:`account_devices / tenant_wallets / billing_charges / wallet_ledger / refund_requests / reconciliation_runs / campaigns / campaign_templates / campaign_recipients`。平台级表(`proxy_pool / cluster_nodes / tenants / console_users / tenant_pricing`)不带 RLS。迁移全幂等(`migrate_twice.sh` CI 门禁验证)。

**Redis(asynq + 限速)**:两队列 `default`(发送,优先级6)/`takeover`(接管,优先级1,`asynq.Unique` 集群去重);限速/配额用 Lua 脚本集群原子;**必须 `maxmemory-policy noeviction`**(任务被淘汰 = 已冻结资金的消息丢失)。

**三重隔离**:RLS(行级)+ 加密分片(删租户 DEK 即密文不可恢复)+ PII 双写(`phone` 明文 / `_enc` 密文 / `_bidx` 盲索引去重)。

### 2.4 分布式:锁、围栏、接管

- **唯一仲裁是 advisory 锁,心跳只是提示**。抢锁:`AcquireDeviceLock(fnv64a(jid))` 成功才起会话;被持有则 `ErrDeviceLocked` 跳过。
- **Fencing(防双开)**:`DeviceLock.Healthy()` 查 `pg_locks` 确认本后端真持锁;连接被透明重连 → 新后端无锁 → Healthy=false → 守卫循环(~10s)杀会话。双开窗口 ≤ 守卫间隔,有 `TestTakeoverChaos` 验证。
- **Takeover**:扫描器每 15s 找陈旧/无主账号 → 入 takeover 队列(集群去重)→ handler 重新抢锁起会话;锁仍被陈旧节点持有则 no-op。
- **容量公式**:`max_connections ≈ (MaxLockConns + 2×MaxOpenConns)×节点数 + 余量`。**lockPool 不能经 PgBouncer 事务池化**(锁随会话,重连即丢锁),故锁连接数是 `max_connections` 主导项,也是扩展瓶颈。
- **优雅停机/滚动发布**:`/readyz`→503 → 等 PreStopDelay → 停收任务/排空 → 取消后台循环 → 关会话(断 whatsmeow→放锁)→ 注销节点 → 关池。k8s `maxUnavailable:0/maxSurge:1` 零停机,账号经 takeover 迁移。

### 2.5 WhatsApp 发送链路

```
campaign(running) → Dispatcher.DispatchRunning(每~1s)
  → 拉 pending 收件人(盲索引过滤退订)→ 健康加权选号 → sent_today+1、定 message_id → 入 asynq
    → SendWorker.ProcessSend(并发32):
       幂等检查(已 sent/failed 跳过)
       → sendgate.Admit(Redis Lua:日配额+pacing,不过则退避重排,不扣费)
       → billing.Hold(冻结,message_id 幂等)
       → renderBody(模板+spintax,按 message_id 做种)
       → RoutingSender.Send → whatsmeow 真发
          成功        → Settle + 标记 sent + 健康+1
          wa_warning  → 健康-30 + 隔离6h + RequestRefund + 标记 failed
          其它错误    → RequestRefund + 健康-2 + 标记 failed + 重试
```

**双层幂等**:DB 唯一约束 + 应用状态机。**防封两层**:健康加权选号 + Spintax 文案打散。

### 2.6 安全 / 加密

- **KEK/DEK 信封**:32 字节主密钥(`WADIST_MASTER_KEY`)封装每租户 DEK(`tenant_keys`),密文 `version||nonce||GCM`,支持版本化轮换。
- **盲索引**:`phone_bidx = HMAC(WADIST_BLIND_INDEX_KEY, phone)`,用于去重(`UNIQUE(campaign_id,phone_bidx)`)与退订匹配,不暴露明文。
- **审计**:`audit_log` 追加写,与业务操作同事务写入。
- **加密分片**:删 DEK = 密文永久不可恢复(注意:明文 PII 列尚未收缩,见技术债)。

### 2.7 可观测与运维

- **日志**:zap JSON,whatsmeow 与各包共用一个 logger。
- **指标**:Prometheus,**严禁高基数标签**(`scripts/check_metric_labels.sh` CI 门禁禁止 jid/tenant_id/message_id/phone)。计数器如 `wadist_send_outcomes_total{outcome}`、`wadist_gate_decisions_total`、`wadist_health_signals_total`、`wadist_billing_ops_total`;DB 聚合(10s 缓存)含活会话数、各状态账号/收件人/charge、钱包余额/冻结、待退款积压。
- **K8s**:`/healthz` liveness、`/readyz` readiness、preStop sleep、`terminationGracePeriodSeconds:45`、PDB。控制台清单见 [../deploy/console.yaml](../deploy/console.yaml)。

---

## 三、模块完成度

| 模块 | 业务能力 | 状态 |
|------|----------|------|
| M1–M5 | 账号注册/RLS双池、代理池、钱包计费、对账、防封健康 | ✅ |
| M6 / A | 调度+真实发送(纯文本)、Spintax、结算/退款 | ✅(文本) |
| M7–M9, M11 | 可观测、优雅停机、集群接管、容量/灰度 | ✅ |
| M10 | 数据安全(加密/盲索引/退订/审计) | ⚠️ 部分(明文 PII 列待收缩) |
| B | Web 登录/会话/RBAC/RLS 基座 | ✅ |
| C-1 | 按租户定价 + 管理员充值 | ✅ |
| D | 销售控制台(名下客户/详情/定价) | ✅ |
| E-1 | 客户门户基座(RLS fail-closed、CSRF、仪表盘) | ✅ |
| E-2 | 客户发送流(上传/去重/退订/预估/提交) | ✅(文本) |

**MVP 整体已打通**:客户登录→上传→发送→扣费 端到端可用。

---

## 四、关键权衡与技术债

**非显然的设计决策**

- advisory 锁是唯一仲裁,心跳只是提示 → 强一致防双开,但锁池成为扩展瓶颈。
- 冻结资金模型 + 双层幂等 → 不透支、不重复扣、失败必退,代价是账务复杂。
- 全栈 Go + htmx → 零前端构建、单二进制;交互复杂时不如 SPA 灵活。

**已知债务 / 未做**

- 媒体(图片/文件)上传发送(当前纯文本)
- 明文 PII 列收缩(当前明文+密文双写,GDPR 彻底擦除未完成)
- 客户自助注册(当前管理员建租户)
- 控制台内建"新建租户/账号/代理"页面(当前靠 SQL/seed)
- 在线支付网关(当前线下充值 + 人工录入)
- `effective_quota` 在 Go 与 SQL 各维护一份,需手动保持同步(无自动测试)
