# 管理平台 — 顶层路线图设计 (Management Platform Roadmap Spec)

- 状态:草案,待评审
- 日期:2026-06-25
- 层级:**顶层路线图 spec**(锁定整体架构与跨子系统契约)。五个子系统各自再出详细 spec → plan → 实现。

---

## 1. 目标与背景

在现有 `wadist`(无界面的 WhatsApp 多账号并发分发节点)之上,构建一个**统一 Web 管理平台**,按角色(管理员 / 销售 / 客户)提供视图与权限,支撑完整的"接单 → 配置产能 → 充值 → 客户自助发送"业务闭环。

现状约束(已核实):

- `wadist` 当前唯一的 HTTP 面是运维端点 `/metrics`、`/healthz`、`/readyz`([internal/metrics/server.go](../../../internal/metrics/server.go)),**无任何前端、无 JS 构建链**。
- 后端业务机制(账号、代理、活动编排、计费、限流、对账、集群接管)真实可用,但**真实投递链路未接通**:resolver 与 media uploader 是占位桩([cmd/wadist/main.go:33](../../../cmd/wadist/main.go#L33)、[cmd/wadist/main.go:141-143](../../../cmd/wadist/main.go#L141-L143),标注 `pre-M-send`)。
- **系统无 `tenants` 实体表**:`tenant_id` 仅作为散落在各表的 `BIGINT`,没有租户注册表。
- 计价是全局硬编码 `priceFor(country)`([cmd/wadist/main.go:39-52](../../../cmd/wadist/main.go#L39-L52)),不支持按客户定价。
- RLS 已就绪:9 张租户表 `FORCE ROW LEVEL SECURITY`,策略读 `current_setting('app.current_tenant_id')`;store 层已提供 `WithTenant(ctx, tenantID)`(走 `app_tenant` 池,事务内 set GUC)与 `app_system`(BYPASSRLS)双池([internal/store/rls.go](../../../internal/store/rls.go))。

## 2. 业务场景(权威需求)

> 销售获取到客户 WS 群发需求,敲定发送国家以及发送单价。管理员配置发送所需的 WS 账号和代理并新建客户账号。客户预付发送定金,管理员充值后,客户登录账号编辑发送文案和号码提交后进行发送。

### 端到端生命周期(角色 × 系统表 × 模块)

| # | 步骤 | 角色 | 落到哪张表 | 模块 |
|---|---|---|---|---|
| 1 | 接需求,敲定发送国家 + 发送单价 | 销售 | `tenant_pricing`(新):tenant_id × country × 单价 | D |
| 2 | 配置发送用的 WS 账号 + 代理 | 管理员 | `account_devices`、`proxy_pool`(+绑定) | C |
| 3 | 新建客户账号 | 管理员 | `tenants`(新) + `console_users`(新, role=customer) | C |
| 4 | 客户预付定金(线下转账) | 客户 | —(线下) | — |
| 5 | 收款后充值钱包 | 管理员 | `tenant_wallets` + `wallet_ledger`(credit 流水) | C |
| 6 | 登录 → 编辑文案 + 号码 → 提交 | 客户 | `campaign_templates`、`campaign_recipients`、`campaigns` | E |
| 7 | 发送(按 租户+国家 单价逐条扣费) | 系统 | dispatcher 扣 `tenant_wallets`,真实投递走模块 A | A+E |

## 3. 架构与技术栈

- **独立二进制 `cmd/console`**,与 wadist 节点同一 Go module,通过 `internal/*` 复用 store / billing / dispatch / crypto / audit / cluster 全部业务逻辑。**不混进 worker 节点**:节点是横向扩到 500+ 账号的无头 worker,控制台是低副本、带认证的 web 层;二者扩缩与安全姿态不同,分开部署、各自端口、独立故障域。
- **服务端渲染:Go `html/template` + htmx + 极简 CSS**,无 JS 构建链。理由:贴合单体 Go 仓库、内部工具为主、易维护、零新增构建工具。htmx 提供局部刷新与表单提交的渐进式交互,不引入 SPA。
- **会话:Redis 后端**(部署已含 Redis)。session id 存于签名 + 加密 cookie,服务端 session 数据存 Redis,支持登出与吊销。
- **配置**:沿用 `WADIST_*` 环境变量风格,新增 `console` 专属变量(监听地址、session 密钥、cookie 域等),细节在模块 B spec 定义。

### 角色 → 数据池映射(复用现有双池,零新增隔离机制)

| 角色 | 数据访问 | 机制 |
|---|---|---|
| 管理员 | 跨租户全量 | `SystemPool`(BYPASSRLS) |
| 销售 | 仅名下租户 | `SystemPool` + 应用层按 `sales_owner_id` 过滤 |
| 客户 | 仅自己 | `WithTenant(tenantID)` → DB 级 RLS 强隔离 |

所有写操作记入 `audit_log`(表已存在,`app_tenant`/`app_system` 对其 `REVOKE UPDATE,DELETE`,仅可追加)。

## 4. 子系统分解(五个独立 spec)

每个模块单一职责、通过明确接口通信、可独立理解与测试。

### B — 平台地基(Platform Foundation)
- **职责**:`cmd/console` 进程骨架;HTTP 服务与路由;登录/登出/会话;RBAC 中间件;租户上下文中间件(客户请求 → `WithTenant`);`html/template` 布局与 htmx 基座;静态资源。
- **接口**:对外是带认证的 HTTP;对内依赖 store 双池与 `crypto`(密码哈希复用既有 crypto 习惯或 argon2id)。
- **依赖**:store。**被 C/D/E 依赖。**
- **新建表**:`tenants`、`console_users`(见 §5)。

### C — 管理员控制台(Admin Console)
- **职责**:跨租户读 + 写。建客户(租户 + 客户登录);配账号/代理产能(`account_devices`、`proxy_pool` 及绑定);**钱包充值**(写 `wallet_ledger` credit + 增 `tenant_wallets`);退款审批(`refund_requests`);节点/集群只读视图;审计查询。
- **接口**:复用 `billing`、`dispatch`、`cluster`、`store` 现有方法;新增"管理员充值"用例(见 §6)。
- **依赖**:B。

### A — 真实发送链路(Send Path,纯后端)
- **职责**:实现 `cmd/wadist` 里现为桩的两处 —— recipient resolver(`campaign_recipients` → 收件人手机号 + 模板文案 + 媒体句柄)与 media uploader(whatsmeow 媒体上传)。routing sender 已存在([cmd/wadist/main.go:134](../../../cmd/wadist/main.go#L134))。
- **接口**:实现 `dispatch.Uploader` 与 send handler 的 resolver 回调签名(均已定义)。
- **依赖**:无(独立轨,可与 C 并行)。**E 依赖它才能真实投递。**

### E — 客户门户 + 发送流(Customer Portal)
- **职责**:RLS 隔离下的客户自助:编辑文案(`campaign_templates`)、导入号码(`campaign_recipients`)、创建并提交活动(`campaigns`)、查看发送进度与钱包余额/账单。
- **接口**:经 B 的租户上下文中间件,全部 DB 访问走 `WithTenant`;发送提交触发既有 dispatcher 路径。
- **依赖**:B、A。

### D — 销售视图(Sales Console)
- **职责**:管名下客户(`tenants.sales_owner_id`);**设发送国家 + 单价**(`tenant_pricing`);看名下用量与账单。
- **依赖**:B。

## 5. 持久化新增

新增迁移(沿用现有 `migrations/NNNN_*.sql` 风格,幂等):

- `0009_tenants.sql` — `tenants(id BIGSERIAL PK, name, status, sales_owner_id BIGINT NULL, created_at)`。
- `0010_console_users.sql` — `console_users(id BIGSERIAL PK, email UNIQUE, password_hash, role ENUM{admin,sales,customer}, tenant_id BIGINT NULL, disabled BOOL, created_at)`。其中 `role=customer` 行必须有 `tenant_id`;`role=sales` 行通过 `tenants.sales_owner_id` 反向关联其客户。
- `0011_tenant_pricing.sql` — `tenant_pricing(tenant_id, country_code, unit_price_minor, PK(tenant_id, country_code))`。

**与既有数据的衔接**:现有散落的 `tenant_id` 是裸 `BIGINT`;`tenants` 落地后,外键关联为**可选增强**(避免破坏既有行/RLS)。客户隔离**复用既有 RLS 策略,无需新增策略**。具体字段、约束、索引在各模块 spec 细化(本顶层 spec 不下沉到列级 DDL)。

## 6. 关键机制衔接点

1. **按客户定价**:dispatcher 现用全局 `priceFor(country)`。改为 `priceFor(tenantID, country)`:先查 `tenant_pricing`,未命中回退现有全局表。改动点在 `cmd/wadist` 装配与 dispatch 计价签名;具体重构在模块 C/A 相关 spec 定义,需保持现有 billing 事务语义与测试不回归。
2. **管理员充值**:新增一个 billing 用例 —— 在一个事务内向 `wallet_ledger` 追加 credit 流水并增 `tenant_wallets` 余额,记 `audit_log`。v1 **线下收款、管理员手工入账,无支付网关**。
3. **产能 ↔ 发送**:管理员预先为客户配齐 WS 账号 + 代理(产能池);客户提交发送时,系统从该客户可用账号中按既有调度/限流路径发送。

## 7. 测试策略

沿用仓库既有标准,不降级:

- 真实 PG16/Redis7 集成测试(testcontainers),`-race`。
- HTTP handler 用 `net/http/httptest`;RBAC/RLS 隔离必须有跨角色越权的否定用例(如客户 A 不能读客户 B 数据)。
- 沿用 `make gate`:tidy + vet + test-race + 高基数指标门禁 + govulncheck。
- 每个子模块 spec 自带其测试要求与验收标准。

## 8. 建设顺序

**B → C →(A 与 C 可并行)→ E → D**

理由:B 解锁全部 UI;C 不依赖未接通的发送链路、最快出可见价值;A 作为高风险 whatsmeow 集成单独隔离成并行轨;E 等 B+A 就绪;D 最后。每一步各自独立 spec → plan → 实现,逐里程碑可交付。

## 9. 非目标(YAGNI)

- 不在本顶层 spec 下沉到各模块的页面线框、列级 DDL、字段校验(留给子 spec)。
- 不接入在线支付网关(v1 线下收款 + 手工入账)。
- 不引入 SPA / 前端构建链(React/Vue/打包器)。
- 不在管理员 / 销售 / 客户三角色之外扩展更多角色。
- 不改造既有计费规则(扣费事务语义、退款逻辑保持不变),仅新增"按客户定价"查表与"管理员充值"用例。
- 不重构与本目标无关的既有代码。

## 10. 未决 / 子 spec 须确认

- `console_users` 密码哈希算法选型(argon2id vs 复用现有 crypto 习惯)— 模块 B 定。
- 客户活动的国家是否锁定为销售敲定的国家(单国)还是允许多国 — 模块 E 定,需与 `tenant_pricing` 粒度一致。
- 号码导入格式与上限(CSV/粘贴、去重、与 `suppression_list` 的交互)— 模块 E 定。
- 销售是否可代客操作(代下活动)还是仅配置与查看 — 模块 D 定。
