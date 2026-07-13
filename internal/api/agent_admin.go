package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
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
