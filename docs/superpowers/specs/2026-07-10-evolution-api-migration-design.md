# 数据面换栈 — 剥离 whatsmeow/wabadger 接入 Evolution API（Evolution API Migration）

**日期**: 2026-07-10
**分支**: `main`（spec 落库）→ 实现另起分支 `feat/evolution-migration`
**状态**: 设计已确认（用户 2026-07-10 拍板全量替换），待实现

---

## 1. 背景与动机

wadist 现状：Go 单体，控制面（计费/风控/分发调度/代理池）+ 数据面（whatsmeow 直管 WS、wabadger 落 Signal 会话）同进程。M1–M7 高密度重构 + 双栈坍缩（`1b699fb`）已把 whatsmeow+BadgerDB 固化为**唯一**路径。

**决策**（用户 2026-07-10 拍板）：把数据面替换为开源 **Evolution API**（Node.js/Baileys）微服务。Go 保留为纯控制面，经 **REST** 调 Evolution 发消息/管实例，Evolution 经 **Webhook** 回推登录/回执/掉线。Session 落 Evolution 侧外部 **Redis**，Go 只留 `instance_name↔jid` 映射与账号状态。

### 1a. 架构师异议存档（已当面提出，用户选"全量替换"）

本 spec 忠实执行用户决策，但以下三点风险**记录在案**，防未来会话据本 spec 产生"换栈解决了内存/防封"的幻觉：

1. **动因倒置（内存/CPU）**：whatsmeow（goroutine + `coder/websocket`）每连接比 Baileys（Node/V8 每实例一 socket）**更省**。Evolution 单机现实密度是**百~低千实例**，非 10 万。换栈后单账号内存**上升**，10 万账号池**必须横向铺多 Evolution 节点**（见 §7 E5）。
2. **动因倒置（防封可控性）**：现有反封控制环（governor/seg-τ/ghost-reaper/backoff/antifp + `PresenceConn`/`LivenessConn`/`manageLifecycle` 关自动重连）正因自持 whatsmeow 层才拿到细粒度控制；REST 边界**丢失**部分控制，需在 Go 侧重建等效钳位（见 §7 E4）。
3. **真正未决风险未被换栈解决**：项目自身结论是「吞吐从不是瓶颈，封号率才是」（[高吞吐发送设计]），而 Evolution 不改善封号率。真机 ramp 封号率标定**仍空白**——换栈后仍需补（见 §9）。

> 参见 [Evolution API 转向] 记忆。曾建议的更稳路径（Evolution 做成 `cluster.Conn`/`dispatch.Sender` 第二实现、canary 对真实封号率灰度）被否决，本 spec 走全量。

## 2. 目标 / 非目标

**目标**
- Go 端不再 import `whatsmeow`/`badger`；数据面全部行为经 Evolution REST + Webhook 完成。
- 复用现有抽象接缝零改调用方：`dispatch.Sender`、`cluster.Conn`/`PresenceConn`/`LivenessConn`、`receipt.Recorder`、`store.ProxyBinding`、`sendgate.SendGate` 签名不变。
- 保留全部正确性不变式（§3）与反封控制环语义（在 REST 层重建等效钳位）。
- 支持多 Evolution 节点分片，为 10 万账号池留出横向扩展位。

**非目标 / 明确不做**
- 不写 Badger→Baileys 会话迁移工具（决策：**接受账号全量重扫码**，与坍缩 spec 同一笔账，见 §8）。
- 不在 Go 内重实现 Baileys 协议逻辑——协议层完全托管给 Evolution。
- 不改计费/风控/分发的业务语义（红线不变式照旧）。
- 本 spec 不含运营侧 ramp 标定流程（上线后做，不阻塞代码）。

## 3. 必须存活的正确性不变式（护栏）

删除/替换过程中以下不变式**必须继续成立**，由 `make gate` 回归守护：

1. **计费**：`balance = Σledger`、`frozen = Σ未结charge`。
2. **幂等**：`message_id` 幂等去重。**换栈关键点**：`message_id` 现来自 Evolution 返回的 `key.id`（== 真实 WA 消息 id），发送时落库，webhook 回执用同一 `key.id` 匹配，保持回执对称。
3. **对称记账**：`sent_today` 加减对称。
4. **防双开**：单账号单会话。**换栈后 fence 语义迁移**：一个 jid 同时只允许一个 `instance_name` 处于 connected，且只有一个 Go 节点持有其 redis 所有权租约（`ownership_redis.go` 不变）——Evolution 侧不做双开保护，Go 控制面负责不对同 jid 建第二个 connected 实例。
5. **门禁**：`make gate` 全绿（`tidy vet test-race labels vuln`）。

## 4. 顶层架构

```
┌─────────────────────────── Go 控制面（本仓库）───────────────────────────┐
│  dispatch(pump+governor)  billing  sendgate  receipt  store(proxy/redis)  │
│        │Sender接口              │                 ▲Recorder                │
│        ▼                        ▼                 │                        │
│  evoSender ──REST──►      cluster.WhatsAppClient  │        api/webhook ◄───┼── Webhook
│  (sender_evolution.go)    (evolution_client.go)   │        (webhook_evolution.go)
│        │                        │                 │                        │
│        └──── instanceResolver (jid↔instance↔node 分片路由) ────────────────┘
└──────────────┬──────────────────────────────────────────┬─────────────────┘
               │ REST /instance/* /message/*               │ POST 回调
       ┌───────▼────────┐  ┌────────────────┐      ┌───────▼────────┐
       │ Evolution 节点1 │  │ Evolution 节点N │ …    │ 事件: connection│
       │ Baileys实例×N   │  │ Baileys实例×N   │      │ /messages.update│
       │   └─Redis(会话) │  │   └─Redis(会话) │      └────────────────┘
       └────────────────┘  └────────────────┘
```

- **Go→Evolution**：REST，per-instance 并发闸 + HTTP 连接池上限 + 分类退避（§7 E4）。
- **Evolution→Go**：Webhook `POST /api/v1/webhook/evolution`，HMAC 校验，幂等落库。
- **会话**：Evolution 侧 Redis（`CACHE_REDIS_ENABLED`）。Go 侧 `account_instances` 只存路由/状态，**不存 Signal 态**。

## 5. Go 侧数据模型（新增）

新表 `account_instances`（替代 whatsmeow device / badger 会话在 Go 侧的位置）：

| 列 | 类型 | 说明 |
|---|---|---|
| `instance_name` | text PK | Evolution 实例主键（Go 生成，稳定；建议 `wa_{tenant}_{shard}_{seq}`）|
| `jid` | text NULL | WA JID，配对成功后由 `connection.update` 回填；`UNIQUE WHERE jid IS NOT NULL` 防双绑 |
| `tenant_id` | bigint | 归属租户（RLS 轴，见 [klws tenant 为轴]）|
| `evo_node` | text | 分片到哪个 Evolution 节点（E5 用；E0 先单节点常量）|
| `proxy_id` | bigint NULL | 粘在此实例的 `proxy_pool.id`（终身粘性）|
| `state` | text | `created`/`qr`/`connected`/`disconnected`/`loggedOut` |
| `created_at`/`updated_at` | timestamptz | |

迁移文件：`migrations/00NN_account_instances.sql`（replay-all 幂等模型：`CREATE TABLE IF NOT EXISTS` + `information_schema` guard，见坍缩 spec 迁移约定）。RLS：按 `tenant_id` 加 policy，与既有表一致；receipt/webhook 走 `SystemPool()` BYPASSRLS。

## 6. 核心接口（符号级）

见附录 A（`WhatsAppClient` 完整定义已在会话中给出，落地时置于 `internal/cluster/evolution.go`）。关键接缝：

- `dispatch.Sender.Send(ctx,jid,phone,body,media)→(msgID,err)` —— `evoSender` 实现，`msgID = SendResult.RemoteID (key.id)`。
- `cluster.Conn{Connect,Disconnect}` + 可选 `PresenceConn`/`LivenessConn` —— `evoInstance` 实现；`Connect=create+connect`、`Disconnect=停本地路由不 logout`、`Liveness` 读 webhook 维护的状态缓存。
- `receipt.Recorder.Record(receipt.Event)` —— webhook handler 喂，签名不变。
- `store.ProxyBinding{ProxyID,ProxyURL,ProxyType,Country}` —— `CreateInstance` 拆成 `proxyHost/Port/Protocol/Username/Password` 传 Evolution，终身粘性只首次/代理死重建。

## 7. 分模块替换顺序（E0–E6，每模块独立可测可合并）

> 每个 Em 一份 TDD 计划 `docs/superpowers/plans/2026-07-1x-em-*.md`，subagent 驱动。全程 `make gate` 绿、whatsmeow 默认路径直到 E6 cutover 才删。

- **E0 — 骨架/地基**（本 spec 首个计划）：`account_instances` 迁移 + `instanceResolver`（jid↔instance↔node 缓存，store 层）+ `evolution_client.go` HTTP 客户端（create/connect/logout/delete/sendText 薄封装，含 `http.Transport` 池上限）+ webhook 路由（**只校验+log，不落库**）+ Evolution 配置（base URL/apikey/webhook secret/node）。零发送行为变更。`make gate` 绿。
- **E1 — 实例生命周期 Conn**：`conn_evolution.go` 实现 `Conn`/`PresenceConn`/`LivenessConn`；`CreateInstance` 传代理（拆 `ProxyBinding`）；`ConnectInstance`/QR；`LogoutInstance`/`DeleteInstance`。挂进 cluster registry，门控 `WADIST_CONN=whatsmeow|evolution`（默认 whatsmeow）。
- **E2 — evoSender**：`sender_evolution.go` 实现 `dispatch.Sender`（sendText/sendMedia，返回 `key.id`）；`retrySender` 退避装饰器。门控 `WADIST_SENDER=whatsmeow|evolution`（默认 whatsmeow）。
- **E3 — Webhook 入库**：`webhook_evolution.go` 全量：`connection.update`（state + jid 回填 + offline→`sendgate.ApplyHealthSignal`）、`messages.update`（ack 翻译→`receipt.Record`）、HMAC 校验、幂等。从 E0 的 log-only 升级为落库。
- **E4 — 背压/反封重建**：per-instance `semaphore(1~2)`、错误分类（429/503→governor 乘性减；400 not-connected→health signal；loggedOut→permanent 不重连）、per-instance 熔断。复用 `pump`/`governor`/`sendgate`。
- **E5 — 多节点分片**：一致性哈希 `instance→evo_node`，每节点容量上限，`instanceResolver` 据此路由。填 §1a 的密度落差。
- **E6 — cutover + 拆除**：默认门控翻到 evolution；删 `conn_whatsmeow.go`、`internal/store/wabadger`、`go.mod` 去 `whatsmeow`/`badger`；停用/DROP whatsmeow 会话表；账号全量重扫码 runbook。

## 8. 边界决策

- **8a 会话迁移**：不写迁移工具，**接受账号全量重扫码**（Badger Signal 态无法迁到 Baileys）。与坍缩 spec 同一笔账，别付两次。
- **8b message_id 来源**：用 Evolution `POST /message/sendText` 响应里的 `key.id`（== 真实 WA 消息 id）作 `campaign_recipients.message_id`。**不**用 Evolution 自造的内部 uuid，否则回执匹配断链。若某版本响应不含 `key.id` 只含 status，E2 需回退到 webhook `messages.upsert`(fromMe) 补 id——落地时按实测 Evolution 版本定，spec 记为待验证点。
- **8c 代理粘性**：`CreateInstance` 传一次代理，**只在代理死亡才 delete+recreate 实例**。绝不 per-send 换 IP（[高吞吐发送设计] 头号雷）。
- **8d 温驻留 > churn**：Evolution 实例 create/delete 比 whatsmeow 更贵（起 Node socket + Redis 态）。维持工作集常驻、慢速轮转。
- **8e Webhook 安全**：prod 必须设 HMAC secret；Evolution 会重投，靠 `receipt.Record` 的 `COALESCE` 幂等兜底，但仍须验签防伪造。
- **8f 不删 asynq**：takeover 队列（`internal/node/takeover.go`）仍在 asynq 上，与本次正交，保留。

## 9. 验收与未决

- **验收**：每模块 `make gate` 全绿；E1 单账号回环（create→扫码→发一条→webhook 回执落 `campaign_recipients`，验 message_id 对称）；E3 webhook 幂等重投测试；E4 分类退避单测（假 HTTP）；E6 后 `grep -r whatsmeow internal/ = 0`。
- **未决点**（不阻塞 E0）：① Evolution 版本的 webhook 事件名/ack 语义需实测钉死（`messages.update` vs `messages-update`、`ack` 数值 vs `status` 字符串）；② `key.id` 是否随发送响应返回（8b）；③ 多节点分片再平衡策略（E5）；④ 真机 ramp 封号率标定（运营，仍空白，换栈不解决）。

## 附录 A：`WhatsAppClient` / `evoSender` / webhook handler

见会话内已给出的三段 Go 代码（`internal/cluster/evolution.go` 的 `WhatsAppClient` 接口 + `InstanceInfo`/`SendResult`/`InstanceState`；`internal/dispatch/sender_evolution.go` 的 `evoSender`；`internal/api/webhook_evolution.go` 的 `EvolutionWebhook` + `translateAck`）。E0–E3 计划将逐段 TDD 落地。

相关：[Evolution API 转向] [klws 高吞吐发送设计] [klws 部署拓扑] [klws 单机高密度重构] [klws tenant 为轴]
