// internal/api/sales_api.go — sales controllers.
//
// A sales user only ever sees/acts on tenants where tenants.sales_owner_id
// equals their own console_users.id. Every per-tenant route re-checks ownership
// (salesOwns) and returns 403 otherwise — the same guard the old console used.
// Reads use the SystemPool; pricing writes reuse pricing.SetPrice.
package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// salesOwns reports whether the session's sales user owns the given tenant.
func (s *Server) salesOwns(ctx context.Context, salesUserID, tenantID int64) (bool, error) {
	var owner *int64
	err := s.deps.Mgr.SystemPool().QueryRow(ctx,
		`SELECT sales_owner_id FROM tenants WHERE id=$1`, tenantID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return owner != nil && *owner == salesUserID, nil
}

type salesCustomerRow struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Balance int64  `json:"balance"`
	Frozen  int64  `json:"frozen"`
}

// handleSalesCustomers: GET /api/v1/sales/customers — the caller's own tenants.
func (s *Server) handleSalesCustomers(c *gin.Context) {
	me := sessionFrom(c)
	if me == nil {
		fail(c, http.StatusUnauthorized, "no session")
		return
	}
	rows, err := s.deps.Mgr.SystemPool().Query(c.Request.Context(), `
SELECT t.id, t.name, t.status, COALESCE(w.balance,0), COALESCE(w.frozen,0)
  FROM tenants t
  LEFT JOIN tenant_wallets w ON w.tenant_id = t.id
 WHERE t.sales_owner_id = $1
 ORDER BY t.id`, me.UserID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list customers")
		return
	}
	defer rows.Close()
	out := make([]salesCustomerRow, 0)
	for rows.Next() {
		var r salesCustomerRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Status, &r.Balance, &r.Frozen); err != nil {
			fail(c, http.StatusInternalServerError, "scan customer")
			return
		}
		out = append(out, r)
	}
	ok(c, out)
}

// handleSalesCustomerDetail: GET /api/v1/sales/customers/:id (must own it).
func (s *Server) handleSalesCustomerDetail(c *gin.Context) {
	me := sessionFrom(c)
	tenantID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if me == nil || err != nil {
		fail(c, http.StatusBadRequest, "bad tenant id")
		return
	}
	owns, err := s.salesOwns(c.Request.Context(), me.UserID, tenantID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "ownership check failed")
		return
	}
	if !owns {
		fail(c, http.StatusForbidden, "not your customer")
		return
	}
	var r salesCustomerRow
	if err := s.deps.Mgr.SystemPool().QueryRow(c.Request.Context(), `
SELECT t.id, t.name, t.status, COALESCE(w.balance,0), COALESCE(w.frozen,0)
  FROM tenants t
  LEFT JOIN tenant_wallets w ON w.tenant_id = t.id
 WHERE t.id = $1`, tenantID).Scan(&r.ID, &r.Name, &r.Status, &r.Balance, &r.Frozen); err != nil {
		fail(c, http.StatusInternalServerError, "load customer")
		return
	}
	ok(c, r)
}

// handleSalesSetPricing: POST /api/v1/sales/customers/:id/pricing
// {country, unit_price} — sets a per-customer country price (must own it).
func (s *Server) handleSalesSetPricing(c *gin.Context) {
	me := sessionFrom(c)
	tenantID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if me == nil || err != nil {
		fail(c, http.StatusBadRequest, "bad tenant id")
		return
	}
	var req struct {
		Country   string `json:"country" binding:"required,len=2"`
		UnitPrice int64  `json:"unit_price" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "country(2) and unit_price required")
		return
	}
	if req.UnitPrice <= 0 {
		fail(c, http.StatusBadRequest, "unit_price must be positive")
		return
	}
	owns, err := s.salesOwns(c.Request.Context(), me.UserID, tenantID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "ownership check failed")
		return
	}
	if !owns {
		fail(c, http.StatusForbidden, "not your customer")
		return
	}
	if err := s.deps.Pricing.SetPrice(c.Request.Context(), tenantID, req.Country, req.UnitPrice); err != nil {
		fail(c, http.StatusInternalServerError, "set price failed")
		return
	}
	ok(c, gin.H{"tenant_id": tenantID, "country": req.Country, "unit_price": req.UnitPrice})
}
