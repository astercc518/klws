# 交接 / PR 说明：资源精细化管理 · IP 绑定防关联 · 风控熔断中心

> 范围：原始三块功能（§1–§9）+ 后续增补两块（§10：群发数据穿透 + 触达回执接入），分阶段实现，已全部编译/类型/迁移幂等/集成测试验证通过。
> 红线复核（§1–§9 部分）：`internal/billing`、`internal/dispatch`、`internal/store`、`internal/sendgate`、`internal/cluster` **五个引擎包零改动**。所有逻辑落在 `migrations/`（加性）、`internal/api`、新包 `internal/riskbreaker`、装配层 `cmd/wadist`、以及前端。
> ⚠️ **红线例外（§10，已获用户明确授权）**：触达回执接入对 `internal/cluster` 有**一处极小的加性改动**（`NewWAConn` 增回执回调 + 注册一个只翻译转发的事件 handler，**不改发送逻辑**）。详见 §10.4。除此之外其余引擎包仍零改动。

---

## 1. 功能概览

| # | 功能 | 用户价值 |
|---|---|---|
| A | **资源精细化管理 + IP 绑定** | WS 账号可打多彩标签（如 `US-Marketing`）、为每个账号绑定/解绑独享静态代理 IP，实现账号间网络隔离防关联；代理池显示真实关联账号数 |
| B | **风控策略中心** | 管理员前端集中配置全局防封策略：发信间隔、单设备日上限、封号率熔断阈值（控制面，持久化+审计+热读） |
| C | **封号率自动熔断** | 后台监督器按配置巡检，任务封号率超阈值自动挂起（复用 `paused`），审计留痕；前端标记"自动熔断"并支持一键恢复 |

---

## 2. 变更文件清单

### 新增
```
migrations/0013_device_tags.sql            account_devices.tags TEXT[] + GIN 索引
migrations/0014_risk_config.sql            system_risk_config 单例表（四参数 + CHECK + seed）
migrations/0015_risk_breaker_opts.sql      熔断控制列（enabled/dry_run/min_sample/window/eval_interval）
internal/riskbreaker/riskbreaker.go        旁路熔断监督器（评估/封号率SQL/熔断事务/advisory选主/热读配置）
internal/riskbreaker/riskbreaker_test.go   DB 集成测试（env 守卫，无 DSN 自动 skip）
frontend/app/admin/settings/risk/page.tsx  风控策略页（PageHeader 规范）
frontend/components/admin-risk-settings.tsx 风控表单（Card/Slider/Toggle/Input + 诚实横幅）
docs/RISK-CIRCUIT-BREAKER-DESIGN-zh.md      熔断设计文档（阶段拆分/红线论证）
docs/HANDOVER-resource-riskbreaker-zh.md    本文件
```

### 修改
```
internal/api/admin_api.go    设备DTO+tags/proxy；代理DTO+bound_devices；导入支持tags；
                             绑定/解绑代理 handler；风控配置 GET/PUT；campaign auto_tripped；resume handler
internal/api/router.go       注册：devices/:id/proxy(POST,DELETE)、settings/risk(GET,PUT)、campaigns/:id/resume(POST)
cmd/wadist/main.go           装配 riskbreaker goroutine（sup.Go）
frontend/components/admin-devices.tsx   标签列/网络环境列/配置网络Dialog/导入解析tags
frontend/components/admin-campaigns.tsx 自动熔断徽章 + 恢复按钮
frontend/components/admin/nav.ts        侧边栏新增"策略中心 → 风控策略"
frontend/app/admin/audit/page.tsx       描述文案更新
```

---

## 3. 新增 / 变更 API

| 方法 | 路径 | 说明 | 鉴权 |
|---|---|---|---|
| POST | `/api/v1/admin/resources/devices/:id/proxy` | 为账号绑定指定代理（事务：释放旧→占用新，校验存活/容量；满/离线→409，不存在→404） | admin |
| DELETE | `/api/v1/admin/resources/devices/:id/proxy` | 解绑代理（幂等，回退计数器） | admin |
| GET | `/api/v1/admin/settings/risk` | 读风控配置（缺行返默认值） | admin |
| PUT | `/api/v1/admin/settings/risk` | 写风控配置（范围校验 + DB CHECK 兜底 + 审计 updated_by） | admin |
| POST | `/api/v1/admin/campaigns/:id/resume` | 恢复被挂起任务（`paused→running` + 写 resume 审计） | admin |

> 既有 `GET /admin/resources/devices`、`GET /admin/resources/proxies`、`POST /admin/resources/devices`、`GET /admin/campaigns` 字段已扩展（tags/proxy/bound_devices/auto_tripped），向后兼容（仅新增字段）。

---

## 4. 数据库迁移（应用顺序 — 必须顺序执行）

迁移由部署脚本/人工用 `psql` 顺序应用。三个迁移**全部为加性**（`ADD COLUMN IF NOT EXISTS` / `CREATE TABLE IF NOT EXISTS`），已用 `scripts/migrate_twice.sh` 验证幂等。

```bash
# 按编号顺序，逐个应用（ON_ERROR_STOP 保证失败即止）
psql "$DSN" -v ON_ERROR_STOP=1 -f migrations/0013_device_tags.sql
psql "$DSN" -v ON_ERROR_STOP=1 -f migrations/0014_risk_config.sql
psql "$DSN" -v ON_ERROR_STOP=1 -f migrations/0015_risk_breaker_opts.sql
```

迁移内容速览：
- **0013**：`account_devices.tags TEXT[] NOT NULL DEFAULT '{}'` + `idx_acc_tags` GIN。
- **0014**：`system_risk_config` 单例表（`id=1` CHECK），列 `min_delay_seconds`(3) / `max_delay_seconds`(8) / `daily_limit_per_device`(1000) / `ban_rate_circuit_breaker`(0.15) + `updated_at/updated_by`；seed 一行。
- **0015**：`system_risk_config` 追加 `circuit_breaker_enabled`(false) / `circuit_breaker_dry_run`(true) / `min_sample`(20) / `window_seconds`(900) / `eval_interval_seconds`(20) + 边界 CHECK。

> ⚠️ 注意：`proxy_id` / `proxy_url_cache` 列**早已存在**（`0002_proxy_pool.sql`），本次无需新增；IP 绑定复用既有列 + 既有 `current_bindings/max_bindings/chk_bindings`。

---

## 5. 红线合规说明（评审重点）

- **IP 绑定**：`internal/store/proxy.go` 的 `BindProxy/ReleaseProxy` 是自动选代理的防封事务，**未改**。指定代理的绑定/解绑改为在 `internal/api` 用 `SystemPool` 裸 SQL 事务实现，复刻了相同的计数器不变量（释放旧、`WHERE is_alive AND current_bindings<max_bindings` 原子占用），`chk_bindings` CHECK 兜底。
- **风控配置**：未碰 `internal/store/config.go`（那是连接池配置且属红线包）；配置存到新表，api 层 `SystemPool` 读写。
- **熔断器**：与 dispatch 引擎**仅通过数据库 campaign 状态解耦通信**——熔断器把 campaign 置 `paused`，dispatcher 本就只发 `running`（`scheduler.go`），因此引擎无需感知、无需改动。熔断器是全新包；启动代码加在 `cmd/wadist`（装配层，非五包）。
- **封号率度量**：复用现有表（`campaign_recipients.last_error/state/updated_at`），匹配 `dispatch/worker.go:isBanSignal` 的同源模式（`wa_warning|banned|403`）。**已知耦合**：若日后改 `isBanSignal` 的字符串模式，需同步更新 `riskbreaker.banRate` 的 SQL（见设计文档 §10）。

---

## 6. 验证记录（已执行）

- `go build ./...` ✅　`go vet ./internal/api/... ./internal/riskbreaker/... ./cmd/wadist/...` ✅
- `npx tsc --noEmit`（frontend）✅
- 迁移幂等：一次性 `postgres:16` 容器跑 `scripts/migrate_twice.sh` → `OK: migrations are idempotent`；CHECK 约束拒绝非法值已验证。
- 熔断器集成测试（`internal/riskbreaker`，env 守卫）：超阈值挂起+审计+幂等、最小样本不误杀、dry-run 不挂起、无 DSN 干净 skip —— 全过。
- `auto_tripped` 判定 SQL 与 resume 流程：psql 实证 c1=t/c2=f/c3=f/c4=f，恢复后复位为 f —— 全对。
- **全程未触碰生产 `klws-postgres-1` 与 docker compose**（牢记铁律：本机跑 compose 永远带齐 `-f docker-compose.prod.yml -f docker-compose.adopt.yml`）。

---

## 7. 上线 Checklist

1. **代码合并 + 构建镜像**：后端 `klws-console`/`wadist`、前端 `klws-frontend`。
   > `wadist` 节点二进制需重建（含新 `riskbreaker` goroutine）；`klws-console`（API）需重建（含新 handler/字段）。
2. **应用迁移**（顺序见 §4）。对生产用真实 DSN，先 `pg_dump` 备份。
3. **重启服务**（带齐两个 compose `-f`）：
   ```bash
   cd /var/klwa && docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml up -d
   ```
4. **冒烟验证（控制面）**：
   - 设备页：标签多彩展示、未绑定显示告警、配置网络绑定/解绑生效、批量导入 `tenant_id:jid:phone:tag1,tag2` 解析正确。
   - 风控页：可读取/保存四参数 + 熔断开关；保存有 Sonner toast；侧边栏入口可达。
5. **熔断灰度（强烈建议）**：
   - 先 `circuit_breaker_enabled=true`、`circuit_breaker_dry_run=true`，观察 1–2 天 `wadist` 日志中 `riskbreaker: DRY-RUN would pause campaign ...`，据此校准 `ban_rate_circuit_breaker`/`min_sample`/`window_seconds`。
   - 校准后关 `dry_run`，正式启用。
6. **闭环验证**：制造/等待一个高封号率任务 → 审计页出现红色「自动熔断」徽章 → 点「恢复」→ 任务回 running、徽章消失、审计有 `campaign.circuit_break` 与 `campaign.resume` 两条。

---

## 8. 回滚

- **熔断**：风控页一键 `circuit_breaker_enabled=false`（热生效，下一巡检周期停止动作）。dispatcher 完全不受影响。
- **整体**：回滚镜像即可；迁移为加性，**无需回滚 schema**（新列/新表对旧代码无害）。若坚持回滚 schema，需手写 down 脚本（当前未提供，因加性变更回滚通常不必要）。
- **数据安全**：迁移不改任何既有列/数据，仅新增。

---

## 9. 已知限制 / 后续工作

- **熔断精度（阶段 2，未做，需单独授权）**：当前封号归因依赖 `last_error` 文本匹配。若需更精确，可在 `campaign_recipients` 加 `ban_attributed` 列并由 `worker.markFailed` 写入——**会改 `internal/dispatch`（红线），需单独授权**。见设计文档 §8 阶段 2。
- **另两个参数尚未引擎接入**：`min/max 间隔` 与 `单设备日上限` 目前 UI 可配置但引擎仍用编译默认值（页面已诚实标注）。接入需改 `dispatch/sendgate`（红线），见设计文档附录 A，建议单独立项。
- **多节点**：熔断器用 `pg_try_advisory_lock` 选主，多 `wadist` 节点下同一周期仅一个评估；即便重复，挂起 UPDATE 幂等。
- **熔断器指标**：当前以结构化日志为主，未接 Prometheus 计数器（设计文档列为可选增强）。

---

## 10. 增补：群发数据穿透 + 触达回执接入

> 在 §1–§9 之后追加的两块功能。设计依据：`docs/RECEIPT-INGESTION-DESIGN-zh.md`。
> 关键：本节含**唯一一处经授权的红线改动**（`internal/cluster`，见 §10.4），其余仍落在 api / 新包 / 装配层 / 前端。

### 10.1 功能概览

| # | 功能 | 用户价值 |
|---|---|---|
| D | **群发任务明细穿透** | 客户面板与超管看板可点开任意任务，宽版 Sheet 抽屉展示转化漏斗 + 逐号送达明细（手机号 / 状态 / 失败原因 / 时间），服务端分页 |
| E | **触达回执接入（delivered/read）** | 接入 WhatsApp 送达/已读回执，漏斗从"提交→已发送"延伸到"提交→已发送→**已送达→已读**"，逐号显示送达/已读时间 |

### 10.2 变更文件清单（增补）

**新增**
```
migrations/0016_recipient_receipts.sql       campaign_recipients.delivered_at/read_at + idx_recip_msgid
internal/receipt/receipt.go                  回执记录器 Recorder（幂等关联 SQL，provider 无关）
internal/receipt/receipt_test.go             DB 集成测试（env 守卫）
internal/api/campaign_test.go                classifyError / mapStatus 单测（无需 DB）
frontend/components/ui/sheet.tsx             侧边抽屉（基于 base-ui Dialog）
frontend/components/campaign-detail-sheet.tsx 客户/超管共用：漏斗卡片 + 分页明细表 + 状态徽章
frontend/components/customer-campaigns.tsx   客户任务列表（此前不存在）+ 点击穿透
frontend/app/dashboard/campaigns/page.tsx    客户任务列表页
docs/RECEIPT-INGESTION-DESIGN-zh.md          回执接入设计文档
```
**修改**
```
internal/api/campaign.go      共享 loadRecipients（漏斗计数 + 分页 + 错误分类 + delivered/read 里程碑）；
                              GET /campaigns/:id/recipients（客户, RLS）；campaignSummary 加 created_at
internal/api/admin_api.go     GET /admin/campaigns/:id/recipients（超管, SystemPool, 复用 loadRecipients）
internal/api/router.go        注册上述两条 recipients 路由
internal/cluster/conn_whatsmeow.go  ★红线：NewWAConn 增 onReceipt 回调 + 注册回执事件 handler（见 §10.4）
cmd/wadist/main.go            装配 receipt.Recorder，向 NewWAConn 注入回调（WADIST_RECEIPTS 开关）
frontend/components/admin-campaigns.tsx   超管任务行可点击 → 打开明细抽屉
frontend/components/new-campaign-dialog.tsx  增 onDone 回调（提交后刷新列表）
frontend/components/sidebar.tsx           客户侧边栏「新建群发」→「群发任务」
```

### 10.3 新增 API（增补）

| 方法 | 路径 | 说明 | 鉴权 |
|---|---|---|---|
| GET | `/api/v1/campaigns/:id/recipients?page=&page_size=` | 客户：本租户任务的接收者明细 + 漏斗计数（RLS 自动隔离，越权 404） | customer |
| GET | `/api/v1/admin/campaigns/:id/recipients?page=&page_size=` | 超管：任意任务明细（SystemPool 跨租户） | admin |

返回体：`{campaign_id, total, counts{queued,sent,delivered,read,failed,skipped}, page, page_size, recipients[]}`。`recipients[]` 每行：`phone, status(里程碑 read>delivered>sent>queued/failed/skipped), error_reason(分类码), error_detail(原文), sent_at, delivered_at, read_at, updated_at`。分页默认 50、封顶 200。

### 10.4 ★红线改动详解（`internal/cluster`，已授权）

**为何必须碰**：回执是 whatsmeow client 的异步事件，client 由 `internal/cluster` 持有并 `Connect`，**无法在 cluster 之外取到**——这是与熔断器（可纯旁路）的本质差别。

**改了什么（加性、最小、不改发送逻辑）**：
- `NewWAConn(device, logger, proxy, onReceipt ReceiptFunc)` 新增最后一个回调参数。
- 当 `onReceipt != nil` 时，在 `whatsmeow.NewClient` 上 `AddEventHandler` 一个 handler：把 `*events.Receipt` 翻译为 `(messageIDs, kind, timestamp)` 并转发——`""→delivered`、`read/read-self→read`，其余忽略。**零业务逻辑、零 DB 访问、不 import 自有 receipt 包**（只认 whatsmeow + 原语）。
- `Send`/`Connect`/`Disconnect` 等发送相关逻辑**一行未动**。

**逻辑落点（非红线）**：
- 关联与写库在新包 `internal/receipt`：按 `message_id + assigned_jid` 幂等 `UPDATE`（`COALESCE` 保留最早时间，read 回填 delivered 保单调）。用 `SystemPool`（BYPASSRLS，回执不带租户上下文）。
- 装配在 `cmd/wadist`：每账号注入回调（捕获该账号 `jid` 作 `assigned_jid` 匹配），异步用 `context.Background()`。
- **可行性根基**：`campaign_recipients.message_id` 存的是 whatsmeow 真实消息 ID（`waConn.Send` 返回 `resp.ID` → `worker.markSent` 写入），故回执 `MessageIDs` 可直接关联。

**回滚开关**：环境变量 `WADIST_RECEIPTS=off` → 不注册 handler，回执停止写入，**发送链路完全不受影响**。

### 10.5 验证记录（增补，已执行）

- `go build ./...` ✅（含红线 `cluster` 与 `cmd/wadist`）；`go vet` ✅；`go test` 触及包全绿（api/receipt/riskbreaker/cluster/cmd-wadist，DB 测试无 DSN 自动 skip）。
- `npx tsc --noEmit` ✅。
- 迁移 `0016` 幂等：`migrate_twice` → `OK`。
- `receipt.Recorder` 集成测试：delivered 置位、read 置位且回填 delivered、`COALESCE` 幂等不覆盖、`assigned_jid` 消歧 —— 全过。
- 漏斗计数 SQL psql 实证：`total=12 | queued=3 | sent=7 | delivered=2 | read=1 | failed=2` 与预期一致。
- recipients 分页/存在性 SQL：命中=t / 不存在=空(→404)、`GROUP BY`/`LIMIT/OFFSET` 正确。
- **全程未触碰生产库与 docker compose**。

### 10.6 上线 / 回滚（增补）

- **迁移**：在 §7 步骤 2 追加 `psql ... -f migrations/0016_recipient_receipts.sql`（加性，已验证幂等）。
- **镜像**：`klws-console`（API recipients 接口）、`klws-frontend`（抽屉/列表）、`wadist`（回执 handler）均需重建。
- **回执灰度**：先 1 个 `wadist` 节点带新二进制（`WADIST_RECEIPTS` 不为 off），观察 `delivered_at/read_at` 是否正常填充、漏斗数字合理，再全量。
- **冒烟**：任意任务点开抽屉 → 漏斗四级 + 明细分页正常；客户侧「群发任务」列表可见可点。
- **回滚**：`WADIST_RECEIPTS=off` 重启该节点即停回执写入（发送不受影响）；或回滚镜像（迁移加性，schema 无需回滚）。

### 10.7 已知限制（增补）

- **回执早于 markSent**（极少）：回执引用真实 ID 但 DB 尚未写入 → UPDATE 命中 0 行、该回执丢失。缓解：回执通常滞后于发送；可后续加短期重试缓冲。
- **"delivered" 健康分语义**：`worker.go` 仍在发送成功即刻给 `+delivered` 健康分（非真实送达）。如需改由真实回执触发，需改 `internal/dispatch`+`internal/sendgate`（红线），与回执漏斗解耦，建议单独立项。见 `docs/RECEIPT-INGESTION-DESIGN-zh.md` §7。
- **played（语音已播放）回执**：当前忽略，未单列。
