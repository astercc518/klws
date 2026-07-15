# P5 养号系统 · 设计（Go 栈原生）

- 日期：2026-07-15
- 状态：已获用户设计批准（待 spec 复审）
- 范围：管理后台增加自动养号——养号引擎接生产 + 后台可视化/控制 + 自动化触发
- 技术路线（用户拍板）：**Go 栈原生重做**（不接 TS `p0-sending-unit`，TS 留作参考实现）；**三段一起大干**（一个 spec，P5a/P5b/P5c 分段落地）；养号内容采用**多轮对话脚本库**（防封最强）

---

## 1. 背景与目标

生产是 Go 单栈，发送体系数据轴为 `account_jid`（`account_devices` 表）。真机验证已坐实：新号立即发送会触发 WhatsApp 反封（`device_removed` / 401 conflict）。当前只有两层零散养号：

- **Layer 1（已在生产生效）**：`effective_quota(registered_at, health)` 号龄配额 ramp（SQL 函数 + Go `warmupQuota/EffectiveQuota` 同曲线）。只限流，不主动养。
- **Layer 2（未接生产）**：TS `p0-sending-unit/src/warmup/` 的 `WarmupService`（stage 机 / lane / pairAndWarm / 信号毕业），是独立 Prisma/Evolution 骨架。

**目标**：把养号做成发送体系里一个**独立子系统**，管住新号"从注册到能上业务量"的全生命周期，把封号损失挡在业务流量之前。北极星：单号存活时长 N 最大化、养号成本压到最低。

**非目标（YAGNI）**：不引入 TS runtime；不做跨栈数据同步；不做外部养号（加群/被动收信）——仅池内互发；不重写 Layer 1 号龄曲线。

---

## 2. 架构总览

```
                     ┌─────────────────────────────────────────┐
  扫码接入新号 ──────▶│ EnrollDeviceForInstance (已存在, FIX-3)   │
                     │        ↓ 自动 Enroll                       │
                     │  warmup_profiles (stage=WARMING)          │
                     └─────────────────────────────────────────┘
                                     │
      ┌──────────────────────────────┼───────────────────────────────┐
      │ worker tick (cmd/wadist)      │  webhook (backend)             │
      │  ├ PairAndWarm  ──sendText──▶ Evolution ──▶ 池内号互收         │
      │  │   (脚本库+抖动+打字态)                     │                 │
      │  └ EvaluateAndPromote                messages.upsert(inbound)  │
      │       ↓ 够信号                              ↓ RecordReply       │
      │  stage=MATURE ◀────────────────────────────┘                  │
      └───────────────────────────────────────────────────────────────┘
                                     │
                          ┌──────────▼───────────┐
                          │ selectaccount (业务发送) │  仅选 stage=MATURE
                          │   [WADIST_WARMUP_GATE]  │  特性开关, 灰度可关
                          └────────────────────────┘

  admin /admin/warmup ── 总览 / 号列表 / 手动控制 / 策略配置（读写 warmup_profiles + 策略表）
```

单元边界：
- `internal/warmup/`（纯逻辑：状态机、策略、服务、脚本库；依赖通过端口注入）——可独立单测。
- webhook 入站接线（`internal/api/webhook_evolution.go` 新增 `messages.upsert` case）——只负责把入站事件转成 `RecordReply`。
- worker tick（`cmd/wadist`）——只负责按周期驱动引擎。
- `selectaccount.go` 闸门——只负责在业务选号时按 stage 过滤。
- 前端 `/admin/warmup`——只负责可视化与控制，经 admin API 读写。

---

## 3. 数据模型

### 3.1 `warmup_profiles`（新表，migration `0024_warmup.sql`）

一号一行，主键 `account_jid`，外键引用 `account_devices(account_jid)`。

| 字段 | 类型 | 说明 |
|---|---|---|
| account_jid | TEXT PK | 号（= `account_devices.account_jid`） |
| tenant_id | TEXT NOT NULL | 租户轴（沿用全局 tenant 轴，RLS 一致） |
| lane | TEXT NOT NULL | `FAST` / `STANDARD` |
| stage | TEXT NOT NULL DEFAULT `NEW` | `NEW` / `WARMING` / `MATURE` |
| warmup_messages_sent | INT NOT NULL DEFAULT 0 | 养号发送累计（毕业信号） |
| replies_received | INT NOT NULL DEFAULT 0 | 养号入站累计（毕业信号） |
| online_since | TIMESTAMPTZ | 在线时长基准（enroll/上线时刻） |
| matured_at | TIMESTAMPTZ | 毕业时刻（算 mature ramp 天数） |
| warmup_sent_today | INT NOT NULL DEFAULT 0 | 当日养号发送额（与业务 `sent_today` **分离**） |
| warmup_sent_date | DATE | 当日养号计数所属日期（跨日归零） |
| paused | BOOLEAN NOT NULL DEFAULT false | 运营手动暂停 |
| created_at / updated_at | TIMESTAMPTZ | 审计 |

索引：`(stage)` 用于 tick 拉 WARMING 池；`(tenant_id, stage)` 用于后台列表。RLS 策略与 `account_devices` 一致（tenant 轴 + BYPASSRLS 系统池用于 worker 全局操作）。

**与 `effective_quota` 的关系**：业务配额仍由 `effective_quota(registered_at, health) - sent_today` 决定（Layer 1 不动）。warmup 只新增两个维度：(a) stage 闸门（谁能进业务轮转），(b) 养号发送独立计数（`warmup_sent_today`，不占业务额）。

### 3.2 策略配置（后台可配，不写死）

lane 阈值与 cap 存 `warmup_policies` 表（migration 同 0024），初始值从 TS `LANE_POLICIES` 植入：

| lane | minWarmupMessages | minReplies | minOnlineHours | warmingCap | matureBaseCap | matureMaxCap | matureRampStep |
|---|---|---|---|---|---|---|---|
| FAST | 2 | 0 | 2 | 5 | 15 | 20 | 5 |
| STANDARD | 20 | 5 | 36 | 8 | 20 | 40 | 5 |

后台改这些值 → 写库 → 引擎每 tick 读最新值（热生效，无需重启）。

### 3.3 脚本库 `warmup_scripts`（新表，同 0024）

| 字段 | 说明 |
|---|---|
| id PK | |
| lang | 语种/地区分组（`zh` / `pt` / `en` …），与配对号所在国家/代理国匹配 |
| turns | JSONB：多轮脚本，形如 `[{from:"A",text:"oi {name}, tudo bem?"},{from:"B",text:"tudo 👍 e vc?"},...]`，含变量占位（`{name}`/`{emoji}`）|
| enabled | BOOLEAN |
| created_at | |

初始植入 zh/pt/en 各若干套多轮脚本（问候→寒暄→表情→道别的一来一往）。后台可增删启停。

---

## 4. 养号引擎 `internal/warmup/`

纯逻辑 + 端口注入，照搬 TS 语义并做 Go 化增强。

### 4.1 `state.go` — 状态机
- 阶段 `NEW / WARMING / MATURE`，事件 `ENROLL / PROMOTE`。
- 迁移表：`NEW --ENROLL--> WARMING`，`WARMING --PROMOTE--> MATURE`，`MATURE` 终态。
- 非法迁移返回错误（不 panic）。
- 额外事件 `DEMOTE`（`MATURE --DEMOTE--> WARMING`）——P5c 封号自动降级用。

### 4.2 `policy.go` — 策略
- `LANE_POLICIES` 从库加载（含内存默认兜底）。
- `MeetsPromotionCriteria(signals, policy)`：`msgs>=min && replies>=min && onlineHours>=min`。
- `DailyCap(policy, stage, matureDays)`：NEW→0；WARMING→warmingCap；MATURE→`min(base+matureDays*step, max)`。

### 4.3 `service.go` — 服务（端口注入 profiles / accounts / evolution / clock）
- `Enroll(jid, lane)`：stage NEW→WARMING，`online_since=now`。
- `RecordReply(jid)`：`replies_received++`。
- `EvaluateAndPromote(jid)`：WARMING 且够信号 → PROMOTE，`matured_at=now`。
- `PairAndWarm(batch)`：拉 WARMING 池 → 过滤（ONLINE + 未暂停 + `warmup_sent_today < warmingCap`）→ 两两配对 → 每对挑脚本、逐句带抖动/打字态交替发送 → bump 计数。返回 `{pairs, messagesSent}`。
- `Demote(jid, reason)`：MATURE→WARMING，重置部分信号（P5c）。

### 4.4 `scripts.go` — 脚本库与防封节奏
一轮 pairAndWarm 对 (A,B)：
1. 按 A/B 所在国家/代理国选 `lang` 匹配的 enabled 脚本，随机挑一套。
2. 变量替换（称呼/emoji 随机）。
3. 逐 turn 交替发送：`from:"A"` → `evolution.SendText(A.instance, B.phone, text)`；`from:"B"` → 反向。
4. 每句之间 **随机 3–90s 抖动间隔**（用注入的 `*rand.Rand`，便于测试可注入确定性种子），发送前打字态 `SendTyping`（Evolution 已有 TODO 端点，本 spec 落地）。
5. A 发的每句，B 侧产生真实 inbound（webhook messages.upsert → RecordReply(B)），反之亦然——**毕业信号由真实入站驱动**。

抖动/多轮/多脚本使同批号内容与时序全部错开，消除农场特征。

---

## 5. 回复信号接线（webhook）

`internal/api/webhook_evolution.go` 现处理 `connection.update` / `messages.update` / `qrcode.updated`。**新增 `messages.upsert` case**：
- 解析入站消息（`data.key.remoteJid` 发信方、本实例 jid 收信方；注意 messages.upsert 是 NESTED 结构 `data.key.{id,fromMe}`，见 `webhook_evolution_types.go`）。
- `fromMe=false` 且收信方 jid 属于养号池（stage=WARMING/MATURE）且发信方也在池内 → `warmup.RecordReply(收信方 jid)`。
- 非池内入站（真实客户回信）不计入养号信号（未来可另做业务侧统计，本 spec 不涉及）。

---

## 6. 调度器（worker tick）

`cmd/wadist/main.go` 供应器加一个 `sup.Go` 养号 tick（仿现有 reconciler ticker，用 `SystemPool()` BYPASSRLS 做全局操作）：
- 周期可配 `WADIST_WARMUP_INTERVAL`（默认几分钟），`<=0` 关闭。
- 每 tick：
  1. `EvaluateAndPromote` 扫 WARMING 池，够信号自动升 MATURE。
  2. `PairAndWarm(batch)` 驱动池内互发。
- 新扫码号自动 Enroll：在 `EnrollDeviceForInstance`（FIX-3，扫码回填 device 之后）之后插入 `warmup_profiles`（stage=WARMING，默认 lane 可配）。幂等（`ON CONFLICT DO NOTHING`）。

---

## 7. 发送闸门（P5c 自动化闭环）

### 7.1 业务发送只选 MATURE
`internal/dispatch/selectaccount.go` 的选号 SQL 加 `JOIN warmup_profiles wp ON wp.account_jid=a.account_jid AND wp.stage='MATURE'`（NEW/WARMING 号绝不进业务轮转）。这是养号真正防封的落点。

### 7.2 特性开关（灰度安全）
`WADIST_WARMUP_GATE=on/off`（默认 `off`，灰度期可关，回退零风险）：
- `off`：selectaccount 不加 stage 闸门（当前行为，向后兼容）。
- `on`：加 `stage='MATURE'` 过滤。
- ⚠️ 这动 dispatch 引擎；红线在单机高密度重构（2026-07-02）时已解除，仍以开关兜底灰度。

### 7.3 封号自动降级
封号信号（`device_removed` webhook / health 掉到阈值以下）→ `warmup.Demote(jid)`（MATURE→WARMING 重养或隔离），从业务轮转自动摘除。降级由 webhook / health 监控触发。

---

## 8. 后台养号中心（前端 `/admin/warmup`）

复用 P1 ProDataTable / next-themes 主题 / i18n（zh/en）/ 亮暗。

- **总览卡**：各 stage/lane 号数、今日养号消息量、毕业率、平均在线时长。
- **号列表**（ProDataTable，带 selection/storageKey/filters）：jid / lane / stage / 号龄 / health / 养号进度（x/min 条 · y/min 回复 · 在线 h）/ 今日业务配额剩余（`effective_quota-sent_today`）/ paused / 状态。
- **手动控制**：enroll、改 lane、暂停/恢复、强制毕业（PROMOTE）、降级重养（DEMOTE）、调 health/配额。批量操作走 P1 selection。
- **策略配置页**：改 lane 阈值与 cap（写 `warmup_policies`，热生效）；脚本库增删启停（写 `warmup_scripts`）。
- 后端 admin API（`internal/api/warmup_api.go`）：列表（分页/筛选）、单号动作、策略读写、脚本读写。审计走现有 `recordAudit`。
- i18n key 补齐（zh/en 字典），过 `check-i18n-keys.mjs` gate（注意盲区：labelKey/map 间接 key 需人工核）。

---

## 9. 错误处理

- 引擎非法状态迁移 → 返回错误，API 层转 4xx，不影响 tick 其余号。
- PairAndWarm 单对发送失败（Evolution 429/503/超时）→ 跳过该对、记日志、不中断整批；节流码触发退避（复用 `IsThrottle`）。
- 脚本库为空 / 无匹配 lang → 该对跳过并告警（不 fallback 到写死单句）。
- webhook messages.upsert 解析失败 → 忽略该事件（回执/QR 主链路不受影响）。
- 闸门 `on` 但无 MATURE 号 → selectaccount 返回空，campaign 正常等待（不报错），后台总览可见"0 成熟号"提示。

---

## 10. 测试策略

- **引擎单测**（`internal/warmup/*_test.go`）：状态机全迁移 + 非法迁移；MeetsPromotionCriteria 边界；DailyCap ramp（各 stage/matureDays）。
- **PairAndWarm 集成测**（testcontainers pg + mock Evolution sendText）：配对逻辑、抖动/打字态调用、超额跳过、脚本变量替换、空脚本告警。
- **回复信号**：webhook messages.upsert → `replies_received` 落库；非池内入站不计。
- **闸门测**：`gate=on` 时 WARMING 号不被 selectaccount 选中、MATURE 号被选中；`gate=off` 行为不变。
- **自动 enroll**：扫码 EnrollDeviceForInstance 后 warmup_profiles 出现该号、幂等。
- **前端**：养号中心页渲染、手动动作、i18n gate 通过。

---

## 11. 实施分段（一个 spec，三段落地）

- **P5a — 养号中心 + 引擎骨架 + 自动 enroll**（不依赖账号池，立即可用）
  - migration 0024（三表）、`internal/warmup/`（state/policy/service，PairAndWarm 可先桩）、admin API + `/admin/warmup` 前端、扫码自动 enroll。
- **P5b — 主动养号真跑**（需账号池才真跑）
  - 脚本库 `scripts.go` + PairAndWarm 完整（抖动/打字态）+ webhook messages.upsert 回复信号 + worker 养号 tick + SendTyping Evolution 端点落地。
- **P5c — 自动化闭环**
  - selectaccount MATURE 闸门 + `WADIST_WARMUP_GATE` 开关 + 封号自动降级 + 策略/脚本热配。

每段独立可交付、可灰度。P5c 闸门默认 off，账号池起来、灰度验证后再开。

---

## 12. 风险与回退

| 风险 | 缓解 |
|---|---|
| 闸门开启后无成熟号 → 业务发不出 | 开关默认 off；后台"0 成熟号"提示；灰度先验 |
| 养号互发本身被判封 | 多轮脚本 + 随机抖动 + 打字态 + 分语种；warmingCap 保守 |
| 动 dispatch 引擎（红线邻域） | 已解除红线；特性开关兜底；闸门逻辑加单测覆盖 on/off |
| Layer 1 曲线与 warmup 双份配额语义混淆 | `warmup_sent_today` 与业务 `sent_today` 严格分离，文档标注 |
| 账号池为空时 P5b 无法真机验 | P5a 先落地拿运营价值；P5b 逻辑靠 mock Evolution 集成测保证正确 |

---

## 13. 与既有约束的一致性

- 数据轴 tenant_id（无 customers/customer_wallets），沿用 RLS。
- 计费不受影响：养号互发**不走计费**（`billing.Topup`/wallet_ledger 不触碰），仅业务发送计费。
- Evolution 数据面唯一（whatsmeow 已删）；SendTyping/SetPresence 复用现有 evolution_client TODO 端点。
- Go `warmupQuota`/SQL `effective_quota` 同曲线约束不变（本 spec 不改号龄曲线）。
