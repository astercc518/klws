# 发送链路深潜（send pipeline）

> 子系统深潜，配套 [ARCHITECTURE-DETAILED-zh.md](ARCHITECTURE-DETAILED-zh.md) §4.1。发送链路是贯穿 `dispatch → asynq → sendgate → billing → cluster → control` 的系统主干，也是计费/防封/幂等三条不变式的交汇点。本文给出时序图、状态机、失败-退款矩阵与幂等证明。

## 1. 参与者

| 角色 | 位置 | 职责 |
|---|---|---|
| **Dispatcher** | `dispatch`（`cmd/wadist` 1s ticker） | 拉 pending recipients、选号、claim、入队 |
| **asynq** | Redis(noeviction) | at-least-once 队列（`default:6` / `takeover:1`） |
| **SendWorker** | `dispatch`（asynq handler，并发 `AsynqConcurrency`） | 6 步处理单条发送 |
| **SendGate** | `sendgate` | Redis Lua 原子准入（日配额 + pacing）+ 隔离 |
| **billing.Repo** | `billing` | Hold/Settle/RequestRefund（冻结资金 + 幂等） |
| **RoutingSender** | `cluster` | 按 JID 路由到活跃 Session；fence + typing |
| **WorkingSet** | `control` | miss 时按 `warm:req` 反应式预热账号 |

## 2. 阶段一：派发（Dispatcher.dispatchBatch，单事务）

```
BEGIN
 ① 早清抑制: UPDATE campaign_recipients SET state='skipped'
      WHERE campaign_id=$c AND state='pending'
        AND EXISTS (suppression_list WHERE phone_bidx = recipients.phone_bidx)
 ② 拉取: SELECT id, phone, country, phone_bidx
      FROM campaign_recipients
      WHERE campaign_id=$c AND state='pending' AND assigned_jid IS NULL
        AND NOT EXISTS (suppression_list ... phone_bidx)      -- 二次防线
      ORDER BY id
      FOR UPDATE SKIP LOCKED LIMIT $batch                     -- 并发不阻塞
   对每条 recipient:
     ③ selectAccount(tenant, country):
          SELECT jid FROM account_devices
          WHERE tenant_id=$t AND ban_status='active'
            AND (quarantined_until IS NULL OR quarantined_until < now())
          ORDER BY effective_quota(registered_at, health_score) - sent_today DESC, random()
          LIMIT 1
        无可用号 → 记 dispatch_no_capacity 指标, recipient 留 pending, 下轮再试
     ④ UPDATE account_devices SET sent_today = sent_today + 1 WHERE jid=$jid   -- 分配即占配额
     ⑤ message_id := "$campaignID:$recipientID"
        UPDATE campaign_recipients SET assigned_jid=$jid, message_id=$mid WHERE id=$rid
     ⑥ enqueuer.EnqueueSend(SendPayload{campaignID, recipientID, jid, phone, mid, country})
COMMIT   -- 入队与 DB 变更同事务；入队失败 → 整批回滚(含 sent_today+1)
```

**为什么入队在事务内**：asynq 入队与 `assigned_jid/sent_today` 变更绑定原子提交。若进程在 COMMIT 前崩溃，一切回滚，recipient 仍 pending、`sent_today` 未涨——下轮重跑无副作用。若 COMMIT 后崩溃，任务已在 Redis，SendWorker 幂等消费。

## 3. 阶段二：发送（SendWorker.ProcessSend，6 步）

```
SendWorker          SendGate         billing          RoutingSender      cluster.Session
    │ 1. 终态守卫     │                │                    │                    │
    │  SELECT state ──┼── 已 sent/failed → return nil (no-op, 幂等吸收重投)
    │                 │                │                    │                    │
    │ 2. Admit ──────►│ Lua: q:{jid}:{date} < quota && now-p:{jid} >= min_gap
    │                 │  拒(daily_quota|pacing) → sent_today−1; requeue(delay); return  (不扣费)
    │◄── Ticket ──────│                │                    │                    │
    │                 │                │                    │                    │
    │ 3. Hold ────────┼───────────────►│ INSERT billing_charges ON CONFLICT(tenant,mid) DO NOTHING
    │                 │  balance→frozen │  失败(不足/钱包锁): Ticket.Release + requeue(回 pending, sent_today−1, 不扣费)
    │◄── Charge ──────┼────────────────│                    │                    │
    │                 │                │                    │                    │
    │ 4. 渲染 body = spintax.Expand(template) ; resolveMedia(按账号缓存)
    │    渲染/媒体失败 → RequestRefund(mid) + markFailed ; return (不重试)
    │                 │                │                    │                    │
    │ 5. Send ────────┼────────────────┼───────────────────►│ reg.Get(jid)?
    │                 │                │                    │  miss → warmReq(jid|cc)→warm:req; return ErrNoActiveSession
    │                 │                │                    │  fenceOnSend && !Healthy() → ErrLostOwnership (不发)
    │                 │                │                    │  typingOn → SetTyping(on)→sleepJitter→...→SetTyping(off)
    │                 │                │                    │──► sender.Send(phone, body, media) ──► whatsmeow
    │                 │                │                    │                    │
    │ 6. 结果分派 (worker.go:74-101):
    │   成功              → billing.Settle(mid) frozen→消费 ; markSent ; ApplyHealthSignal(delivered,+1) ; cohort 指标
    │   sendErr 且 ban    → ApplyHealthSignal(wa_warning,−30,6h) ; RequestRefund(mid) ; markFailed(sent_today−1) ; return err
    │   sendErr 且 非 ban → ApplyHealthSignal(undelivered)       ; RequestRefund(mid) ; markFailed(sent_today−1) ; return err
    │                       └─ 注意: ErrNoActiveSession / ErrLostOwnership 也走此路 → recipient 置 failed + 退款(非 requeue)
    │   Settle 失败       → return err (Settle 幂等, asynq 重试安全; recipient 仍 pending 未终态则下次守卫短路)
```

**关键更正（易误解处）**：`sender.Send` 返回的**任何** `sendErr`（含无会话 `ErrNoActiveSession`、丢锁 `ErrLostOwnership`）都走同一失败分支 —— `RequestRefund + markFailed`，recipient 直接置 **`failed`** 终态并 `sent_today−1`，**不是** requeue 回 `pending`。返回的 err 会让 asynq 重投，但重投在第 1 步终态守卫处被吸收为 no-op（recipient 已 failed）。warm miss 时的 `warm:req` 预热是**为下一次/其它 recipient 服务**，当前这条已 failed+退款。

**唯一的 requeue（回 `pending`、不扣费、`sent_today−1`）路径**只有两个：Admit 拒（配额/pacing，`worker.go:42`）与 Hold 失败（余额不足/钱包锁，`worker.go:57`，先 `Ticket.Release`）。

## 4. recipient 状态机

```
                    dispatchBatch 命中 suppression
        ┌──────────────────────────────────────────► skipped (终态, 绝不发送/扣费)
        │
   pending ──assign(assigned_jid,message_id)──► [in-flight, 仍 state=pending]
        ▲                                              │
        │  requeue: 仅 Admit拒 / Hold失败               │ ProcessSend
        │  (回 pending, sent_today−1, 不扣费/已释放)     │
        └──────────────────────────────────────────────┤
                                                        ├─ 成功                → sent   (终态; Settle 已扣费; sent_today 保留)
                                                        └─ 发送任何错误/渲染媒体错误 → failed (终态; RequestRefund 进审核; sent_today−1)
                                                             (含 ErrNoActiveSession / ErrLostOwnership)
```

- `in-flight` 不是独立枚举——recipient 仍是 `pending` 但 `assigned_jid IS NOT NULL`，故 dispatchBatch 的 `assigned_jid IS NULL` 谓词天然不会重复拉取它。
- 只有 `sent`/`failed`/`skipped` 是终态；`ProcessSend` 第 1 步守卫据此吸收 asynq 重投。

## 5. 失败-退款矩阵

| 失败点 | 是否已 Hold | 动作 | recipient | 计费影响 | asynq |
|---|---|---|---|---|---|
| suppression 命中 | 否 | 早清 | `skipped` | 无 | 不入队 |
| selectAccount 无号 | 否 | 留待下轮 | `pending` | 无 | 不入队 |
| Admit 拒(配额/pacing) | 否 | requeue(`sent_today−1`) | `pending` | 无 | 重投(delay) |
| Hold 余额不足/钱包锁 | 否 | `Ticket.Release` + requeue(`sent_today−1`) | `pending` | 无(未 Hold) | 重投 |
| 渲染/媒体失败 | 是 | RequestRefund + markFailed | `failed` | frozen→审核队列 | 重投被守卫吸收 |
| 无活跃会话(ErrNoActiveSession) | 是 | warm:req + RequestRefund + markFailed | `failed` | frozen→审核队列 | 重投被守卫吸收 |
| fence 丢锁(ErrLostOwnership) | 是 | RequestRefund + markFailed | `failed` | frozen→审核队列 | 重投被守卫吸收 |
| 发送封号信号 | 是 | 健康 −30(6h) + RequestRefund + markFailed | `failed` | frozen→审核队列 | 重投被守卫吸收 |
| 发送其他错误(非 ban) | 是 | 健康 undelivered + RequestRefund + markFailed | `failed` | frozen→审核队列 | 重投被守卫吸收 |
| Settle 失败 | 是 | return err(Settle 幂等) | `pending`→重投后 sent | frozen(重投时 Settle) | 重投 |
| 发送成功 | 是 | Settle + 健康 +1 | `sent` | frozen→消费 | 完成 |

**核对结论**（`worker.go:74-101`）：`sender.Send` 一旦被调用（即已 Hold），其返回的**任何** `sendErr` 都统一 `RequestRefund + markFailed`——recipient 置 `failed` 终态、退款进审核、`sent_today−1`。返回 err 触发 asynq 重投，但重投在第 1 步终态守卫短路为 no-op（recipient 已 failed），故净效果=至多一次退款。requeue（回 `pending`）**仅**发生在 Send 之前的 Admit 拒 / Hold 失败。

## 6. 三条不变式与证明

### 6.1 `message_id` 幂等（不重复扣费/发送）

`message_id = campaignID:recipientID` 全链路贯穿：
- **Hold**：`INSERT billing_charges ... ON CONFLICT(tenant_id, message_id) DO NOTHING` + 后续 SELECT 取现有 charge → 同 mid 二次 Hold 不再扣款。
- **ledger**：`wallet_ledger.idem_key` 唯一（`hold:{mid}` / `settle:{mid}`）→ 分录不重复。
- **发送守卫**：ProcessSend 第 1 步按 recipient 终态短路。
- **结论**：asynq at-least-once 重投 + dispatchBatch 重跑，均收敛到「至多一次真实发送、至多一次净扣费」。

### 6.2 `sent_today` 对称记账（配额不漂移）

```
分配(dispatchBatch ④):        sent_today += 1
非发送退出(Admit拒/无会话/丢锁): sent_today −= 1
真实发送成功:                  保留(+1 不回退)
真实发送失败(markFailed):      按实现回退或保留(配额已被"用掉")
```

净效果：`Σ sent_today` 恒等于「已真实占用配额的发送次数」，不会因重投/回退单调漂移，避免虚耗 `effective_quota` 导致号被误判超配。

### 6.3 冻结资金守恒（对账双不变式）

任一时刻：`wallet.frozen = Σ(billing_charges.amount WHERE state ∈ {held, refund_pending})`，且 `wallet.balance = Σ wallet_ledger.delta_balance`。发送链路每步都在单事务内同时改 wallet 与 charge/ledger，故快照恒等；`ReconcileTenant` 单 SQL 单 MVCC 快照校验，漂移即告警并可 `wallets.locked=TRUE` 拒后续 Hold。

## 7. 与控制面/防封的耦合点

- **温驻留**：发送 miss（`ErrNoActiveSession`）不是错误终点，而是把 `jid|cc` 推入 `warm:req`，`WorkingSet.Tick` 反应式预热该号后，asynq 重试即命中活跃 Session。主动侧由 `Scheduler.due:{cc}` 按配额均匀调度预热。
- **代理粘性**：预热经 `StickyBindProxy` —— 已绑代理则跳过，保证账号→代理终身不变，避免每轮换 IP 招封。
- **反指纹**：`AntifpOn` 时 Send 前后插入 typing 序列 + presence，`GracefulClose` 永不 `Logout()`。
- **熔断**：`riskbreaker` 旁路监控 running campaign 封号率，超阈值把 campaign 置 `paused`——Dispatcher 下一 tick 只发 running，天然停发，无需改 dispatch。

## 8. 关键调优旋钮

| 目标 | 旋钮 |
|---|---|
| 单节点在线号上限(pg) | `WADIST_MAX_LOCK_CONNS`(默认 300) |
| 发送并发 | `WADIST_ASYNQ_CONCURRENCY`(默认 32) |
| 派发批量 | `DispatchRunning(ctx, 100)`(代码内 batch=100) |
| pacing / 日配额 | `sendgate` 温号曲线 + `system_risk_config` 延迟参数 |
| 温驻留规模 | `WADIST_WARM_TARGET` / `WS_TICK_MS` / `DAILY_QUOTA_MIN/MAX` |
| 突破锁天花板 | `WADIST_OWNERSHIP_BACKEND=redis`(B2, opt-in, ~1.5万/节点) |
