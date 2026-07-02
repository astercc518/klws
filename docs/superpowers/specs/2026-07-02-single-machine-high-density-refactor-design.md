# 单机高密度架构重构 —— 技术方案与数据结构映射表

- 日期：2026-07-02
- 状态：设计草案，待评审（不写实现代码，确认后逐模块替换）
- 目标：单机（64C/128G/1T NVMe/单 IP）支撑 10 万账号池、~2 万并发（1 万预热 + 1 万发送）、**日发百万**。
- 授权：本次重构由用户显式解除「五大引擎禁改」红线，可重写 `billing/dispatch/store/sendgate/cluster` 内部。但**必须保留的正确性不变式**见 §8。
- 新核心栈：**Go + whatsmeow + BadgerDB + Redis(ZSET/Lua) + PgBouncer**。

---

## 0. 现状核对（已读 `internal/store`、`internal/dispatch`、`cmd/wadist` + whatsmeow store 接口）

| brief 任务 | 现状 | 结论 |
|---|---|---|
| T1 会话 store 降维 | 现走 `sqlstore.NewWithDB(sqlDB,"postgres")` + `container.Upgrade`，16 张 `whatsmeow_*` 表；`GetDeviceStore/NewDeviceStore` 经 `container` | **需新写 BadgerDB adapter 实现 whatsmeow store 全部接口**（本文档核心，见 §3） |
| T2 防双开锁 | **已有** `ownership_redis.go`：Lua + **fence token(INCR)** + `node:hb` 心跳判活，比裸 TTL 键安全 | **复用/微调，不要退化成裸 `lock:jid` TTL** |
| T2 代理冷却圈 | 现 `BindProxy` 走 PG `FOR UPDATE SKIP LOCKED`，`ORDER BY usage_count, latency`；有 `max_bindings/current_bindings/is_alive/failure_count` 容量与健康 | 改 Redis ZSET 冷却圈，但**须保留容量上限与死代理排除**（见 §4） |
| T3 滚筒波调度 | 现 `DispatchRunning`（1s ticker）→ `dispatchBatch`（PG SKIP LOCKED 拉 recipients）→ asynq 入队；控制面 `control.WorkingSet` 已做温驻留 warm | 新增内存令牌桶 Pump，**替换 asynq 传输 + 脉冲，复用 sendgate/billing 计费闸**（见 §5） |
| T4 Boot Jitter / Ghost 下线 | 现 `SessionFactory` connect 后立即可用；`GracefulClose`=presence off→linger→Close（**不 Logout**） | 新增 boot jitter；Ghost 下线**须做有界化**，否则连接/FD 泄漏（见 §6，最大风险点） |
| T5 Lua 退避 | **已有** admission Lua（日配额 + pacing）；`ApplyHealthSignal` 已有 wa_warning−30 + quarantine | 指数退避是**加法扩展** admission（见 §7） |
| T5 PgBouncer | 现 `DeviceLock` 在 lockPool 连接上持 **session 级 advisory lock 跨查询** | **与 PgBouncer transaction 模式不兼容**；T1+T2 移除 advisory lock 后天然对齐（见 §7） |

**总判断**：这不是推倒重来，而是「**存储引擎降维（PG/SQL→Badger+Redis）+ 调度模型换血（批处理/asynq→内存滚筒波）+ 反封增强**」。控制面（Scheduler/WorkingSet/代理粘性）与计费不变式大量可保留。

---

## 1. 目标态架构（单机高密度）

```
┌───────────────────────────── 单进程 wadist 节点 ─────────────────────────────┐
│ 控制面                                                                        │
│  ① Rolling-Wave Pump   内存令牌桶 τ≈160/s，平滑摊销 2 万并发到 60s            │
│  ② Working-Set Mgr     维持 R≈1 万温驻留（复用现 control.WorkingSet）         │
│  ③ Eligibility Sched   Redis due:{cc} ZSET（复用现 control.Scheduler）        │
│  ④ Risk Governor       health/ban/receipt → 调 τ / L3 段熔断（新增）          │
│  ⑤ Ghost Reaper        有界回收「幽灵」连接（新增，见 §6）                    │
│ 数据面                                                                        │
│  cluster.RoutingSender → whatsmeow.Client（每号一条，绑独立代理）             │
│  sendgate.Admit(Redis Lua) │ billing Hold/Settle │ receipt │ riskbreaker      │
│ 存储                                                                          │
│  BadgerDB(本地 NVMe)  = whatsmeow Signal 会话状态（高频）                     │
│  Redis                = 防双开锁(fence) + 代理冷却 ZSET + due ZSET + 限速 Lua │
│  PostgreSQL(经 PgBouncer, txn 模式) = 账号元数据/计费/审计/campaign（低频）   │
└──────────────────────────────────────────────────────────────────────────────┘
```

**读写分层原则**：高频变动的 Signal 状态 → Badger（NVMe KV，零事务锁）；瞬时协调（锁/冷却/限速）→ Redis（内存）；低频且需强一致的钱/审计/任务态 → PG（经 PgBouncer，短事务）。

---

## 2. 并发预算（单机 2 万并发 / 日百万）

| 量 | 值 | 说明 |
|---|---|---|
| 稳态 τ | ~160/s | 令牌桶目标；日百万 16h 窗口仅需 ~17/s，160/s 留峰值余量 |
| 工作集 R | ~1 万温驻留 + ~1 万发送态 | Working-Set 维持；Badger 承接其 Signal 状态读写 |
| Ghost 窗口并发 | **须计入**：160/s × ghost_ttl(建议≤30s) ≈ 4800 半死连接 | FD/内存/代理 socket 预算必须含此项（§6） |
| Badger | 10 万账号 × 每号数十 KB~MB Signal 状态；value-log 在 NVMe | `SyncWrites` 策略是关键取舍（§3.4） |
| PG 连接 | pgxpool ≤ 200，经 PgBouncer txn 模式 | 移除 advisory lock 后可行（§7） |
| 内核 | `ulimit -n≈200K`、`nf_conntrack_max`、`ip_local_port_range`、`somaxconn` | 2 万+ghost 连接必调 |

---

## 3. 【Task 1】whatsmeow Store → BadgerDB 适配器（核心）

### 3.1 要实现的接口面

whatsmeow `Device` 由 13 个子 store + 1 个 `DeviceContainer` 组成。适配器须实现全部：

`DeviceContainer`(GetDevice/GetAllDevices/PutDevice/DeleteDevice/NewDevice) + `IdentityStore` `SessionStore` `PreKeyStore` `SenderKeyStore` `AppStateSyncKeyStore` `AppStateStore` `ContactStore` `ChatSettingsStore` `MsgSecretStore` `PrivacyTokenStore` `NCTSaltStore` `EventBuffer` `LIDStore`。装配经 `device.SetAllStores(...)`，替换 `sqlstore.Container`。

### 3.2 数据结构映射表（whatsmeow 16 表 → Badger keyspace）

单节点**一个** BadgerDB 实例；按账号 JID 前缀分区，前缀扫描即「该账号全部状态」，删号=前缀批删。Key 采用 `code/{jid}/{sub}` 二进制安全布局，`{jid}` 用定长编码（建议 `sha256(jid)[:16]` 定长前缀，避免变长 JID 破坏范围扫描；device 索引另存明文 JID）。

| whatsmeow 表 | 子 store（接口方法） | Badger key | value | 关键操作实现 |
|---|---|---|---|---|
| `whatsmeow_device` | DeviceContainer | `dev/{jid}` | 序列化 Device（NoiseKey/IdentityKey/SignedPreKey/RegID/AdvSecret/Account/PushName…） | GetAllDevices=扫 `dev/` 前缀；PutDevice=单 Put；DeleteDevice=前缀批删该 jid 所有 code |
| `whatsmeow_identity_keys` | IdentityStore | `idt/{jid}/{their_addr}` | `[32]byte` | PutIdentity=Put；DeleteAllIdentities=前缀删 `idt/{jid}/` |
| `whatsmeow_sessions` | SessionStore | `ses/{jid}/{their_addr}` | session bytes | GetManySessions=多 Get；PutManySessions=WriteBatch；DeleteAllSessions=前缀删；MigratePNToLID=读改写 |
| `whatsmeow_pre_keys` | PreKeyStore | `pk/{jid}/{keyid:BE32}` + 元 `pkm/{jid}` | key+uploaded 标志 / 元(next_id,uploaded_upto,count) | **高频**：GetOrGenPreKeys/GenOne 用 `pkm` 计数器分配 id 避免扫描；MarkUploaded=改元+范围标记；UploadedCount=读元 |
| `whatsmeow_sender_keys` | SenderKeyStore | `sk/{jid}/{group}/{user}` | session bytes | Put/Get 直映射 |
| `whatsmeow_app_state_sync_keys` | AppStateSyncKeyStore | `assk/{jid}/{keyid}` + `assklatest/{jid}` | key data / 最新 id | GetLatest=读 latest 指针；GetAll=前缀扫 |
| `whatsmeow_app_state_version` | AppStateStore | `asv/{jid}/{name}` | version+`[128]byte` hash | Put/Get/Delete 直映射 |
| `whatsmeow_app_state_mutation_macs` | AppStateStore | `asm/{jid}/{name}/{indexMAC}` | valueMAC | GetMutationMAC=Get；DeleteMutationMACs=按 indexMAC 批删 |
| `whatsmeow_contacts` | ContactStore | `ctc/{jid}/{their_jid}` | names 结构 | PutAll=WriteBatch；GetAll=前缀扫 |
| `whatsmeow_chat_settings` | ChatSettingsStore | `chs/{jid}/{chat_jid}` | muted/pinned/archived | 读改写单键 |
| `whatsmeow_message_secrets` | MsgSecretStore | `msec/{jid}/{chat}/{sender}/{msgid}` | secret | PutMany=WriteBatch |
| `whatsmeow_privacy_tokens` | PrivacyTokenStore | `ptk/{jid}/{their_jid}` | token+ts | DeleteExpired=前缀扫比 ts（低频，可接受） |
| `whatsmeow_nct_salt` | NCTSaltStore | `nct/{jid}` | salt | 单键 |
| `whatsmeow_event_buffer` | EventBuffer | `evb/{jid}/{ciphertextHash}` | plaintext+ts | DeleteOld=按 ts 扫；或用 Badger TTL(SetEntry WithTTL) 自动过期 |
| `whatsmeow_retry_buffer` | EventBuffer(outgoing) | `oge/{jid}/{chat}/{msgid}` | payload+ts | 同上，倾向 Badger TTL |
| `whatsmeow_lid_map` | LIDStore | `lidpn/{lid}`→pn、`lidlid/{pn}`→lid | JID | ⚠️ **待确认作用域**：whatsmeow 中 LID map 可能是设备内 or 全局；若全局则 key 不带 {jid}。实现前 grep 确认 |

> 说明：`{their_addr}` = whatsmeow signal address（`user.device` 形式）。序列化优先复用 whatsmeow 内部已用的编码（多为定长字节或 protobuf），Device 顶层用 gob/protobuf。

### 3.3 事务/批量/扫描

- 写用 `db.Update`；批量（PutManySessions/PutAllContactNames）用 `WriteBatch` 合并 fsync。
- 前缀扫描用 `Iterator` + `PrefetchValues`；删号用 `DropPrefix`（Badger 原生高效前缀删）。
- 计数器（prekey）用独立元 key，避免每次 O(n) 扫描——prekey 是最高频路径。

### 3.4 ⚠️ 决策点：Badger `SyncWrites` 与 Signal 状态持久性

Signal 棘轮状态丢一次写可能导致解密失败/对端要求重发。取舍：
- `SyncWrites=true`：每写 fsync，安全但**扼杀 IOPS**（与 brief「压榨 IOPS」矛盾）。
- `SyncWrites=false`（推荐）：靠 Badger value-log + 周期 sync，崩溃丢最近数百 ms 写。**最坏=该号重握手/重发少量消息**，可接受；配合 boot 时对未完成握手的号走 re-warm。
- **折中方案（建议）**：device/identity（罕见、致命）用同步 Put；sessions/prekeys（高频）异步。适配器按 code 前缀区分 sync 策略。
- **需你拍板**：接受「异步 + 崩溃小窗口丢失」以换 IOPS，还是关键子 store 强同步。

---

## 4. 【Task 2】Redis 防双开锁 + 代理冷却 ZSET

### 4.1 防双开锁——复用现有 fence 模型（不退化）

现 `ownership_redis.go` 已用 `owner:{jid}="node:fence"` + `fence:{jid}` INCR + `node:hb:{node}` TTL 判活，`Healthy()` 校验 `owner==node:fence && hb 存在`。**优于裸 `lock:jid` 90s TTL**（TTL 锁在 GC/STW 超时后会脑裂双发）。
- 单机场景防双开主要是「同进程内不重复起同一 JID」——内存 `cluster.Registry` 已保证；Redis 锁负责跨进程崩溃重启/多节点。
- 建议：保留 fence 模型，把硬编码 `hbTTL=30s` 解耦为配置（现有延后项），令 `WADIST_OWNERSHIP_BACKEND=redis` 成为本次默认。

### 4.2 代理带权冷却圈——ZSET 替换 SKIP LOCKED

- 每国家一个 `proxy:pool:{cc}` ZSET，`member=proxyID`，`score=下次可用时间戳ms`。
- 分配：`ZRANGEBYSCORE proxy:pool:{cc} -inf {now} LIMIT 0 1` 取已冷却≥60s 者 + Lua 原子 `ZREM`（占用）；O(logN)。
- 释放：`ZADD proxy:pool:{cc} {now+cooldown} proxyID`。
- **必须保留的语义**（不能只有冷却）：① 容量上限——代理仅在有空闲 binding 槽时才在 ZSET 中（满则移出，释放时再加回）；② 死代理排除——`ReportProxyFailure` 达阈值即从所有 ZSET `ZREM`；③ 国家隔离已由 per-cc ZSET 天然实现。
- **真相源**：PG `proxy_pool` 仍为durable 真相源（导入/存活/容量），Redis ZSET 是热索引，**节点启动时从 PG 重建 ZSET**；避免 Redis 丢数据导致代理池「消失」。
- **代理粘性不变**：账号首次 warm 才分配并写 `proxy_url_cache`；后续复用缓存，仅代理死亡才重进 ZSET 重挑（复用现 `StickyBindProxy` + ProxyJanitor）。

---

## 5. 【Task 3】Rolling-Wave 令牌桶滚筒波调度

### 5.1 目标
废弃「1s ticker 批处理 + asynq 脉冲」，改内存令牌桶：**每秒稳定唤醒 ~160 号、发 160 条、销毁 160 连接**，CPU/内存/网络 IO 成直线，杜绝 GC 尖峰。

### 5.2 结构
- 令牌桶：`rate.Limiter(τ, burst)`（`golang.org/x/time/rate`），τ 由 Risk Governor 动态调。
- 环形/优先队列喂料：从 `control.Scheduler` due:{cc} ZSET 拉到期号 → 每拿一个令牌 → 触发一次「warm(若未驻留)→boot jitter→Admit→Hold→render→send→Settle→ghost 下线」的单元流水。
- 平滑摊销：不再「整秒一批 N 条」，而是令牌以 1/τ 间隔滴出 → 到达近 Poisson，天然去脉冲。
- 与 Working-Set 协同：Pump 只在 R 有空位且号已（或可）驻留时发；背压=令牌等待 + due 重排。

### 5.3 保留 vs 替换
- **替换**：asynq 队列/worker、`DispatchRunning` 1s 批循环、`dispatchBatch` 的 PG 拉取脉冲。
- **保留**：`sendgate.Admit`（最终原子闸，防重复计数）、`billing.Hold/Settle`、`message_id` 幂等、`sent_today` 对称记账、campaign_recipients 状态机。
- ⚠️ **去 asynq 的代价**：失去「durable at-least-once 队列 + 自动重试」。恢复策略=进程启动从 PG `campaign_recipients WHERE state='pending'` 重扫补发；in-flight 崩溃丢失由 `message_id` 幂等 + 退款兜底吸收。对 5–10 条/号/天可接受，但**须显式接受**该取舍。

---

## 6. 【Task 4】反封欺骗层（Boot Jitter + Ghost Disconnect）

### 6.1 Boot Jitter
warm：代理 TCP + TLS(Noise) 握手完成后，`math/rand` 休眠 5–15s（可中断 ctx）再让 Pump 选中发信，模拟真人开 App 迟疑。落点=`SessionFactory` 或 Pump 的 warm→eligible 之间。低风险。

### 6.2 ⚠️ Ghost Disconnect —— 本方案最大技术风险，必须有界化
brief 要求：发完**不**调 `Disconnect()`/不发 Close 帧，让底层 TCP 因心跳超时自然死亡（模拟基站信号丢失）。

**风险**：
1. **whatsmeow 内部 goroutine 不会自己停**——Client 有 keepalive/read-pump goroutine。仅「结束你的业务 goroutine」不会关闭底层 websocket，真正结果是**永久泄漏**（goroutine + FD + 代理 socket），不是「幽灵」而是「僵尸」。
2. **连接会计**：160/s 新连接 × ghost 窗口，若窗口无上限则连接数无界增长 → FD/内存/conntrack 爆。

**方案（有界 Ghosting）**：
- 新增 **Ghost Reaper**：发送完成的 Session 不 GracefulClose，而是转入「幽灵池」，仅**停止应用层收发与续约**（让 WA 侧因缺 keepalive 判定离线），但**保留一个硬上限 `ghost_max` + 硬 TTL `ghost_ttl`（建议≤30s）**；超限/超时由 Reaper `Disconnect()` 强制回收底层资源。
- 即「对 WhatsApp 表现为信号丢失（不主动 Close 帧），对本机资源仍有界回收」——两全。
- **须验证**：whatsmeow 是否提供「停 keepalive 但不 Close」的入口；若无，需在 `cluster` 新增方法（已获授权）。
- **须 A/B 标定**：「TCP 自然死亡 vs 优雅 logout」哪个封号率低是**假设**，用 `canary` cohort 实测反推（现有标定框架）。

---

## 7. 【Task 5】Lua 智能退避 + PgBouncer 隔离

### 7.1 admission 指数退避（加法扩展）
在现 `admitLua` 基础上增加：账号/网段收到 `wa_warning` 时，除 health−30，写 `backoff:{seg}` 键（seg=cc 或代理 /24），下次该段 `min_gap` 动态翻倍（指数），带上限与衰减 TTL。Risk Governor 消费该信号联动降 τ。落点=`sendgate/admission.go` + `ApplyHealthSignal`。

### 7.2 PgBouncer 事务模式
- **前置条件已满足**：T1 移除 whatsmeow SQL、T2 移除 `pg_try_advisory_lock`（session 级锁跨查询与 txn 池不兼容）后，PG 只剩 billing/audit/campaign/账号元数据，**全是短事务** → 可安全走 PgBouncer transaction 模式。
- pgxpool `MaxConns ≤ 200`；关闭 prepared-statement 缓存或用 `pgx` 的 simple protocol（PgBouncer txn 模式不支持跨事务 prepared statement）——**须确认 pgx 配置** `PreferSimpleProtocol`/`statement_cache_mode`。
- 所有 repo 用完即还，严禁应用层跨事务持连（现 lockPool 模式整体删除）。

---

## 8. 必须保留的正确性不变式（红线虽解除，但不可回归 bug）

1. **计费**：`balance=Σledger`、`frozen=Σ未结charge`；`message_id` 幂等（Hold ON CONFLICT）；对账双不变式。→ 保留 billing 测试。
2. **发送幂等**：`message_id` 全链路；`sent_today` 对称记账（分配+1/非发送退出−1）。
3. **防双开**：任一 JID 任一时刻至多一条活跃会话（内存 Registry + Redis fence）。→ 需 **Redis 版 TestTakeoverChaos 平价测试**。
4. **代理隔离**：account↔proxy 1:1 终身粘性，不每发轮换。
5. **门禁**：`make gate`（含 -race）、指标高基数 label 门禁、内存基线门禁全绿。

---

## 9. 分模块替换顺序（逐个 PR，可回滚）

| 步 | 模块 | 交付 | 验收 |
|---|---|---|---|
| M1 | Badger 会话 store adapter | 实现 13 子 store + Container + `SetAllStores`；单元测试对拍 sqlstore 行为 | whatsmeow 起号/收发/appstate 同步通过；崩溃恢复策略验证 |
| M2 | Redis 锁默认化 + 代理 ZSET 冷却圈 | ownership=redis 默认；proxy ZSET + PG 重建 + 容量/死代理语义 | Redis 版 chaos 平价；代理不每发轮换、死代理排除 |
| M3 | Rolling-Wave Pump | 内存令牌桶替换 asynq/批循环；PG 重扫恢复 | τ 平滑（无 GC 尖峰）、幂等/记账不变式保持 |
| M4 | 反封层 | Boot jitter + 有界 Ghost Reaper（+ 必要的 cluster 方法） | 连接数有界、FD 不泄漏；cohort A/B 封号率标定 |
| M5 | Lua 退避 + PgBouncer | admission 指数退避；pgxpool≤200 + txn 模式 + simple protocol | 段级退避生效；PgBouncer 压测无 prepared-stmt 报错 |
| M6 | 上量标定 | 1K/min→3K→6K→10K ramp，卡封号率 | 稳态 τ 达标、封号率≤SLO、内存≤预算 |

---

## 10. 决策记录（2026-07-02 已定）

1. **Badger 持久性**（§3.4）：✅ **device/identity 强同步 fsync，sessions/prekeys 等高频子 store 异步**。适配器按 key `code` 前缀区分 sync 策略；boot 时对未完成握手的号走 re-warm 兜底。
2. **Ghost 下线**（§6.2）：✅ **有界 Ghost Reaper**——对 WA 表现为信号丢失（不发 Close 帧、停应用层收发与续约），对本机 `ghost_max` 硬上限 + `ghost_ttl≤30s` 强制 `Disconnect()` 回收。连接预算须含 ghost 窗口并发（§2）。
3. **去 asynq**（§5.3）：✅ **接受**——内存令牌桶 Pump，无 durable 队列；崩溃后从 PG `campaign_recipients WHERE state='pending'` 重扫补发，`message_id` 幂等 + 退款兜底吸收 in-flight 丢失。
4. **LID map 作用域**（§3.2）：⏳ 实现前（M2/M1）grep whatsmeow 确认设备内 vs 全局，据此定 key 是否带 `{jid}`。属实现期核实项，非阻塞。
5. **PG durable 真相源**：✅ **保留**——PG 存代理池/账号元数据/计费/审计的持久真相；Redis(锁/冷却/限速/due) 与 Badger(Signal 状态) 为热层，节点启动时从 PG 重建 Redis 热索引。
