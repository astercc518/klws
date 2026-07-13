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
