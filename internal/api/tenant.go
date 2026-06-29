// internal/api/tenant.go — Tenant controller (customer self-service).
//
// All tenant-scoped reads go through s.deps.Mgr.WithTenant(ctx, tenantID),
// i.e. the exact RLS transaction the console used. Postgres FORCE ROW LEVEL
// SECURITY remains the backstop — this layer never touches a cross-tenant pool
// for customer data.
package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// walletResponse is the data payload for GET /api/v1/tenant/wallet.
// Amounts are in the smallest currency unit (matching billing's model).
type walletResponse struct {
	Balance int64 `json:"balance"` // spendable
	Frozen  int64 `json:"frozen"`  // held for in-flight campaigns
}

// statsResponse bundles every dashboard metric in one RLS round-trip.
type statsResponse struct {
	Balance        int64 `json:"balance"`
	Frozen         int64 `json:"frozen"`
	AccountsOnline int   `json:"accounts_online"` // ban_status='active'
	AccountsTotal  int   `json:"accounts_total"`
	SentToday      int   `json:"sent_today"` // recipients delivered since 00:00
}

// tenantInfoResponse is the data payload for GET /api/v1/tenant/info.
type tenantInfoResponse struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// tenantID returns the session's tenant id, or false if the session is not a
// customer with a tenant (shouldn't happen behind requireRole(customer)).
func tenantID(c *gin.Context) (int64, bool) {
	data := sessionFrom(c)
	if data == nil || data.TenantID == nil {
		return 0, false
	}
	return *data.TenantID, true
}

// handleWallet: GET /api/v1/tenant/wallet (customer only).
func (s *Server) handleWallet(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "wallet unavailable")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	bal, frozen, err := scanWallet(ctx, tx)
	if err != nil {
		fail(c, http.StatusInternalServerError, "wallet read failed")
		return
	}
	ok(c, walletResponse{Balance: bal, Frozen: frozen})
}

// handleStats: GET /api/v1/tenant/stats (customer only). One RLS transaction
// feeds all four dashboard cards.
func (s *Server) handleStats(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "stats unavailable")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	bal, frozen, err := scanWallet(ctx, tx)
	if err != nil {
		fail(c, http.StatusInternalServerError, "stats read failed")
		return
	}

	var total, online int
	if err := tx.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE ban_status = 'active') FROM account_devices`,
	).Scan(&total, &online); err != nil {
		fail(c, http.StatusInternalServerError, "stats accounts failed")
		return
	}

	var sentToday int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM campaign_recipients
		  WHERE state = 'sent' AND updated_at >= date_trunc('day', now())`,
	).Scan(&sentToday); err != nil {
		fail(c, http.StatusInternalServerError, "stats sends failed")
		return
	}

	ok(c, statsResponse{
		Balance:        bal,
		Frozen:         frozen,
		AccountsOnline: online,
		AccountsTotal:  total,
		SentToday:      sentToday,
	})
}

// handleTenantInfo: GET /api/v1/tenant/info (customer only). The tenants table
// has no RLS, so it is read via the registry repo (SystemPool) but strictly
// scoped to the session's own tenant id.
func (s *Server) handleTenantInfo(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	t, err := s.deps.Tenants.Get(c.Request.Context(), tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "tenant info unavailable")
		return
	}
	ok(c, tenantInfoResponse{ID: t.ID, Name: t.Name, Status: t.Status})
}

// scanWallet reads balance+frozen on an RLS tx; a missing wallet row means 0,0.
func scanWallet(ctx context.Context, tx pgx.Tx) (balance, frozen int64, err error) {
	err = tx.QueryRow(ctx, `SELECT balance, frozen FROM tenant_wallets`).Scan(&balance, &frozen)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	return balance, frozen, err
}
