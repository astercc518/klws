package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// agent_admin.go holds admin-side, cross-tenant handlers for module 9 (agent
// distribution): platform cost pricing / country. All reads and writes go
// through systemPool() (BYPASSRLS) — this data has no tenant_id and is not
// covered by RLS.

// costPricingRow is one country's platform unit cost (minor units).
type costPricingRow struct {
	CountryCode string `json:"country_code"`
	UnitCost    int64  `json:"unit_cost"`
}

// handleAdminListCostPricing: GET /admin/agent/cost-pricing — all rows,
// ordered by country_code.
func (s *Server) handleAdminListCostPricing(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := s.systemPool().Query(ctx,
		`SELECT country_code, unit_cost FROM agent_cost_pricing ORDER BY country_code`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list cost pricing")
		return
	}
	defer rows.Close()
	out := make([]costPricingRow, 0)
	for rows.Next() {
		var r costPricingRow
		if err := rows.Scan(&r.CountryCode, &r.UnitCost); err != nil {
			fail(c, http.StatusInternalServerError, "scan cost pricing")
			return
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate cost pricing")
		return
	}
	ok(c, out)
}

// handleAdminSetCostPricing: POST /admin/agent/cost-pricing
// {country_code,unit_cost} — upsert the platform cost price for a country.
func (s *Server) handleAdminSetCostPricing(c *gin.Context) {
	var req struct {
		CountryCode string `json:"country_code" binding:"required,len=2"`
		// UnitCost is a *pointer* so a MISSING field (nil) is distinguishable
		// from an explicit 0. gin's `required` rejects nil (missing) with 400
		// but accepts a pointer-to-zero (explicit 0) — critical because T5/T6
		// join this table for money math, so a forgotten field must NOT
		// silently write a free-tier (0) cost.
		UnitCost *int64 `json:"unit_cost" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "country_code(2), unit_cost required")
		return
	}
	if req.UnitCost == nil {
		fail(c, http.StatusBadRequest, "unit_cost required")
		return
	}
	if *req.UnitCost < 0 {
		fail(c, http.StatusBadRequest, "unit_cost must be >= 0")
		return
	}
	ctx := c.Request.Context()
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO agent_cost_pricing (country_code, unit_cost) VALUES ($1,$2)
		 ON CONFLICT (country_code) DO UPDATE SET unit_cost=$2, updated_at=now()`,
		req.CountryCode, *req.UnitCost); err != nil {
		fail(c, http.StatusInternalServerError, "set cost pricing failed")
		return
	}
	s.recordAudit(ctx, auditEvent{TenantID: 0, ActorID: actorID(c),
		Action: "agent.cost_pricing_set", ResourceType: "agent_cost_pricing", ResourceID: 0,
		Details: map[string]any{"country_code": req.CountryCode, "unit_cost": *req.UnitCost}})
	ok(c, gin.H{"country_code": req.CountryCode, "unit_cost": *req.UnitCost})
}

// --- Monthly settlement overview / close (T6) -------------------------------
//
// MONEY-CRITICAL. Both handlers enumerate every role='sales' console_user and
// run agentSettlement (agent_api.go) per agent — there is no subtree scoping
// here since this IS the platform "god view" (mirrors handleAdminCommissions'
// shape, per-agent rows for ALL agents, not just roots).

// handleAdminSettlementOverview: GET /api/v1/admin/agent/settlements?month=
// — per-agent monthly settlement (debt/margin/rebate/net) for every sales
// agent on the platform.
func (s *Server) handleAdminSettlementOverview(c *gin.Context) {
	ctx := c.Request.Context()
	from, to, err := parseMonth(c.Query("month"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.systemPool().Query(ctx,
		`SELECT id FROM console_users WHERE role='sales' ORDER BY id`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list agents failed")
		return
	}
	var agentIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			fail(c, http.StatusInternalServerError, "scan agent failed")
			return
		}
		agentIDs = append(agentIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate agents failed")
		return
	}

	period := from.Format("2006-01")
	out := make([]gin.H, 0, len(agentIDs))
	for _, id := range agentIDs {
		in, err := s.agentSettlement(ctx, id, from, to)
		if err != nil {
			fail(c, http.StatusInternalServerError, "settlement computation failed")
			return
		}
		out = append(out, settlementJSON(id, period, in))
	}
	ok(c, gin.H{"period": period, "agents": out})
}

// handleAdminCloseSettlement: POST /api/v1/admin/agent/settlements/close?month=
// — computes every sales agent's settlement for the month and INSERTs one
// agent_settlements row per agent, ON CONFLICT (agent_id, period) DO NOTHING
// so re-closing an already-closed month is a no-op: it neither double-writes
// nor changes the previously-recorded (possibly since-superseded) figures.
// Each newly-inserted row is audited individually inside its own tx; a
// no-op (already closed) skips the audit write — there is nothing new to log.
func (s *Server) handleAdminCloseSettlement(c *gin.Context) {
	ctx := c.Request.Context()
	from, to, err := parseMonth(c.Query("month"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	period := from.Format("2006-01")

	rows, err := s.systemPool().Query(ctx,
		`SELECT id FROM console_users WHERE role='sales' ORDER BY id`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list agents failed")
		return
	}
	var agentIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			fail(c, http.StatusInternalServerError, "scan agent failed")
			return
		}
		agentIDs = append(agentIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate agents failed")
		return
	}

	actor := actorID(c)
	closed := make([]gin.H, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		in, err := s.agentSettlement(ctx, agentID, from, to)
		if err != nil {
			fail(c, http.StatusInternalServerError, "settlement computation failed")
			return
		}
		debt, margin, rebate, net := computeSettlement(in)
		err = pgx.BeginTxFunc(ctx, s.systemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
			tag, e := tx.Exec(ctx,
				`INSERT INTO agent_settlements (agent_id, period, debt, margin, rebate, net)
				 VALUES ($1,$2,$3,$4,$5,$6)
				 ON CONFLICT (agent_id, period) DO NOTHING`,
				agentID, period, debt, margin, rebate, net)
			if e != nil {
				return e
			}
			if tag.RowsAffected() == 0 {
				return nil // already closed this month: idempotent no-op
			}
			return s.recordAuditTx(ctx, tx, auditEvent{TenantID: 0, ActorID: actor,
				Action: "agent.settlement_close", ResourceType: "agent_settlement", ResourceID: agentID,
				Details: map[string]any{"period": period, "debt": debt, "margin": margin, "rebate": rebate, "net": net}})
		})
		if err != nil {
			fail(c, http.StatusInternalServerError, "close settlement failed")
			return
		}
		closed = append(closed, gin.H{"agent_id": agentID, "period": period,
			"debt": debt, "margin": margin, "rebate": rebate, "net": net})
	}
	ok(c, gin.H{"period": period, "closed": closed})
}
