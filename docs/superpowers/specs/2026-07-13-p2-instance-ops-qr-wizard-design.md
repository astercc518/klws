# P2 实例运维 + 扫码接入向导 设计

日期:2026-07-13
状态:已获用户批准(P2 四问定案)
上位:`2026-07-13-platform-reskin-crud-design.md` 模块②

## 1. 目标

补齐"账号/实例运维"缺口:admin 后台平台级管理 Evolution 实例——列表总览、扫码接入向导、掉线重连、批量登出/删除、节点容量视图。并借扫码向导完成 Evolution 真机验证(钉死 QR/状态码等协议假设)。

## 2. 需求定案(P2 四问)

- **真机时机**:用户有 Evolution 环境 + 可扫码备用号 → P2 做成**含真机验证闭环**(边写边验)。
- **代理分配**:扫码向导选国家码 → `BindProxy` 从代理池自动分配粘性代理;无可用代理则 fail-closed 拒绝创建。手动指定代理留后续。
- **列表范围**:平台级跨租户总览(admin god view,照 send-records/大盘同款),带租户列 + 筛选。
- **架构**:console(admin API 进程)复用 worker 同款 `cluster.EvoCluster`,admin `Deps` 加 `EvoCluster` 字段接线;零新客户端、零 worker RPC。

## 3. 架构

### 3.1 接线
- `internal/config`:console 侧读取已有的 `WADIST_EVOLUTION_NODES`/`APIKEY`/`WEBHOOK_SECRET`/`CAP_PER_NODE`/`WEBHOOK_URL`(worker 已用,console 复用同环境变量)。
- `cmd/console/main.go`:`cluster.NewEvoCluster(cfg.EvolutionNodes, cfg.EvolutionAPIKey, cfg.EvolutionWebhookSecret)` 注入 `api.Deps.EvoCluster`。
- `internal/api`:新增 handler 经 `Deps.EvoCluster.For(node)` 拿 `*EvoClient` 调现有方法。

### 3.2 红线
- 只调 `EvoClient` 现有方法(CreateInstance/SetProxy/ConnectInstance/FetchState/LogoutInstance/DeleteInstance)+ `store` 读写(UpsertInstance/InstanceForJID/NodeCounts/BindProxy/GetBoundProxy)+ `nodering.AssignNode`。
- **不改** dispatch/cluster 的发送与生命周期业务逻辑(evoInstance/SendWorker/governor 等)。
- 创建实例代理 **fail-closed**:无可用代理或代理设置失败 → 不 connect、返回错误(沿用 `evoInstance.Connect` 的 create→setProxy→connect fail-closed 约束)。
- admin 端点走 `requireAuth()+requireRole(RoleAdmin)`,SystemPool(BYPASSRLS,跨租户)。所有写操作补 `audit_log`(照 SP2 助手)。

## 4. 后端端点(internal/api,新增 instances_api.go)

| 端点 | 动作 | 审计 |
|---|---|---|
| `GET /admin/instances` | 跨租户列表:instance_name/jid/tenant_id+name/evo_node/proxy_id/state/updated_at;`buildInstanceWhere`(state/tenant_id/node/q 筛选)+ `{rows,total,stats}`(state 分布 stats);照 SP3a 分页 | — |
| `POST /admin/instances` | body `{tenant_id, country_code}`:①校验 tenant 存在 ②`BindProxy(instanceName, country_code)` 自动分池(无代理→402/409)③`NodeCounts`→`AssignNode(cap)` 分片(all-full→扩容提示,empty-ring→配置错)④`EvoClient.CreateInstance(name, webhookURL)` ⑤`SetProxy(name, binding)`(失败→回滚:Delete 实例+释放代理)⑥`UpsertInstance(state=created, proxy_id, evo_node)` | instance.create |
| `GET /admin/instances/:name/qr` | `ConnectInstance(name)` → base64 QR;向导轮询调用 | — |
| `GET /admin/instances/:name/state` | `FetchState(name)` → created/qr/connected/…;向导轮询 | — |
| `POST /admin/instances/:name/reconnect` | `ConnectInstance` 重取 QR/重连(掉线实例) | instance.reconnect |
| `POST /admin/instances/:name/logout` | `LogoutInstance(name)` + `UpsertInstance(state=loggedOut)` | instance.logout |
| `DELETE /admin/instances/:name` | `DeleteInstance(name)` + 释放代理绑定 + 删 account_instances 行(事务:先 Evo 后 DB;Evo 失败不删行) | instance.delete |
| `GET /admin/nodes` | `NodeCounts` + `cfg.EvolutionCapPerNode` → 每节点 {node, count, cap, pct} | — |

**实例名生成**:`inst_<tenant_id>_<短随机>`(随机由 handler 生成,避免碰撞;instance_name 是 PK)。

**批量端点**:批量登出/删除复用单条端点循环(前端逐个调,per-item 报告),不新增批量后端(YAGNI,量级低)。

## 5. 前端(frontend)

- **路由** `/admin/instances`(nav 资源组,`Smartphone`/`QrCode` 图标;文案入 `dicts/admin.ts` 键 `admin.instances.*`,zh/en 成对——沿用 P1b 机制)。
- **实例列表**:升级版 ProDataTable(server 模式分页+state Tab 筛选+租户/号码搜索),列:号码(jid)/租户/节点/代理/状态徽标/更新时间/行操作(重连·登出·删除);批量选择+批量登出/删除(复用 P1 selection,per-item toast 报告)。
- **扫码接入向导**(弹框/Sheet):步骤①选租户(下拉)+ 国家码 → 创建 → ②QR 弹窗(轮询 `/qr` 显示 base64 图,`/state` 轮询;connected→自动关闭+刷新列表;超时/失败→重试)。
- **节点容量视图**:`/admin/nodes` 卡片行(每节点用量条,接近 cap 高亮)。

## 6. 真机验证闭环

写完后用备用号跑 `docs/four-phase-demo/evo-verify`(docker-compose Evolution/Redis + 手机侧清单):
1. 创建实例 → 确认 `/instance/create` + `/proxy/set` 真实响应与代码假设一致。
2. 取 QR → **钉死 `ConnectInstance` 返回的 base64 字段名**(代码假设 `data.base64`,evo-verify TODO)。
3. 扫码 → 确认 `FetchState` 状态字符串流转(created→qr→connected 的真实枚举值)。
4. 登出/删除 → 确认路径与幂等。
全绿后回填 `2026-07-10-evolution-api-migration-design.md` §9、删对应 `TODO(evo-verify)`。**验证结果记 evo-verify runbook 顶部结论表。**

## 6b. 已知实现风险(plan/T3 必须钉死)

**从零接入是系统首个无 jid 创建路径。** worker 的 `SessionFactory` 以 jid 为轴(`AssignNode(...,jid)`/`TenantForJID(jid)`/`UpsertInstance(JID:jid)`),是"已登录账号恢复会话"。P2 创建时无 jid(`account_instances.jid` 允许 NULL,扫码后 webhook `connection.update`→`BindInstanceJIDIfUnset` 回填)。牵出两点必须在 T3 勘查 worker `proxyBinding` 来源后定案:

1. **代理绑定键**:`BindProxy(accountJID, cc)` 以 accountJID 为键记账(proxy_pool 计数 + account_devices)。创建时无 jid → 用 `instance_name` 当键;删除时 `ReleaseProxy(instance_name)` 对称释放。**代理粘性实际落在 Evolution 实例上(`SetProxy` 一次终身),Go 侧键只管池计数/释放**——需确认 worker 恢复路径不会因 jid≠instance_name 而对同一 Evolution 实例二次 `BindProxy(jid)`+`SetProxy` 覆盖粘性。若有此风险,T3 记录并决定:扫码回填时迁移绑定键(instance_name→jid),或统一按 instance_name 记账。
2. **AssignNode 键**:worker 用 jid,P2 用 instance_name;分片只需一个稳定键,instance_name 满足(PK 唯一),无迁移问题。

T3 先派勘查子任务读全 `SessionFactory`(proxyBinding 来源)+ `redisProxyAllocator.bind`/`releaseBinding` 键语义,再定实现。

## 7. 分任务(SDD)

- T1 config + console 接线 EvoCluster + `Deps.EvoCluster`(休眠,无端点)
- T2 `GET /admin/instances` 列表(store 查询 + buildInstanceWhere + 分页,真库测试)
- T3 `POST /admin/instances` 创建(BindProxy+AssignNode+CreateInstance+SetProxy+回滚+审计,真库测试 fake EvoClient)
- T4 QR/state/reconnect/logout/delete 端点(+审计,真库测试)
- T5 `GET /admin/nodes` 容量
- T6 前端列表页 + 批量操作
- T7 前端扫码向导弹框(QR 轮询)
- T8 真机 evo-verify 联调 + 回填 spec §9 + 删 TODO

## 8. 验收标准

- admin 可在 `/admin/instances` 看全租户实例列表(状态/节点/代理),筛选分页正常。
- 扫码向导:选租户+国家 → 弹 QR → 备用号真机扫码 → 连接成功 → 列表出现 connected 实例。
- 批量登出/删除、单条重连可用,写操作有审计。
- 节点容量视图反映真实 NodeCounts。
- 后端 testcontainers 真库测试(fake EvoClient 注入);前端 build+lint(baseline 不回归);check-i18n-keys 0 missing。
- **真机 evo-verify 全绿,QR/状态码假设钉死,spec §9 回填,TODO(evo-verify) 删除。**

## 9. 计划外(不做)

- 手动指定代理/节点;代理探活主动探测(维持暂不立项);风控/发送监控(P3);批量后端端点(前端循环足够)。
