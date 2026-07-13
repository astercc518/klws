# 模块九 · 多级代理分销 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 多级代理树 + 差价 + 多级返佣 + 额度批量划拨 + 月度结算 + 代理独立后台，红线 billing 引擎一行不改。

**Architecture:** 新迁移(console_users 加 parent_id/credit_limit + 3 张新表) + `internal/api` 层(递归 CTE 子树圈定 + 结算计算，照 SP7 佣金/finance_query 范式) + 前端。代理无钱包，月结欠账，消耗时计欠。额度划拨**调用** `billing.Topup` 导出原语；欠款/差价/返佣全是只读计算。

**Tech Stack:** Go 1.23 + pgx v5 + gin + testcontainers-go(postgres:16) + Next.js。

## Global Constraints

- **红线**：禁改 `internal/{billing,dispatch,store,sendgate,cluster}` 核心逻辑。只新增迁移、`internal/api`、前端。唯一对 billing 的调用是导出原语 `billing.Topup(ctx, tenantID, amount int64, ref string) error`(幂等于 ref，拒非正数)。结算/欠款/差价/返佣 = 只读计算(照 SP7)。
- **资金三轴(口径不可混)**：① `outstanding`(零售 units) = Σ agent_allocations.amount − Σ 子树 billing_charges settled amount；卡 credit_limit。② `debt`(成本 units) = Σ_country 子树直属客户 settled 消耗量(COUNT billing_charges) × agent_cost_pricing.unit_cost。③ 利润 = margin(直属零售−成本) + rebate(子树零售×rebate 率)。
- **消耗源**：`billing_charges`(state=settled) 有 `tenant_id, country_code, amount(零售), created_at`。量=COUNT，零售额=SUM(amount)。月界按 `created_at AT TIME ZONE 'Asia/Shanghai'`，用现有 `parseMonth`。
- **代理侧非 tenant**：代理 handler 用 `s.systemPool()`(BYPASSRLS) + **显式子树 CTE 圈定**(不是 RLS)。子树 = 沿 `console_users.parent_id`(下级代理) + `tenants.sales_owner_id`(直属客户) 递归。代理绝不能读兄弟/上级/他树数据。
- **防浮点**：返佣/佣金一律 `round(consumption * rate)` **per-tenant/per-agent 后再 SUM**(照 SP7 finance_stats.go:270)。rate 存 NUMERIC(5,4)。
- **审计**：划拨/设额度/设成本价/改父/建下级/结算 写 `audit_log`，用 `s.recordAudit(ctx, auditEvent{...})` / `recordAuditTx(ctx,tx,...)`。
- **前端无 JS 测试框架**：验证 = `npm run build` + eslint(house baseline `react-hooks/set-state-in-effect` 不新增类别)。

---

## File Structure

- Modify `migrations/0022_agent_distribution.sql`(新建) — console_users +parent_id/credit_limit；agent_cost_pricing / agent_allocations / agent_settlements；防环触发器；grants。
- Create `internal/api/agent_query.go` — 纯：`subtreeCTE(rootParam string) string`、`buildAgentRollup`、`computeSettlement`(纯计算) + 类型。
- Create `internal/api/agent_api.go` — 代理侧 handler(子树受限：树/汇总/额度/月结/划拨/客户改价)。
- Create `internal/api/agent_admin.go` — admin 侧(成本价 CRUD / 设额度 / 树总览 / 结算总览)。
- Modify `internal/api/router.go` — 注册 `/sales/*`(扩展) + `/admin/agent*`。
- Create `internal/api/agent_db_test.go` — testcontainers 真库测试。
- Create `frontend/app/sales/*` 扩展 + `frontend/app/admin/agents/*` + 组件。

---

## Task 1: 迁移 0022 — 层级列 + 3 表 + 防环

**Files:**
- Create: `migrations/0022_agent_distribution.sql`
- Test: `internal/api/agent_db_test.go`(新建，schema 冒烟)

**Interfaces:**
- Produces: `console_users.parent_id BIGINT`(自引用)、`credit_limit BIGINT NOT NULL DEFAULT 0`；表 `agent_cost_pricing / agent_allocations / agent_settlements`。

- [ ] **Step 1: 写迁移**

`migrations/0022_agent_distribution.sql`:
```sql
-- 0022: 多级代理分销。console_users 扩层级+信用额度；成本价/划拨台账/月结单。replay-all 幂等。
ALTER TABLE console_users ADD COLUMN IF NOT EXISTS parent_id   BIGINT REFERENCES console_users(id);
ALTER TABLE console_users ADD COLUMN IF NOT EXISTS credit_limit BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_console_users_parent ON console_users (parent_id);

-- 平台成本价/国家（minor units）。
CREATE TABLE IF NOT EXISTS agent_cost_pricing (
    country_code CHAR(2) PRIMARY KEY,
    unit_cost    BIGINT  NOT NULL CHECK (unit_cost >= 0),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
DROP TRIGGER IF EXISTS trg_agent_cost_touch ON agent_cost_pricing;
CREATE TRIGGER trg_agent_cost_touch BEFORE UPDATE ON agent_cost_pricing
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- 划拨台账（append-only）：代理给客户 tenant_wallet 垫付的零售额度。
CREATE TABLE IF NOT EXISTS agent_allocations (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id   BIGINT NOT NULL REFERENCES console_users(id),
    tenant_id  BIGINT NOT NULL REFERENCES tenants(id),
    amount     BIGINT NOT NULL CHECK (amount > 0),
    actor_id   BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_alloc_agent ON agent_allocations (agent_id);

-- 月结单（每代理每月一行）。
CREATE TABLE IF NOT EXISTS agent_settlements (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id   BIGINT NOT NULL REFERENCES console_users(id),
    period     CHAR(7) NOT NULL,             -- 'YYYY-MM' (Asia/Shanghai)
    debt       BIGINT NOT NULL,             -- 成本口径应付平台
    margin     BIGINT NOT NULL,             -- 直属差价
    rebate     BIGINT NOT NULL,             -- 子树返佣
    net        BIGINT NOT NULL,             -- margin+rebate-debt
    status     TEXT   NOT NULL DEFAULT 'open' CHECK (status IN ('open','settled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (agent_id, period)
);

-- app_tenant 永不读这些员工/结算表（照 0010 对 console_users/tenants 的 REVOKE）。
REVOKE ALL ON agent_cost_pricing, agent_allocations, agent_settlements FROM app_tenant;
```

- [ ] **Step 2: schema 冒烟测试**

`internal/api/agent_db_test.go`:
```go
package api

import (
	"context"
	"testing"
)

func TestAgentSchemaApplies(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var tabs, cols int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_name IN ('agent_cost_pricing','agent_allocations','agent_settlements')`).Scan(&tabs); err != nil {
		t.Fatal(err)
	}
	if tabs != 3 { t.Fatalf("want 3 agent tables, got %d", tabs) }
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_name='console_users' AND column_name IN ('parent_id','credit_limit')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 2 { t.Fatalf("want parent_id+credit_limit, got %d", cols) }
}
```

- [ ] **Step 3: 跑测试** — `go test ./internal/api/ -run TestAgentSchemaApplies -count=1` → PASS
- [ ] **Step 4: Commit** — `git add migrations/0022_agent_distribution.sql internal/api/agent_db_test.go && git commit -m "feat(agent): 0022 hierarchy columns + cost pricing/allocations/settlements"`

> 防环不用 DB 触发器(复杂)，放应用层(Task 3 改父时用递归 CTE 校验目标不在自身子树)。

---

## Task 2: 子树递归 CTE + 结算计算（纯）

**Files:**
- Create: `internal/api/agent_query.go`
- Test: `internal/api/agent_query_test.go`

**Interfaces:**
- Produces:
  - `func subtreeCTE(rootArg string) string` — 返回一段 `WITH RECURSIVE agent_tree(id) AS (...)` SQL 片段，`rootArg` 是根代理 id 的位置参数(如 `"$1"`)。展开：根 + 所有下级代理(沿 parent_id)。
  - `type settlementInput struct { RetailDirect int64; CostDirect int64; RetailSubtree int64; RebateRate float64 }`
  - `func computeSettlement(in settlementInput) (debt, margin, rebate, net int64)` — `debt=in.CostDirect; margin=in.RetailDirect-in.CostDirect; rebate=round(in.RetailSubtree*in.RebateRate); net=margin+rebate-debt`。

- [ ] **Step 1: 写失败测试**

`internal/api/agent_query_test.go`:
```go
package api

import (
	"strings"
	"testing"
)

func TestSubtreeCTE_Shape(t *testing.T) {
	cte := subtreeCTE("$1")
	for _, want := range []string{"WITH RECURSIVE agent_tree", "SELECT id FROM console_users WHERE id = $1",
		"JOIN agent_tree", "u.parent_id = agent_tree.id"} {
		if !strings.Contains(cte, want) {
			t.Fatalf("cte missing %q:\n%s", want, cte)
		}
	}
}

func TestComputeSettlement(t *testing.T) {
	// direct retail 1000, direct cost 600 -> margin 400, debt 600.
	// subtree retail 5000 @ 10% rebate -> 500. net = 400+500-600 = 300.
	debt, margin, rebate, net := computeSettlement(settlementInput{
		RetailDirect: 1000, CostDirect: 600, RetailSubtree: 5000, RebateRate: 0.10})
	if debt != 600 || margin != 400 || rebate != 500 || net != 300 {
		t.Fatalf("got debt=%d margin=%d rebate=%d net=%d", debt, margin, rebate, net)
	}
}
```

- [ ] **Step 2: 跑确认失败** — `go test ./internal/api/ -run 'SubtreeCTE|ComputeSettlement' -count=1` → FAIL
- [ ] **Step 3: 实现**

`internal/api/agent_query.go`:
```go
package api

import "math"

// subtreeCTE returns a recursive CTE binding `agent_tree(id)` to the root agent
// (rootArg, e.g. "$1") and all descendant agents via console_users.parent_id.
// Consumers append their own SELECT that JOINs agent_tree, or filters
// tenants.sales_owner_id IN (SELECT id FROM agent_tree).
func subtreeCTE(rootArg string) string {
	return `WITH RECURSIVE agent_tree(id) AS (
    SELECT id FROM console_users WHERE id = ` + rootArg + `
    UNION
    SELECT u.id FROM console_users u JOIN agent_tree ON u.parent_id = agent_tree.id
)`
}

type settlementInput struct {
	RetailDirect  int64   // direct customers' settled retail consumption
	CostDirect    int64   // direct customers' consumption at platform cost
	RetailSubtree int64   // whole subtree settled retail consumption (rebate base)
	RebateRate    float64 // this agent's rebate rate (fraction)
}

// computeSettlement derives the monthly statement. round() matches SP7's
// per-actor rounding to avoid float drift.
func computeSettlement(in settlementInput) (debt, margin, rebate, net int64) {
	debt = in.CostDirect
	margin = in.RetailDirect - in.CostDirect
	rebate = int64(math.Round(float64(in.RetailSubtree) * in.RebateRate))
	net = margin + rebate - debt
	return
}
```

- [ ] **Step 4: 跑确认通过** — PASS
- [ ] **Step 5: Commit** — `git commit -am "feat(agent): subtree recursive-CTE builder + settlement math"`

---

## Task 3: 树管理 + 子树访问控制

**Files:** Modify `internal/api/agent_api.go`(新建), `router.go`; Test `agent_db_test.go`.

**Interfaces:**
- Consumes: `subtreeCTE`, `sessionFrom(c)`, `s.systemPool()`, `recordAuditTx`.
- Produces:
  - `func (s *Server) agentInSubtree(ctx, rootAgentID, targetAgentID int64) (bool, error)` — target 是否在 root 子树。
  - `func (s *Server) tenantInSubtree(ctx, rootAgentID, tenantID int64) (bool, error)` — 客户是否在 root 子树(sales_owner_id ∈ 子树)。
  - handlers: `handleAgentCreateSubAgent`(建下级代理，parent=自己)、`handleAdminSetAgentParent`(改父，防环)、`handleAdminSetAgentTerms`(设 rebate/credit_limit)。

- [ ] **Step 1: 写失败测试**（子树隔离 + 防环）

追加 `agent_db_test.go`:
```go
func TestTenantInSubtree_Isolation(t *testing.T) {
	s := &Server{sysPool: testPool(t)}
	ctx := context.Background()
	// tree: A(root) -> B(sub) ; C is a separate root. tenant tB under B, tC under C.
	a := seedAgent(t, ctx, s, "a@x", nil)
	b := seedAgent(t, ctx, s, "b@x", &a)
	c := seedAgent(t, ctx, s, "c@x", nil)
	tB := seedTenantUnderAgent(t, ctx, s, b)
	tC := seedTenantUnderAgent(t, ctx, s, c)

	if ok, _ := s.tenantInSubtree(ctx, a, tB); !ok {
		t.Fatal("A must see tenant under its sub-agent B")
	}
	if ok, _ := s.tenantInSubtree(ctx, a, tC); ok {
		t.Fatal("A must NOT see tenant under unrelated agent C")
	}
}
```
> `seedAgent(t,ctx,s,email,parent *int64)` inserts a `role='sales'` console_user with optional parent_id (RETURNING id). `seedTenantUnderAgent(t,ctx,s,agentID)` inserts a tenant with `sales_owner_id=agentID`. Add both helpers to agent_db_test.go (mirror existing seed helpers).

- [ ] **Step 2: 跑确认失败** — FAIL
- [ ] **Step 3: 实现** `agentInSubtree`/`tenantInSubtree` 用 `subtreeCTE`:
```go
func (s *Server) tenantInSubtree(ctx context.Context, rootAgentID, tenantID int64) (bool, error) {
	var ok bool
	err := s.systemPool().QueryRow(ctx,
		subtreeCTE("$1")+`
		SELECT EXISTS (SELECT 1 FROM tenants t
		  WHERE t.id = $2 AND t.sales_owner_id IN (SELECT id FROM agent_tree))`,
		rootAgentID, tenantID).Scan(&ok)
	return ok, err
}
```
`agentInSubtree` 同构(target agent id ∈ agent_tree)。`handleAdminSetAgentParent` 改父前用 `agentInSubtree(ctx, targetID, newParentID)` 判断——若 newParent 在 target 自己子树内则成环 → 400。建下级/设 terms 照 admin CRUD 范式 + `recordAuditTx`。子树受限的代理 handler 一律先 `tenantInSubtree(me.UserID, target)` 否则 403。

- [ ] **Step 4: 注册路由** `/sales/sub-agents`(POST 建下级)、`/admin/agents/:id/parent`、`/admin/agents/:id/terms`。
- [ ] **Step 5: 跑通** — PASS
- [ ] **Step 6: Commit** — `git commit -am "feat(agent): subtree access control + tree management (cycle-safe)"`

---

## Task 4: 成本价 admin CRUD

**Files:** Modify `internal/api/agent_admin.go`(新建), `router.go`; Test `agent_db_test.go`.

**Interfaces:** `handleAdminListCostPricing` / `handleAdminSetCostPricing`(upsert `agent_cost_pricing`，`recordAudit`).

- [ ] **Step 1: 失败测试**：set 成本价 CN=50 → list 返回 CN=50。
- [ ] **Step 2: 跑失败** — FAIL
- [ ] **Step 3: 实现** upsert `INSERT INTO agent_cost_pricing(country_code,unit_cost) VALUES($1,$2) ON CONFLICT(country_code) DO UPDATE SET unit_cost=$2, updated_at=now()`；list `SELECT country_code, unit_cost`。systemPool，admin-only，审计 `agent.cost_pricing_set`。
- [ ] **Step 4: 路由** `GET/POST /admin/agent/cost-pricing`。
- [ ] **Step 5: PASS** → **Commit** `git commit -am "feat(agent): platform cost pricing admin CRUD"`

---

## Task 5: 额度批量划拨（billing.Topup + 台账 + 额度校验，事务）

**Files:** Modify `internal/api/agent_api.go`, `router.go`; Test `agent_db_test.go`.

**Interfaces:**
- Consumes: `billing.Topup(ctx, tenantID, amount, ref)`, `tenantInSubtree`, `subtreeCTE`.
- Produces: `func (s *Server) agentAvailableCredit(ctx, agentID int64) (int64, error)` = `credit_limit − outstanding`；`handleAgentAllocate`(单/批)。

- [ ] **Step 1: 失败测试**（超限拒绝 + 事务原子 + 越权 403）

```go
func TestAgentAllocate_CreditGuardAndTopup(t *testing.T) {
	s := newAgentServer(t) // Server with Mgr(+Billing) + sysPool
	ctx := context.Background()
	ag := seedAgent(t, ctx, s, "ag@x", nil)
	setCreditLimit(t, ctx, s, ag, 1000)
	tn := seedTenantUnderAgent(t, ctx, s, ag)

	// allocate 600 -> ok, tenant wallet +600, outstanding 600
	if code := doAgentAllocate(t, s, ag, tn, 600); code != 200 {
		t.Fatalf("first allocate %d", code)
	}
	assertWalletBalance(t, ctx, s, tn, 600)
	// allocate 600 more -> exceeds available (1000-600=400) -> 402, wallet unchanged
	if code := doAgentAllocate(t, s, ag, tn, 600); code != 402 {
		t.Fatalf("over-limit should be 402, got %d", code)
	}
	assertWalletBalance(t, ctx, s, tn, 600) // no partial topup
}
```
> `newAgentServer` wires `Deps{Mgr, Billing: billing.NewRepo(...), Audit}` + sysPool (mirror finance/campaign DB tests for Billing wiring). Helpers `setCreditLimit`, `assertWalletBalance`, `doAgentAllocate` (posts with agent session `console.SessionData{UserID: agentID}`).

- [ ] **Step 2: 跑失败** — FAIL
- [ ] **Step 3: 实现** `agentAvailableCredit`:
```go
func (s *Server) agentAvailableCredit(ctx context.Context, agentID int64) (int64, error) {
	var available int64
	err := s.systemPool().QueryRow(ctx,
		subtreeCTE("$1")+`
		SELECT cu.credit_limit
		     - COALESCE((SELECT SUM(a.amount) FROM agent_allocations a WHERE a.agent_id=$1),0)
		     + COALESCE((SELECT SUM(bc.amount) FROM billing_charges bc
		                  WHERE bc.state='settled'
		                    AND bc.tenant_id IN (SELECT t.id FROM tenants t
		                         WHERE t.sales_owner_id IN (SELECT id FROM agent_tree))),0)
		  FROM console_users cu WHERE cu.id=$1`,
		agentID).Scan(&available)
	return available, err
}
```
> 即 `available = credit_limit − Σ划拨 + Σ子树已settle零售消耗`(消耗回收浮额)。**verify** billing_charges settled 状态枚举名(charge_state_t：held→settled)，与 SP7 wallet_ledger kind='settle' 对齐；若枚举是别的值就地改。
`handleAgentAllocate`: 校验 `tenantInSubtree(me,tenant)` 否则 403 → 校验 `amount ≤ agentAvailableCredit` 否则 402 → 单事务：`INSERT agent_allocations RETURNING id` → `billing.Topup(ctx, tenant, amount, fmt.Sprintf("alloc:%d", allocID))` → `recordAuditTx(agent.allocate)` → commit。批量 = 循环多个 tenant，各自校验，全过才 commit(或逐条报告——**v1 全过才 commit**)。

- [ ] **Step 4: 路由** `POST /sales/allocate`(body `{items:[{tenant_id,amount}]}`)。
- [ ] **Step 5: PASS**(含越权 403、超限 402 回滚) → **Commit** `git commit -am "feat(agent): bulk credit allocation via billing.Topup + credit-limit guard"`

---

## Task 6: 消耗汇总 + 月结三项计算 + 结算落库

**Files:** Modify `internal/api/agent_api.go`, `agent_admin.go`, `router.go`; Test `agent_db_test.go`.

**Interfaces:**
- Consumes: `subtreeCTE`, `computeSettlement`, `parseMonth`, `billing_charges`, `agent_cost_pricing`.
- Produces: `func (s *Server) agentSettlement(ctx, agentID int64, from, to time.Time) (settlementInput, error)`；`handleAgentStatement`(代理看自己月结)、`handleAdminSettlementOverview`(平台看全部)、`handleAdminCloseSettlement`(落 agent_settlements，幂等 UNIQUE(agent_id,period))。

- [ ] **Step 1: 失败测试**（三项对账，多级）

seed：agent A → sub B；B 的直属客户 tB 当月 settle 消耗 CN 10 条、成本价 CN=50、tB 零售价 CN=80。则 tB retail=800, cost=500。B: margin=300,debt=500,rebate(B 无下线)=0。A: 直属 0，subtree retail=800，A rebate 10% → 80；A debt=0,margin=0,net=80。断言 `agentSettlement` + `computeSettlement` 得此。

- [ ] **Step 2: 跑失败** — FAIL
- [ ] **Step 3: 实现** `agentSettlement` 用两段查询(均 systemPool + subtreeCTE)：
  - 直属(sales_owner_id = agentID 本人)：`RetailDirect = SUM(bc.amount)`；`CostDirect = SUM(cost.unit_cost)` 按 `billing_charges bc JOIN agent_cost_pricing cost ON cost.country_code=bc.country_code`，`bc.state='settled' AND bc.created_at∈[from,to)`，tenant ∈ 本人直属。
  - 子树：`RetailSubtree = SUM(bc.amount)` where tenant.sales_owner_id ∈ agent_tree(全子树)。
  - `RebateRate` = 本代理 `commission_rate`(NUMERIC→float)。
  - → `computeSettlement(in)`。
  `handleAdminCloseSettlement`: 对每个代理算当月 → `INSERT INTO agent_settlements(...) ON CONFLICT(agent_id,period) DO NOTHING`(幂等)，审计 `agent.settlement_close`。
- [ ] **Step 4: 路由** `GET /sales/statement?month=`、`GET /admin/agent/settlements?month=`、`POST /admin/agent/settlements/close?month=`。
- [ ] **Step 5: PASS** → **Commit** `git commit -am "feat(agent): consumption rollup + monthly settlement (debt/margin/rebate)"`

---

## Task 7: 代理独立后台前端

**Files:** Create `frontend/app/sales/*` 扩展 + `frontend/components/sales/agent-*.tsx`; Modify sales nav.

- [ ] **Step 1** 下线树 + 消耗汇总视图(读 `/sales/customers` 扩展 + 新汇总接口)。
- [ ] **Step 2** 额度用量卡(limit/outstanding/available) + **批量划拨**弹框(选客户+额度 → POST /sales/allocate → 报告)。
- [ ] **Step 3** 月结单页(欠款/差价/返佣/净额，月份选择器，读 /sales/statement)。
- [ ] **Step 4** 建下级代理 + 客户改零售价(复用现有 sales pricing)。
- [ ] **Step 5** `cd frontend && npm run build && npx eslint .` — build 过；eslint 无新规则类别。
- [ ] **Step 6** Commit `git commit -am "feat(agent): agent portal (tree/rollup/credit/statement/allocate)"`

---

## Task 8: admin 侧前端

**Files:** Create `frontend/app/admin/agents/*` + 组件; Modify `frontend/lib/nav.ts`.

- [ ] **Step 1** 成本价 CRUD 页(读/写 /admin/agent/cost-pricing)。
- [ ] **Step 2** 代理树总览 + 设 rebate/credit_limit/改父。
- [ ] **Step 3** 结算总览页(读 /admin/agent/settlements + 关账 close)。
- [ ] **Step 4** nav 加"代理分销"组。
- [ ] **Step 5** build + lint。
- [ ] **Step 6** Commit `git commit -am "feat(agent): admin distribution console (cost/terms/settlement)"`

---

## 收尾
- [ ] `make gate` 全绿(vuln 仅预存 GO-2026-5856)。
- [ ] `superpowers:requesting-code-review` 全分支终审：重点 **billing.Topup 事务原子/额度校验无并发漏洞/子树隔离无越权/结算三项对账/防环/红线 billing 未改**。
- [ ] 更新记忆(新建模块九进度)。

## Self-Review 覆盖核对
- spec 实体层级→T1；子树 CTE→T2/T3;定价成本价→T4;额度划拨(Topup+台账+额度)→T5;差价+多级返佣+月结→T6;代理后台→T7;admin→T8。**全覆盖**。
- 资金三轴口径：outstanding(T5 agentAvailableCredit)、debt/margin/rebate(T6 computeSettlement/agentSettlement)一致。
- 类型一致：`settlementInput`/`computeSettlement`(T2) 被 T6 消费；`subtreeCTE`(T2) 被 T3/T5/T6 消费；`tenantInSubtree`(T3) 被 T5/T6 消费。
- 红线：唯一 billing 调用 = `billing.Topup`(T5)；其余全只读计算。
- **待实现期核实**：billing_charges settled 枚举名(T5/T6 注记)。
