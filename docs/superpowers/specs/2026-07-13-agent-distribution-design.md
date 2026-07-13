# 模块九 · 多级代理分销 — 整体设计

**日期**: 2026-07-13
**分支**: `feat/agent-distribution`
**状态**: 设计已获用户认可，待评审 → writing-plans

## 背景与定位

现状：分销是**扁平单级**——`console_users(role='sales')` 经 `tenants.sales_owner_id` 直接挂客户(tenant)，SP7 佣金 = 名下租户当月消耗×比例(纯计算，无 payout)。无多级层级、无差价、无代理额度/钱包、无结算。计费主体是 tenant，`tenant_wallets` + 两段式 Hold/Settle 是**红线 billing 引擎**。

本模块建**多级代理分销**（"两片合一"整体设计）：多级代理树 + 差价 + 多级返佣 + 额度批量划拨 + 代理独立后台 + 月度结算。

**已定资金模型（用户拍板）**：混合(差价+返佣)；代理**无钱包**、**月结欠账**；欠账**消耗时计**；利润 = 直接代理差价 + 上级返佣。

**红线**：不改 `internal/{billing,dispatch,store,sendgate,cluster}` 核心。只新增迁移、`internal/api` 层、前端。额度划拨**调用**现有 `billing.Topup` 导出原语；欠款/差价/返佣/结算全是**只读计算**（照 SP7 佣金范式），不碰 Hold/Settle。

## 实体与层级

- **代理** = `console_users(role='sales')` + 新增 `parent_id BIGINT`(自引用) → 多级树。顶级代理 `parent_id=NULL`(直挂平台)。保留三角色 CHECK(sales 不变；UI 显示为"代理")。
- **客户** = tenant，经现有 `tenants.sales_owner_id` 挂**直接代理**(路径叶子边)。
- 代理**无钱包**，有 **`credit_limit`(信用额度)** + 按月计算的**结算单**。
- **子树** = 以某代理为根，沿 `console_users.parent_id`(下级代理) + `tenants.sales_owner_id`(直属客户) 递归展开的全部下线代理与客户。递归 CTE 圈定。

## 定价（差价 + 返佣）

- 平台设**单一成本价/国家**：新表 `agent_cost_pricing(country_code, unit_cost)`——平台对任何消耗量的收费基准(minor units)。
- **直接代理**用现有 `tenant_pricing(tenant_id, country)` 自定义客户**零售价** → 赚**差价 = 零售−成本**(仅其直属客户的消耗)。
- **上级代理**按下线**子树消耗**拿**多级返佣 rebate%**：复用/扩展 `console_users.commission_rate` 为 rebate 率，沿 `parent_id` 逐级向上。
- **返佣出资方 = 平台**(成本价内含返佣空间)——各代理结算彼此独立，无代理间对账。(策略默认，用户已认可。)

## 三条资金流

**红线 billing 引擎一行不改。**

1. **额度批量划拨**(代理→下线客户)：一个 `mgr.WithTenant` 事务里 `billing.Topup(ctx, tenantID, amount, ...)`(现有原语，增客户 tenant_wallet 余额) + 写 `agent_allocations` 台账 + **校验代理可用额度**(`credit_limit − outstanding ≥ amount`，否则拒)。批量 = 一次对多个客户划拨。
2. **消耗**：客户发群发 → 现有 Hold/Settle 扣其 tenant_wallet(零售价)。**完全不动**。
3. **月结**(消耗时计欠，纯计算，不碰 settle)：每代理月度结算单——
   - **欠平台** = 直属客户当月已 settle 消耗量 × 成本价。
   - **差价(margin)** = 直属客户消耗 × (零售 − 成本)。
   - **返佣(rebate)** = 子树(不含自身直属)消耗 × 本代理 rebate 率，逐级。
   - **净额** = 差价 + 返佣 − 欠款(payable/receivable)。
   - 按 Asia/Shanghai 自然月，查当月即当月累计(照 SP7)。结算记录写 `agent_settlements`(状态 open/settled)。

## 信用额度 / outstanding

- **平台给每个代理设 `credit_limit`**(策略默认，零售 minor units)。父代理向子代理分配额度 = 后续增强，本设计不做。
- **outstanding = 当前"浮"在下线客户手里的、代理已垫付但尚未被消耗掉的零售额度**：
  `outstanding = Σ agent_allocations.amount − Σ 子树客户已 settle 消耗(零售价)`。两项**同为零售 minor units**，可直接相减，无单位混淆。
  - 划拨↑ outstanding；客户消耗↓ outstanding。**连续量，不做月末归零**。
  - **可用额度 = credit_limit − outstanding**；划拨时校验 `amount ≤ 可用额度`。
- 这与月结**欠款**(成本 units = 子树消耗量×成本价)是**两条独立轴**：`outstanding` 是零售浮额风险(卡额度)，`欠款`是成本口径的月度应付。代理线下向客户收零售、按月向平台付成本，差价即利润。

## 代理独立后台（扩展现有 `/sales`）

代理**只见自己子树**：下线代理树、消耗汇总、额度用量(limit/outstanding)、月结单(欠款/差价/返佣/净额)、客户列表(+改零售价 via 现有 sales pricing)、**批量划拨额度**动作、发展下级代理。
**子树可见性 = 新访问控制维度**：所有代理侧读写先经递归 CTE 把 target 限定在 `requester 子树`内，代理**绝不能**看到兄弟/上级/其他树的数据。平台 admin 全视图。

## 数据模型（新增，全按现有 RLS/审计范式）

- `console_users` 加 `parent_id BIGINT REFERENCES console_users(id)`(自引用，仅 sales 有)、`credit_limit BIGINT NOT NULL DEFAULT 0`；`commission_rate` 复用为 rebate 率。
- `agent_cost_pricing(country_code CHAR(2) PK, unit_cost BIGINT, updated_at)`——平台成本价。
- `agent_allocations(id, agent_id, tenant_id, amount, actor_id, created_at)`——划拨台账(append-only)。
- `agent_settlements(id, agent_id, period DATE, debt, margin, rebate, net, status, created_at, UNIQUE(agent_id,period))`——月结单。
- 消耗汇总从现有 `billing_charges`/`wallet_ledger`(settle 记录) 按子树聚合。
- 树完整性：`parent_id` 防环(插入/改父时校验目标不在自身子树)。

## 架构 / 组件（隔离、可测）

- 纯逻辑叶子：`subtreeSQL`(递归 CTE 生成器)、`buildAgentRollup`、结算三项计算(`computeSettlement`)——放 `internal/api/agent_*.go` 纯函数，独立单测(照 finance_query.go 范式)。
- handler：`internal/api/agent_api.go`(代理侧，子树受限) + admin 侧(成本价/额度/树管理)。
- 隔离：代理侧经子树 CTE 限定；平台 admin 经 systemPool。审计复用 `recordAudit(Tx)`(划拨、设额度、设成本价、改父、结算)。
- 划拨调用 `billing.Topup`(唯一对 billing 的调用，导出原语)。

## 错误处理

- 划拨超可用额度 → 402/400，事务回滚(不 Topup)。
- 成环的改父 → 400。
- 客户不在代理子树 → 403(越权划拨/改价)。
- 结算按月幂等(UNIQUE(agent_id,period))；重复结算不重复计。

## 测试

- **纯单测**：subtreeSQL、computeSettlement(含多级返佣 per-agent round、Asia/Shanghai 月界)、额度校验。
- **真库(testcontainers)**：递归子树圈定(多级树)、划拨事务原子(Topup+台账+额度扣减)、超限拒绝+回滚、月结三项(欠款/差价/返佣)对账、**子树访问隔离**(代理 A 看不到代理 B 子树)、防环改父。
- **前端**：build + eslint(house baseline 不回归)。

## 交付切分（给 writing-plans 的种子；整体设计、分阶段实现）

1. 迁移(parent_id/credit_limit/agent_cost_pricing/agent_allocations/agent_settlements) + 防环。
2. `subtreeSQL` 递归 CTE + 子树圈定纯函数 + 单测。
3. 树管理(建下级代理/改父/设 rebate/credit_limit) + 子树访问控制。
4. 成本价 admin CRUD。
5. 额度批量划拨(Topup+台账+额度校验，事务)。
6. 消耗汇总 + 月结三项计算(computeSettlement) + 结算单落库。
7. 代理后台前端(树/汇总/额度/月结/划拨/客户改价)。
8. admin 侧前端(成本价/额度/树总览/结算总览)。

## 未决 / 假设

- 无阻塞未决。已定假设：返佣平台出资；平台设 credit_limit(父代理分配额度=后续)；outstanding = Σ划拨 − Σ子树已settle消耗(均零售 units)连续量、不月末归零，与成本口径欠款是两条独立轴；返佣沿 parent_id 逐级、平台出资、per-agent round 后求和(照 SP7 防浮点)；成本价单一/国家(每代理差异化成本=后续)。
