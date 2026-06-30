# 设计文档：WhatsApp 送达/已读回执接入（delivered / read 漏斗）

> 状态：**待评审 / 待授权**（必须触碰红线包 `internal/cluster`，需用户明确授权后实施）
> 关联：触达穿透抽屉已落地（`internal/api/campaign.go` 的 `loadRecipients` + 前端 `campaign-detail-sheet.tsx`），当前漏斗只有"提交 → 已发送 → 失败"。本设计补齐"已送达 / 已读"两级。
> 作者：架构助手　日期：2026-06-29

---

## 0. TL;DR

- 目标：把 WhatsApp 的 **delivered / read 回执**接入系统，让任务漏斗从"提交 → 已发送"延伸到"提交 → 已发送 → **已送达 → 已读** / 失败"，逐号可见送达与已读时间。
- 可行性核心已验证：`campaign_recipients.message_id` 存的就是 **whatsmeow 真实消息 ID**（`waConn.Send` 返回 `resp.ID`，`markSent` 写入），回执事件 `events.Receipt.MessageIDs` 能直接匹配。
- 红线现实：回执事件只到达 `internal/cluster` 里的 whatsmeow client，**必须在 cluster 注册事件 handler**——这与熔断器不同，无法做纯旁路。本设计把红线触碰压到**一个极薄的注入点**（NewWAConn 接受一个 `onReceipt` 回调 + 注册 handler 转发），所有业务逻辑放在**新包 `internal/receipt`**（非红线）。
- 数据模型推荐**加性时间戳列**（`delivered_at` / `read_at`），**不改** `recipient_state_t` 枚举、不改发送状态机——因为"已发送/已送达/已读"是**递进里程碑**而非互斥状态，时间戳模型最自然、最低风险。

---

## 1. 现状（取证依据）

- **无任何 whatsmeow 事件处理**：全仓库无 `AddEventHandler` / `events.Receipt`（已 grep 确认）。系统当前完全不消费回执。
- **发送链路与真实 message_id**：
  - `internal/cluster/conn_whatsmeow.go:53-64` `waConn.Send` → `client.SendMessage(...)` → `return string(resp.ID)`（whatsmeow 真实消息 ID）。
  - `internal/dispatch/worker.go:152-160` `markSent` → `UPDATE campaign_recipients SET state='sent', message_id=$2`（$2 = 上面的真实 ID）。
  - `internal/dispatch/batch.go:82` 派发时先写临时占位 `fmt.Sprintf("%d:%d", campaignID, r.id)`，**markSent 时被真实 ID 覆盖**。故 `state='sent'` 的行 `message_id` 一定是真实 WA ID。
- **连接生命周期 / 注入点**：
  - `internal/cluster/conn_whatsmeow.go:28-42` `NewWAConn` 创建 `whatsmeow.NewClient`；`Connect` 调 `client.Connect()`。
  - `cmd/wadist/main.go:162-180` 的 `SessionFactory` 内创建连接并 `conn.Connect()`（:176）。**事件 handler 必须在 `Connect()` 之前 `AddEventHandler`**。
  - `internal/node/orchestrator.go:68` `factory(ctx, jid, lock)` 是会话创建扩展点；`Registry` 仅管理活跃会话，**无事件总线/回调机制**。
- **"engine 事件 → DB 写"的现有范式**：`internal/sendgate/sendgate.go:43-70` `ApplyHealthSignal` 直接用 pool `UPDATE account_devices ...`。可作为 recorder 写库的参照。
  - 注意：`healthDelta` 已含 `"delivered": +1`，但 `worker.go:100` 在**发送成功即刻**调用 `ApplyHealthSignal(jid,"delivered")`——这其实是"已离开本系统"，**并非真实送达**。见 §7 可选修正。
- **数据表**：`migrations/0006_dispatch.sql:27-41`，`campaign_recipients` 有 `message_id`、`assigned_jid`、`state`、`updated_at`；枚举 `recipient_state_t = (pending,sent,failed,skipped)`（:5）。**无 delivered_at/read_at/receipt 列**。

---

## 2. 红线立场

| 动作 | 落点 | 是否红线 |
|---|---|---|
| 加 `delivered_at`/`read_at` 列 + 索引 | `migrations/00XX`（加性） | 否 |
| 回执 → recipient 的关联与写库逻辑 | **新包 `internal/receipt`** | 否（新代码 + 既有表 SQL） |
| 漏斗计数/明细返回 delivered/read | `internal/api/campaign.go`（已存在的 `loadRecipients`） | 否 |
| 前端漏斗加两级 + 明细列 | 前端 | 否 |
| **注册 whatsmeow 事件 handler 并转发回执** | `internal/cluster`（NewWAConn/Connect） | **是（必须，最小化）** |
| 装配 recorder、注入回调 | `cmd/wadist`（装配层） | 否 |
| （可选）把真实"delivered"健康分挪到回执路径 | `internal/dispatch/worker.go` + `internal/sendgate` | **是（可选，建议单独授权）** |

**为什么这次必须碰红线**：回执是 whatsmeow client 的异步事件，client 由 `internal/cluster` 持有并在其内部 `Connect`。没有任何办法在 cluster 之外拿到这些事件。我们能做的是把红线触碰**收敛成一个注入点**：`NewWAConn` 多收一个 `onReceipt func(receipt.Event)` 回调，并在 `Connect` 前 `AddEventHandler` 一个只做"翻译 + 转发"的 handler；**零业务逻辑进 cluster**，全部逻辑在 `internal/receipt`。

---

## 3. 数据模型（加性，非红线）

推荐**时间戳里程碑**模型，不动枚举、不动发送状态机：

```sql
-- migrations/00XX_recipient_receipts.sql （示意）
ALTER TABLE campaign_recipients ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;
ALTER TABLE campaign_recipients ADD COLUMN IF NOT EXISTS read_at      TIMESTAMPTZ;
-- 回执按 message_id 关联，建索引；assigned_jid 用于消歧
CREATE INDEX IF NOT EXISTS idx_recip_msgid ON campaign_recipients (message_id);
```

语义：`sent` 行随回执依次补 `delivered_at`、`read_at`。三者是**递进里程碑**（read ⇒ delivered ⇒ sent），不是互斥状态，所以无需改 `recipient_state_t`、不影响 dispatcher（它只 select `pending`）。漏斗与明细在 api 层用这两列**派生**出 delivered/read。

> 为什么不扩枚举：扩 `recipient_state_t` 并推进 state 会引入"read 比 delivered 先到怎么办""sent 计数与 delivered 计数互斥吗"等状态机歧义，且枚举值变更是引擎语义变更。时间戳列是加性、幂等、可乱序，明显更稳。

---

## 4. 架构

```
            WhatsApp 服务器
                  │  events.Receipt (delivered/read)
                  ▼
   internal/cluster/conn_whatsmeow.go  (红线, 极薄改动)
     client.AddEventHandler(func(evt){
        if r, ok := evt.(*events.Receipt); ok {
            onReceipt(receipt.Event{           // ← 翻译成自有类型, 不外泄 whatsmeow 类型
              MessageIDs: r.MessageIDs,
              SenderJID:  ownAccountJID,        // 本连接的账号 = campaign_recipients.assigned_jid
              Kind:       mapKind(r.Type),      // delivered | read
              At:         r.Timestamp,
            })
        }
     })
                  │ onReceipt 回调 (cmd/wadist 注入)
                  ▼
   internal/receipt  (新包, 非红线)
     Recorder.Record(ctx, ev)  → 幂等 UPDATE campaign_recipients
                  │ 仅通过 SQL 写既有表
                  ▼
   campaign_recipients.delivered_at / read_at
                  ▲
                  │ api 层 loadRecipients 派生
   GET /campaigns/:id/recipients → 漏斗 + 明细 (delivered/read)
```

要点：
- **每个回执到达拥有该连接的节点**（assigned_jid 归该节点），handler 就地写库——**天然分布式，无需跨节点协调、无需 advisory lock**。
- cluster 只认一个自有 `receipt.Event` 类型，不把 whatsmeow 类型外泄到其它包。

---

## 5. 关联与写库（internal/receipt）

```go
package receipt

type Kind string
const (
    Delivered Kind = "delivered"
    Read      Kind = "read"
)

type Event struct {
    MessageIDs []string  // events.Receipt.MessageIDs（真实 WA 消息 ID）
    SenderJID  string    // 本连接账号 JID = campaign_recipients.assigned_jid
    Kind       Kind
    At         time.Time
}

type Recorder struct{ pool *pgxpool.Pool } // 用 BYPASSRLS 的 SystemPool（跨租户、回执不带 tenant）
func New(pool *pgxpool.Pool) *Recorder

// Record 幂等关联：按 message_id + assigned_jid 命中 recipient，COALESCE 保留最早时间。
func (r *Recorder) Record(ctx context.Context, ev Event) error
```

写库 SQL（幂等、可乱序、批量）：

```sql
-- delivered
UPDATE campaign_recipients
   SET delivered_at = COALESCE(delivered_at, $3)          -- set-if-null：保留最早
 WHERE message_id = ANY($1) AND assigned_jid = $2;

-- read（read ⇒ delivered，回填 delivered_at 以保证里程碑单调）
UPDATE campaign_recipients
   SET read_at      = COALESCE(read_at, $3),
       delivered_at = COALESCE(delivered_at, $3)
 WHERE message_id = ANY($1) AND assigned_jid = $2;
```

- **为何带 `assigned_jid`**：`message_id` 无全局唯一约束，理论上跨 campaign 可能重复；加来源 JID 消歧（与发送账号一致）。
- **幂等**：`COALESCE` 使重复回执无副作用。
- **批量**：`ANY($ids)` 一条语句处理回执里的多个 MessageIDs。
- **whatsmeow `events.Receipt.Type` 映射**：`ReceiptTypeDelivered`（空串）→ delivered；`ReceiptTypeRead` / `ReceiptTypeReadSelf` → read；`ReceiptTypePlayed`（语音已播放）→ 可选忽略或单列扩展。实现时以所用 whatsmeow 版本常量为准。

装配（`cmd/wadist`，非红线）：

```go
rec := receipt.New(mgr.SystemPool())
// SessionFactory 内创建连接时注入：
conn := cluster.NewWAConn(device, logger, proxyBinding, func(ev receipt.Event) {
    if err := rec.Record(ctx, ev); err != nil { log.Printf("receipt: %v", err) }
})
```

---

## 6. API 与前端（非红线，最小增量）

**API**：`loadRecipients`（`internal/api/campaign.go`）已是客户/超管共用。改动：

- 计数 SQL 增两项：
  ```sql
  count(*) FILTER (WHERE delivered_at IS NOT NULL) AS delivered,
  count(*) FILTER (WHERE read_at      IS NOT NULL) AS read
  ```
- 明细 SELECT 增 `delivered_at::text, read_at::text`；每行派生 `status` 里程碑：`read_at?→"read" : delivered_at?→"delivered" : state 映射`。
- `recipientsResponse.counts` 自然多出 `delivered`/`read` 两键（前端已按 map 读取，向后兼容）。

**前端**：`campaign-detail-sheet.tsx` 已为可扩展结构。改动：

- 漏斗加两节点：提交 → 已发送 → **已送达 → 已读** / 失败（用 `counts.delivered`、`counts.read` + 转化率）。
- 明细表加"送达/已读时间"列；`STATUS` 增 `delivered`（蓝/positive）、`read`（更深 positive）两态徽章。
- 删除当前抽屉里"送达/已读回执暂未接入"的免责文案。

---

## 7. 可选：修正"delivered"健康分（红线，建议单独授权）

当前 `worker.go:100` 在发送成功即刻给 `+delivered` 健康分（语义不准）。接入真实回执后，可：
- 把发送成功的即时信号降级为中性（或移除），
- 在 `receipt.Recorder` 命中 delivered 时再发 `ApplyHealthSignal(jid,"delivered")`（更真实的号码健康度）。

这会改 `internal/dispatch/worker.go` 与 `internal/sendgate`（防封评分），属红线，**与回执漏斗解耦**，建议作为后续单独项。回执漏斗本身不依赖它。

---

## 8. 失败模式与边界

1. **回执早于 markSent**（极少）：receipt 引用真实 ID，但此刻 DB 里还是占位串/未提交 → UPDATE 命中 0 行，回执丢失。缓解：发送提交通常先于回执；可加"短期重试缓冲"或接受极小丢失（先记 metric）。
2. **乱序回执**（read 先于 delivered）：`COALESCE` + read 回填 delivered_at，里程碑保持单调，无害。
3. **占位 message_id 残留**：未成功发送的行 message_id 是占位串，永远不会被回执命中（回执只认真实 ID），无误关联风险。
4. **多账号/多节点**：回执到达拥有连接的节点就地写库，无需协调；不同节点不会争抢同一行（assigned_jid 唯一归属）。
5. **量级**：回执高频；批量 `ANY($ids)` + `message_id` 索引控制成本。
6. **隐私**：不新增 PII（phone 已存）。
7. **连接重连/handler 重复注册**：handler 在 `Connect` 前注册一次；重连走新连接新注册，旧连接关闭即失效（参照 `cluster/types.go` Close）。

---

## 9. 实施计划（分阶段）

| 阶段 | 内容 | 红线 |
|---|---|---|
| 0 | 迁移加 `delivered_at`/`read_at` + 索引 | 否 |
| 1 | 新包 `internal/receipt`（Recorder + 幂等 SQL + 单测） | 否 |
| 2 | `internal/cluster` 注入点：`NewWAConn` 增 `onReceipt` 参数 + `Connect` 前 `AddEventHandler` 转发 | **是（最小）** |
| 3 | `cmd/wadist` 装配 recorder + 注入回调 | 否 |
| 4 | `loadRecipients` 计数/明细加 delivered/read；前端漏斗+列+徽章 | 否 |
| 5（可选） | 健康分修正（§7） | 是（单独授权） |

阶段 0/1/4 可先做（零运行时影响：列空着、API 多返回 0）；阶段 2/3 是让数据真正流起来的红线步，需授权。

---

## 10. 测试计划

- **Recorder 单测**（一次性 Postgres，沿用 `scripts/migrate_twice.sh` 隔离）：
  - 插入 `sent` recipient（带真实 message_id + assigned_jid）→ `Record(delivered)` → `delivered_at` 置位；`read_at` 仍空。
  - `Record(read)` → `read_at` 置位且 `delivered_at` 被回填（单调）。
  - 重复 `Record` → 时间戳不变（幂等 COALESCE）。
  - `message_id` 命中但 `assigned_jid` 不符 → 不更新（消歧）。
  - 乱序：先 read 后 delivered → delivered_at 不覆盖更早的 read 回填值。
- **映射单测**：whatsmeow `Receipt.Type` → `Kind`。
- **API 单测**：`loadRecipients` 计数含 delivered/read（psql 实证，沿用本次穿透验证手法）。
- **回归**：dispatch/sendgate 单测不受影响（阶段 2 仅加注入点，不改发送逻辑）。

---

## 11. 回滚

- 列为加性，空值无害；API 多返回的 delivered/read 在无数据时为 0。
- 红线注入点可用一个 env 开关（如 `WADIST_RECEIPTS=off`）控制是否注册 handler；关闭即停止写入，发送链路完全不受影响。
- 镜像回滚无需回滚 schema。

---

## 附：关键文件锚点

| 项 | file:line |
|---|---|
| `waConn.Send` 返回真实 resp.ID | internal/cluster/conn_whatsmeow.go:53-64 |
| `NewWAConn` / `Connect`（注入点） | internal/cluster/conn_whatsmeow.go:28-42 |
| `markSent` 写真实 message_id | internal/dispatch/worker.go:152-160 |
| 占位 message_id（被覆盖） | internal/dispatch/batch.go:82 |
| SessionFactory / Connect 装配 | cmd/wadist/main.go:162-180 |
| Orchestrator / factory 扩展点 | internal/node/orchestrator.go:35-84,:68 |
| "事件→DB写"范式 ApplyHealthSignal | internal/sendgate/sendgate.go:43-70 |
| recipients 表 / 枚举 | migrations/0006_dispatch.sql:27-41,:5 |
| 穿透查询（待加 delivered/read） | internal/api/campaign.go（loadRecipients） |
| 明细抽屉（待加两级漏斗） | frontend/components/campaign-detail-sheet.tsx |
