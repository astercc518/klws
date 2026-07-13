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
