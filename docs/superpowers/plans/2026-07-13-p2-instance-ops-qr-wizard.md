# P2 实例运维 + 扫码接入向导 — 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** admin 后台平台级管理 Evolution 实例(列表/扫码接入向导/重连/批量登出删除/节点容量),并用备用号跑通真机验证钉死协议假设。

**Architecture:** console(admin API)复用 worker 同款 `cluster.EvoCluster`;新增 `internal/api/instances_api.go` 一组 admin 端点,只调 `EvoClient` 现有方法 + `store` 读写 + `nodering.AssignNode`,不改 dispatch/cluster 引擎逻辑。前端 `/admin/instances` 页 + 扫码向导弹框。

**Tech Stack:** Go(gin/pgx/testcontainers)、Next.js 16 魔改版 + React 19 + Tailwind 4 + ProDataTable(P1 升级版)+ i18n dicts(P1b 机制)。

**Spec:** `docs/superpowers/specs/2026-07-13-p2-instance-ops-qr-wizard-design.md`

## Global Constraints

- **红线**:只调 `EvoClient` 现有方法(CreateInstance/SetProxy/ConnectInstance/FetchState/LogoutInstance/DeleteInstance)+ `store`(UpsertInstance/BindInstanceJID*/InstanceForJID/NodeCounts/BindProxy/GetBoundProxy/ReleaseProxy)+ `nodering.AssignNode`。**不改** dispatch/cluster 的发送/生命周期业务逻辑(evoInstance/SendWorker/governor/orchestrator/registry/reaper)。
- admin 端点:`v1.Group("/admin", s.requireAuth(), s.requireRole(console.RoleAdmin))` 下注册,handler 用 `s.systemPool()`(BYPASSRLS 跨租户)。所有**写**操作补 `audit_log`(照 SP2 的 `internal/api/audit.go` 助手,复用不重造 INSERT)。
- 创建实例 **fail-closed**:无可用代理 / SetProxy 失败 → 不 connect、回滚(Delete 实例 + 释放代理)、返回错误。绝不裸连。
- 后端测试:testcontainers 真库 + **fake EvoClient**(接口注入,不打真 Evolution);金钱无涉但审计写入要测。前端无 JS 测试框架,门槛 = `cd frontend && npx tsc --noEmit` + `node scripts/check-i18n-keys.mjs`(0 missing)+ 改动文件无新 lint 错误 + `npm run build`。
- 前端文案全部走 i18n dict(键 `admin.instances.*` 入 `frontend/lib/i18n/dicts/admin.ts`,zh/en 成对);无硬编码中文(除注释)。
- `make gate`(tidy/vet/test-race/labels)在后端任务收尾跑;已知预存 vuln `GO-2026-5856` 不阻断。
- git 操作在**执行时的 worktree 根**(勿照抄绝对路径)。

---

### Task 1: config + console 接线 EvoCluster(休眠,无端点)

**Files:**
- Modify: `internal/api/*.go`(`Deps` 结构加 `EvoCluster *cluster.EvoCluster` 字段;`Server` 若缓存 deps 则透传)
- Modify: `cmd/console/main.go`(构建 `cluster.NewEvoCluster(cfg.EvolutionNodes, cfg.EvolutionAPIKey, cfg.EvolutionWebhookSecret)` 注入 Deps;config 已有这些字段,console 的 `config.Load` 已读同环境变量——确认无需改 config)
- Test: 无(纯接线;编译 + `make gate` 即验证)

**Interfaces:**
- Produces:`Deps.EvoCluster`(后续 T2–T5 handler 经 `s.deps.EvoCluster.For(node)` 拿 `*cluster.EvoClient`)。

- [ ] **Step 1: 勘查** — 读 `internal/api` 的 `Deps` 结构定义与 `cmd/console/main.go` 现有 Deps 构建;读 `cmd/wadist/main.go:181` 的 `NewEvoCluster` 调用确认签名;确认 `config.Load` 在 console 侧已填充 `EvolutionNodes`(worker/console 共用同 `config.Load`?若 console 用不同 Load 路径,确认 Evolution 字段被读)。
- [ ] **Step 2: Deps 加字段** — `EvoCluster *cluster.EvoCluster`(import `internal/cluster`)。
- [ ] **Step 3: console 接线** — `cmd/console/main.go` 构建 EvoCluster 注入 Deps。若 `cfg.EvolutionNodes` 为空(未配置 Evolution),`NewEvoCluster` 应能容忍(返回空注册表);handler 在 `.For(node)` 返回 `!ok` 时给 503/500,不 panic。
- [ ] **Step 4: 验证 + 提交** — `make gate`(tidy/vet/test-race/labels 绿,vuln 仅 GO-2026-5856);`git commit -m "feat(p2): wire EvoCluster into console admin API deps (dormant)"`。

---

### Task 2: GET /admin/instances 列表

**Files:**
- Create: `internal/api/instances_api.go`(handler + `buildInstanceWhere`)
- Create: `internal/store/instances_query.go`(`ListInstances` 查询,或加到现有 `instances.go`)
- Modify: `internal/api/router.go`(注册路由)
- Test: `internal/api/instances_db_test.go`(testcontainers)

**Interfaces:**
- Produces:
```go
// store
type InstanceListRow struct {
    InstanceName string
    JID          *string   // nullable
    TenantID     int64
    TenantName   string     // joined
    EvoNode      string
    ProxyID      *int64
    State        string
    UpdatedAt    time.Time
}
type InstanceFilter struct { State, Node, Q string; TenantID int64; Limit, Offset int }
func (m *Manager) ListInstances(ctx, InstanceFilter) (rows []InstanceListRow, total int, stateStats map[string]int, err error)
// handler: GET /admin/instances → {rows, total, stats}
```

- [ ] **Step 1: 勘查** — 读 `handleAdminListCampaigns`/`handleAdminListRecipients`(SP3a/SP5 的 `{rows,total,stats}` + buildWhere + 分页模式),照抄结构;读 `account_instances` 表列 + tenants join 键(tenant_id→tenants.name)。
- [ ] **Step 2: 写失败测试** — `instances_db_test.go`:seed 2 租户 × 3 实例(不同 state/node),测 `ListInstances` 的分页、state 筛选、q 搜索(jid/instance_name)、stateStats 全集、total 未筛选。Run: `go test ./internal/api/ -run TestListInstances -v` → FAIL(未实现)。
- [ ] **Step 3: 实现 store.ListInstances** — SystemPool 查询,LEFT JOIN tenants,`buildInstanceWhere`(state/node/tenant_id 精确 + q ILIKE jid/instance_name),ORDER BY updated_at DESC,LIMIT/OFFSET;stateStats 单独 GROUP BY state(未筛选或仅 tenant 维度,与 SP3a stats 语义一致——照现有页决定,默认全局)。
- [ ] **Step 4: 实现 handler + 路由** — `handleAdminListInstances`:解析 query(state/node/tenant_id/q/limit/offset),调 ListInstances,返回 `{rows,total,stats}`;`router.go` 注册 `admin.GET("/instances", s.handleAdminListInstances)`。
- [ ] **Step 5: 测试通过 + gate + 提交** — `go test ./internal/api/ -run TestListInstances -v` PASS;`make gate`;`git commit -m "feat(p2): GET /admin/instances cross-tenant list with filters+stats"`。

---

### Task 3: POST /admin/instances 创建(代理键勘查 + fail-closed + 回滚)

**Files:**
- Modify: `internal/api/instances_api.go`(handler + fake-able EvoClient 接缝)
- Modify: `internal/store/instances.go`(如需 instance-name 键的 proxy 辅助)
- Test: `internal/api/instances_db_test.go`(fake EvoClient)

**Interfaces:**
- Consumes:`Deps.EvoCluster`(T1)、`store.BindProxy`/`ReleaseProxy`/`NodeCounts`/`UpsertInstance`、`nodering.AssignNode`。
- Produces:`POST /admin/instances` body `{tenant_id:int64, country_code:string}` → `{instance_name, evo_node, proxy_id, state:"created"}`;handler 依赖一个**窄接口** `instanceEvoAPI`(CreateInstance/SetProxy/DeleteInstance)便于 fake(生产由 `*EvoClient` 满足,经 `EvoCluster.For(node)` 取)。

- [ ] **Step 1: 勘查代理键(spec §6b 强制)** — 读全 `cmd/wadist/main.go` 的 `SessionFactory`(尤其 `proxyBinding` 从哪来:是否 `GetBoundProxy(jid)` 或 `BindProxy(jid,cc)`)+ `internal/store/proxy_redis.go` 的 `bind`/`releaseBinding` 键语义(accountJID 用作什么、proxy_pool 计数在哪加减)。**产出结论写进 report**:创建实例用 `instance_name` 当 BindProxy 键是否与 worker 恢复路径冲突?若 worker 会对同实例二次 SetProxy 覆盖粘性,记录并选定对策(v1 首选:用 instance_name 当键,扫码回填 jid 后**不**迁移——因为代理粘性已在 Evolution 侧 SetProxy 固化,worker 恢复路径读 `GetBoundProxy` 应能按 instance 找到;若确认 worker 按 jid 查会 miss→二次 bind,则 T3 加一个"回填时 rebind 键"store 方法或在 report 标记为 T8 真机必验项)。**此步只读+决策,不写实现代码。**
- [ ] **Step 2: 写失败测试** — fake EvoClient(记录 CreateInstance/SetProxy 调用、可注入 SetProxy 失败);测:①happy path(建租户+seed 代理池→POST→断言 account_instances 有 created 行 + proxy_id + evo_node + Evolution create/setProxy 被调 + 审计 instance.create 写入);②无可用代理→402/409 且不建行不调 Evolution;③SetProxy 失败→回滚(DeleteInstance 被调 + 无残留行 + 代理释放)+ 500/502。Run → FAIL。
- [ ] **Step 3: 实现 handler** — 顺序:校验 tenant 存在(不存在 400)→ 生成 `instance_name = fmt.Sprintf("inst_%d_%s", tid, rand8)`(rand8 用 crypto/rand hex,避免 `Math.random` 无关;Go 侧 `crypto/rand`)→ `BindProxy(instance_name, country_code)`(err/nil→402)→ `NodeCounts`+`AssignNode(cap)`(!ok→409 all-full/配置错分别提示)→ `EvoCluster.For(node)`(!ok→503)→ `CreateInstance(name, cfg.EvolutionWebhookURL)` → `SetProxy(name, binding)`(err→回滚:`DeleteInstance(name)` best-effort + `ReleaseProxy(instance_name)` → 502)→ `UpsertInstance{InstanceName,TenantID:tid,EvoNode:node,ProxyID,State:"created"}` → 审计 `instance.create` → 返回。
- [ ] **Step 4: 测试通过 + gate + 提交** — 三测 PASS;`make gate`;`git commit -m "feat(p2): POST /admin/instances create w/ auto-proxy, sharding, fail-closed rollback"`。

---

### Task 4: QR / state / reconnect / logout / delete 端点

**Files:**
- Modify: `internal/api/instances_api.go`
- Modify: `internal/api/router.go`
- Test: `internal/api/instances_db_test.go`

**Interfaces:**
- Consumes:窄接口扩展 `ConnectInstance`/`FetchState`/`LogoutInstance`/`DeleteInstance`(fake 覆盖)。
- Produces:
  - `GET /admin/instances/:name/qr` → `{base64}`(ConnectInstance)
  - `GET /admin/instances/:name/state` → `{state}`(FetchState)
  - `POST /admin/instances/:name/reconnect` → `{base64}` + 审计 instance.reconnect
  - `POST /admin/instances/:name/logout` → 204 + UpsertInstance(state=loggedOut) + 审计 instance.logout
  - `DELETE /admin/instances/:name` → 204 + ReleaseProxy + 删行 + 审计 instance.delete

- [ ] **Step 1: 勘查** — 确认 `ConnectInstance` 返回 base64(evolution_client.go:180 已读:`{base64}`);确认删除的正确顺序(先 Evo `DeleteInstance` 后 DB 删行:Evo 失败则不删行,避免孤儿 Evolution 实例)。所有端点先按 instance_name 查 account_instances 存在(404)+ 取 evo_node 定位 client。
- [ ] **Step 2: 写失败测试** — fake 覆盖新方法;测:qr/state 透传;logout 后 state=loggedOut + 审计;delete 后无行 + 代理释放 + 审计;delete 时 Evo 失败→不删行 + 500(幂等:DeleteInstance spec 说幂等,但 DB 行以 Evo 成功为前提)。Run → FAIL。
- [ ] **Step 3: 实现 5 端点 + 路由** — 每个先 `getInstanceRow(name)`(404 guard)→ `EvoCluster.For(row.EvoNode)` → 调对应方法;写操作补审计。注意 reconnect 复用 ConnectInstance(重取 QR)。
- [ ] **Step 4: 测试通过 + gate + 提交** — PASS;`make gate`;`git commit -m "feat(p2): instance qr/state/reconnect/logout/delete endpoints + audit"`。

---

### Task 5: GET /admin/nodes 容量视图

**Files:**
- Modify: `internal/api/instances_api.go` + `router.go`
- Test: `internal/api/instances_db_test.go`

**Interfaces:**
- Produces:`GET /admin/nodes` → `[{node, count, cap, pct}]`(`NodeCounts` + `cfg.EvolutionCapPerNode`;pct = count/cap 保护除零)。

- [ ] **Step 1: 写失败测试** — seed 实例分布多节点,测 NodeCounts 聚合 + cap 注入 + pct 计算(cap=0 时 pct=0 不 panic)。Run → FAIL。
- [ ] **Step 2: 实现** — handler 调 `store.NodeCounts` + 读 `cfg.EvolutionCapPerNode`(handler 需能拿到 cap——经 Deps 或 Server config 字段;T1 若未透传 cap,此处补一个 `Deps.EvoCapPerNode int`),组装数组。
- [ ] **Step 3: 测试通过 + gate + 提交** — PASS;`make gate`;`git commit -m "feat(p2): GET /admin/nodes capacity view"`。

---

### Task 6: 前端实例列表页 + 批量操作

**Files:**
- Create: `frontend/app/admin/instances/page.tsx`(服务端标题 I18nText)
- Create: `frontend/components/admin-instances.tsx`(client,列表 + 批量)
- Modify: `frontend/components/admin/nav.ts`(资源组加 `/admin/instances` 项,label 实例/en Instances)
- Modify: `frontend/lib/i18n/dicts/admin.ts`(`admin.instances.*` zh/en)
- Modify: `frontend/lib/api.ts` 如需类型/fetch 助手

**Interfaces:**
- Consumes:`GET /admin/instances`(server 模式分页)、logout/delete/reconnect 端点、`GET /admin/nodes`。
- Produces:实例运维页。

- [ ] **Step 1: 勘查** — 读 `admin-send-records.tsx`(server-mode ProDataTable + state Tab + 搜索 + stats 卡的既有范式,照抄);读 P1 的 ProDataTable `selection` prop 用法(批量);读 nav.ts 资源组结构。
- [ ] **Step 2: 列表页** — `admin-instances.tsx`:ProDataTable server 模式,列 号码(jid,未配对显"—"占位)/租户/节点/代理ID/状态徽标/更新时间/行操作(重连·登出·删除弹确认);state Tab(created/qr/connected/disconnected/loggedOut)+ 号码/租户搜索;stats 卡(各 state 计数)。批量选择→批量登出/删除(per-item toast 报告,照 P1 selection.actions)。全文案 `t("admin.instances.*")`。
- [ ] **Step 3: 节点容量卡** — 页顶一行节点用量条(`GET /admin/nodes`),接近 cap 高亮(用 brand/destructive token)。
- [ ] **Step 4: nav + 字典 + 验证** — nav 资源组加项;`admin.instances.*` 键 zh/en 成对;`npx tsc --noEmit` + `node scripts/check-i18n-keys.mjs`(0 missing)+ 改动文件 lint 干净 + `npm run build`。
- [ ] **Step 5: 提交** — `git commit -m "feat(p2): admin instances list page + bulk logout/delete + node capacity"`。

---

### Task 7: 前端扫码接入向导弹框

**Files:**
- Create: `frontend/components/admin-instance-wizard.tsx`(client,向导 Sheet/Dialog)
- Modify: `frontend/components/admin-instances.tsx`(接入"接入新账号"按钮打开向导)
- Modify: `frontend/lib/i18n/dicts/admin.ts`(`admin.instances.wizard.*`)

**Interfaces:**
- Consumes:`POST /admin/instances`(创建)、`GET /admin/instances/:name/qr`、`GET /admin/instances/:name/state`(轮询)。

- [ ] **Step 1: 勘查** — 读现有一个多步弹框/Sheet 组件(如 new-campaign-dialog.tsx 或 contacts-import-dialog.tsx)对齐交互范式;读租户下拉数据源(`GET /admin/tenants` 或 users 页已有 fetch)。
- [ ] **Step 2: 向导实现** — 步骤①表单:租户下拉 + 国家码输入/下拉 → "创建"调 `POST /admin/instances`(loading/错误 toast:无代理/无节点分别提示)。步骤②QR:`GET /qr` 取 base64 渲染 `<img src="data:image/png;base64,...">`(确认 Evolution 返回是否含 data URI 前缀——T8 真机验;先按纯 base64 拼前缀,真机若带前缀则去重),并发轮询 `GET /state`(每 2–3s;connected→成功态+自动关闭+刷新列表;超时 ~90s→重试按钮重调 reconnect/qr)。全文案入字典。
- [ ] **Step 3: 验证** — tsc + check-i18n-keys + lint + build。
- [ ] **Step 4: 提交** — `git commit -m "feat(p2): QR pairing wizard (create -> poll QR/state -> connected)"`。

---

### Task 8: 真机 evo-verify 联调 + 回填 spec §9 + 删 TODO

**Files:**
- Modify: `docs/evolution-real-machine-verification-runbook.md`(顶部结论表回填)
- Modify: `docs/superpowers/specs/2026-07-10-evolution-api-migration-design.md`(§9 回填)
- Modify: `internal/cluster/evolution_client.go` 等(删已验证的 `TODO(evo-verify)`,如字段名需修则修)
- Modify: `frontend/components/admin-instance-wizard.tsx`(QR data URI 前缀按真机确认修正)

**这是有人在环节点:需要用户用备用号真机扫码。** subagent 不能代扫。控制器(或用户)执行真机步骤,把观测回填,再由 subagent 做代码修正。

- [ ] **Step 1: 起沙盒** — `docs/four-phase-demo/evo-verify` 的 docker-compose 起 Evolution+Redis;console+worker 指向它(WADIST_EVOLUTION_* 环境变量)。
- [ ] **Step 2: 走真机流程** — admin 后台 → 接入向导 → 选租户+国家 → 创建 → 弹 QR → **备用号扫码** → 观测:①QR base64 字段名/是否带 data URI 前缀 ②`FetchState` 状态字符串真实枚举(created→qr→connected)③connection.update webhook 回填 jid(data.wuid,V1 假设)④代理是否生效(实例走代理 IP)⑤登出/删除路径。
- [ ] **Step 3: 回填 + 修正** — 每处假设:真实值 vs 代码假设,一致则删 TODO,不一致则派 subagent 修代码 + 加/改测试。结论写 runbook 顶部结论表 + spec §9。**代理键结论(T3 Step1 的未决项)在此真机确认**:扫码回填 jid 后,worker 恢复该账号发送时代理是否仍粘同一 IP(不粘则按 T3 记录的对策实现键迁移)。
- [ ] **Step 4: gate + 提交** — 若有代码修正 `make gate` + 前端门槛;`git commit -m "fix(p2): pin Evolution QR/state/proxy assumptions from real-machine verification"`(+ docs commit)。

---

## 计划外(不做)

- 手动指定代理/节点;代理主动探活;风控/发送监控(P3);报表(P3);批量后端端点(前端循环);代理键迁移工具(除非 T8 真机证明必需)。
