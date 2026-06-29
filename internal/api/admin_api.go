// internal/api/admin_api.go — super-admin ("god view") controllers.
//
// Every handler here sits behind requireAuth + requireRole(admin). Reads/writes
// use the BYPASSRLS SystemPool for cross-tenant platform tables, and reuse the
// existing repos (billing.Topup, pricing.SetPrice, console.UserRepo,
// console.TenantRepo) for anything that already has business logic. New writes
// that the codebase never had (create tenant, import proxies/devices, pause a
// campaign) are plain INSERT/UPDATEs from this layer — no red-line package
// (billing/dispatch/store/sendgate/cluster) is modified.
package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/console"
)

// ---------------------------------------------------------------------------
// Global dashboard / audit
// ---------------------------------------------------------------------------

type adminStats struct {
	TotalTopup     int64   `json:"total_topup"`     // gross loaded (smallest unit)
	TotalBalance   int64   `json:"total_balance"`   // spendable across all wallets
	TotalFrozen    int64   `json:"total_frozen"`    // held across all wallets
	Consumed       int64   `json:"consumed"`        // topup - balance - frozen (settled spend)
	AccountsTotal  int     `json:"accounts_total"`  // all WA devices
	AccountsActive int     `json:"accounts_active"` // ban_status='active'
	AccountsBanned int     `json:"accounts_banned"` // banned|flagged
	BanRate        float64 `json:"ban_rate"`        // banned / total
	QueueBacklog   int     `json:"queue_backlog"`   // pending recipients (asynq backlog proxy)
}

// handleAdminStats: GET /api/v1/admin/stats.
func (s *Server) handleAdminStats(c *gin.Context) {
	ctx := c.Request.Context()
	pool := s.deps.Mgr.SystemPool()
	var st adminStats

	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(balance),0), COALESCE(SUM(frozen),0) FROM tenant_wallets`,
	).Scan(&st.TotalBalance, &st.TotalFrozen); err != nil {
		fail(c, http.StatusInternalServerError, "stats: wallets")
		return
	}
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(delta_balance),0) FROM wallet_ledger WHERE kind='topup'`,
	).Scan(&st.TotalTopup); err != nil {
		fail(c, http.StatusInternalServerError, "stats: topup")
		return
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*),
		        count(*) FILTER (WHERE ban_status='active'),
		        count(*) FILTER (WHERE ban_status IN ('banned','flagged'))
		   FROM account_devices`,
	).Scan(&st.AccountsTotal, &st.AccountsActive, &st.AccountsBanned); err != nil {
		fail(c, http.StatusInternalServerError, "stats: devices")
		return
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM campaign_recipients WHERE state='pending'`,
	).Scan(&st.QueueBacklog); err != nil {
		fail(c, http.StatusInternalServerError, "stats: backlog")
		return
	}

	st.Consumed = st.TotalTopup - st.TotalBalance - st.TotalFrozen
	if st.AccountsTotal > 0 {
		st.BanRate = float64(st.AccountsBanned) / float64(st.AccountsTotal)
	}
	ok(c, st)
}

// ---------------------------------------------------------------------------
// Tenants & IAM
// ---------------------------------------------------------------------------

type adminTenantRow struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	SalesOwnerID *int64 `json:"sales_owner_id"`
	Balance      int64  `json:"balance"`
	Frozen       int64  `json:"frozen"`
}

// handleAdminListTenants: GET /api/v1/admin/tenants (with wallet balances).
func (s *Server) handleAdminListTenants(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := s.deps.Mgr.SystemPool().Query(ctx, `
SELECT t.id, t.name, t.status, t.sales_owner_id,
       COALESCE(w.balance,0), COALESCE(w.frozen,0)
  FROM tenants t
  LEFT JOIN tenant_wallets w ON w.tenant_id = t.id
 ORDER BY t.id`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list tenants")
		return
	}
	defer rows.Close()
	out := make([]adminTenantRow, 0)
	for rows.Next() {
		var t adminTenantRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Status, &t.SalesOwnerID, &t.Balance, &t.Frozen); err != nil {
			fail(c, http.StatusInternalServerError, "scan tenant")
			return
		}
		out = append(out, t)
	}
	ok(c, out)
}

// handleAdminCreateTenant: POST /api/v1/admin/tenants {name}.
func (s *Server) handleAdminCreateTenant(c *gin.Context) {
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name is required")
		return
	}
	var id int64
	if err := s.deps.Mgr.SystemPool().QueryRow(c.Request.Context(),
		`INSERT INTO tenants (name, status) VALUES ($1,'active') RETURNING id`, req.Name,
	).Scan(&id); err != nil {
		fail(c, http.StatusInternalServerError, "create tenant failed")
		return
	}
	ok(c, gin.H{"id": id, "name": req.Name, "status": "active"})
}

type adminUserRow struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	TenantID *int64 `json:"tenant_id"`
	Disabled bool   `json:"disabled"`
}

// handleAdminListUsers: GET /api/v1/admin/users.
func (s *Server) handleAdminListUsers(c *gin.Context) {
	rows, err := s.deps.Mgr.SystemPool().Query(c.Request.Context(),
		`SELECT id, email, role, tenant_id, disabled FROM console_users ORDER BY id`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list users")
		return
	}
	defer rows.Close()
	out := make([]adminUserRow, 0)
	for rows.Next() {
		var u adminUserRow
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.TenantID, &u.Disabled); err != nil {
			fail(c, http.StatusInternalServerError, "scan user")
			return
		}
		out = append(out, u)
	}
	ok(c, out)
}

// handleAdminCreateUser: POST /api/v1/admin/users {email,password,role,tenant_id?}.
// A customer must carry a tenant_id; admin/sales must not.
func (s *Server) handleAdminCreateUser(c *gin.Context) {
	var req struct {
		Email    string `json:"email" binding:"required,email"`
		Password string `json:"password" binding:"required,min=8"`
		Role     string `json:"role" binding:"required"`
		TenantID *int64 `json:"tenant_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body (email, password>=8, role required)")
		return
	}
	role := console.Role(req.Role)
	switch role {
	case console.RoleAdmin, console.RoleSales:
		req.TenantID = nil // these roles are tenant-less
	case console.RoleCustomer:
		if req.TenantID == nil || *req.TenantID <= 0 {
			fail(c, http.StatusBadRequest, "customer requires a tenant_id")
			return
		}
	default:
		fail(c, http.StatusBadRequest, "role must be admin, sales, or customer")
		return
	}
	hash, err := console.HashPassword(req.Password)
	if err != nil {
		fail(c, http.StatusInternalServerError, "hash password failed")
		return
	}
	id, err := s.deps.Users.Create(c.Request.Context(), req.Email, hash, role, req.TenantID)
	if err != nil {
		// Most likely a duplicate email (UNIQUE) — report as a conflict.
		fail(c, http.StatusConflict, "create user failed (email may already exist)")
		return
	}
	ok(c, gin.H{"id": id, "email": req.Email, "role": req.Role, "tenant_id": req.TenantID})
}

// handleAdminAssignSales: POST /api/v1/admin/sales/:id/assign {sales_user_id}.
// :id is the tenant (customer) id being placed under a sales owner.
func (s *Server) handleAdminAssignSales(c *gin.Context) {
	tenantID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad tenant id")
		return
	}
	var req struct {
		SalesUserID int64 `json:"sales_user_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "sales_user_id is required")
		return
	}
	if err := s.deps.Tenants.SetSalesOwner(c.Request.Context(), tenantID, req.SalesUserID); err != nil {
		if errors.Is(err, console.ErrNotSalesUser) {
			fail(c, http.StatusBadRequest, "target user is not a sales user")
			return
		}
		fail(c, http.StatusInternalServerError, "assign sales failed")
		return
	}
	ok(c, gin.H{"tenant_id": tenantID, "sales_user_id": req.SalesUserID})
}

// ---------------------------------------------------------------------------
// Finance & pricing
// ---------------------------------------------------------------------------

// handleAdminTopup: POST /api/v1/admin/finance/topup {tenant_id,amount,ref}.
// Reuses billing.Topup (idempotent by ref). The billing engine only credits
// (amount>0); debit is intentionally not exposed here.
func (s *Server) handleAdminTopup(c *gin.Context) {
	var req struct {
		TenantID int64  `json:"tenant_id" binding:"required"`
		Amount   int64  `json:"amount" binding:"required"`
		Ref      string `json:"ref" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "tenant_id, amount, ref are required")
		return
	}
	if req.Amount <= 0 {
		fail(c, http.StatusBadRequest, "amount must be positive (the billing engine does not debit)")
		return
	}
	if err := s.deps.Billing.Topup(c.Request.Context(), req.TenantID, req.Amount, req.Ref); err != nil {
		fail(c, http.StatusInternalServerError, "topup failed: "+err.Error())
		return
	}
	ok(c, gin.H{"tenant_id": req.TenantID, "amount": req.Amount, "ref": req.Ref})
}

// handleAdminSetPricing: POST /api/v1/admin/finance/pricing {tenant_id,country,unit_price}.
func (s *Server) handleAdminSetPricing(c *gin.Context) {
	var req struct {
		TenantID  int64  `json:"tenant_id" binding:"required"`
		Country   string `json:"country" binding:"required,len=2"`
		UnitPrice int64  `json:"unit_price" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "tenant_id, country(2), unit_price required")
		return
	}
	if req.UnitPrice <= 0 {
		fail(c, http.StatusBadRequest, "unit_price must be positive")
		return
	}
	if err := s.deps.Pricing.SetPrice(c.Request.Context(), req.TenantID, req.Country, req.UnitPrice); err != nil {
		fail(c, http.StatusInternalServerError, "set price failed")
		return
	}
	ok(c, gin.H{"tenant_id": req.TenantID, "country": req.Country, "unit_price": req.UnitPrice})
}

type ledgerRow struct {
	ID           int64  `json:"id"`
	TenantID     int64  `json:"tenant_id"`
	Kind         string `json:"kind"`
	DeltaBalance int64  `json:"delta_balance"`
	DeltaFrozen  int64  `json:"delta_frozen"`
	BalanceAfter int64  `json:"balance_after"`
	CreatedAt    string `json:"created_at"`
}

type refundRow struct {
	ID       int64  `json:"id"`
	TenantID int64  `json:"tenant_id"`
	Amount   int64  `json:"amount"`
	Reason   string `json:"reason"`
	State    string `json:"state"`
}

// handleAdminLedger: GET /api/v1/admin/finance/ledger — platform-wide money
// trail + recent refund requests (audit).
func (s *Server) handleAdminLedger(c *gin.Context) {
	ctx := c.Request.Context()
	pool := s.deps.Mgr.SystemPool()

	lrows, err := pool.Query(ctx, `
SELECT id, tenant_id, kind, delta_balance, delta_frozen, balance_after, created_at::text
  FROM wallet_ledger ORDER BY id DESC LIMIT 200`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "ledger query")
		return
	}
	defer lrows.Close()
	ledger := make([]ledgerRow, 0)
	for lrows.Next() {
		var l ledgerRow
		if err := lrows.Scan(&l.ID, &l.TenantID, &l.Kind, &l.DeltaBalance, &l.DeltaFrozen, &l.BalanceAfter, &l.CreatedAt); err != nil {
			fail(c, http.StatusInternalServerError, "scan ledger")
			return
		}
		ledger = append(ledger, l)
	}

	rrows, err := pool.Query(ctx, `
SELECT id, tenant_id, amount, reason, state::text
  FROM refund_requests ORDER BY id DESC LIMIT 100`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "refunds query")
		return
	}
	defer rrows.Close()
	refunds := make([]refundRow, 0)
	for rrows.Next() {
		var r refundRow
		if err := rrows.Scan(&r.ID, &r.TenantID, &r.Amount, &r.Reason, &r.State); err != nil {
			fail(c, http.StatusInternalServerError, "scan refund")
			return
		}
		refunds = append(refunds, r)
	}

	ok(c, gin.H{"ledger": ledger, "refunds": refunds})
}

// ---------------------------------------------------------------------------
// Infrastructure: proxy pool + device pool
// ---------------------------------------------------------------------------

type proxyRow struct {
	ID              int64  `json:"id"`
	URL             string `json:"proxy_url"`
	Type            string `json:"proxy_type"`
	Country         string `json:"country_code"`
	IsAlive         bool   `json:"is_alive"`
	CurrentBindings int    `json:"current_bindings"`
	MaxBindings     int    `json:"max_bindings"`
	FailureCount    int    `json:"failure_count"`
}

// handleAdminListProxies: GET /api/v1/admin/resources/proxies.
func (s *Server) handleAdminListProxies(c *gin.Context) {
	rows, err := s.deps.Mgr.SystemPool().Query(c.Request.Context(), `
SELECT id, proxy_url, proxy_type::text, country_code, is_alive, current_bindings, max_bindings, failure_count
  FROM proxy_pool ORDER BY id DESC LIMIT 500`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list proxies")
		return
	}
	defer rows.Close()
	out := make([]proxyRow, 0)
	for rows.Next() {
		var p proxyRow
		if err := rows.Scan(&p.ID, &p.URL, &p.Type, &p.Country, &p.IsAlive, &p.CurrentBindings, &p.MaxBindings, &p.FailureCount); err != nil {
			fail(c, http.StatusInternalServerError, "scan proxy")
			return
		}
		out = append(out, p)
	}
	ok(c, out)
}

// handleAdminImportProxies: POST /api/v1/admin/resources/proxies
// { proxies: [{url, type?, country}] }. type defaults to socks5. Existing
// proxy_url rows are skipped (ON CONFLICT DO NOTHING).
func (s *Server) handleAdminImportProxies(c *gin.Context) {
	var req struct {
		Proxies []struct {
			URL     string `json:"url" binding:"required"`
			Type    string `json:"type"`
			Country string `json:"country" binding:"required,len=2"`
		} `json:"proxies" binding:"required,dive"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "proxies[].url and 2-letter country are required")
		return
	}
	ctx := c.Request.Context()
	pool := s.deps.Mgr.SystemPool()
	imported := 0
	for _, p := range req.Proxies {
		typ := p.Type
		if typ == "" {
			typ = "socks5"
		}
		tag, err := pool.Exec(ctx, `
INSERT INTO proxy_pool (proxy_url, proxy_type, country_code)
VALUES ($1, $2, $3) ON CONFLICT (proxy_url) DO NOTHING`, p.URL, typ, p.Country)
		if err != nil {
			fail(c, http.StatusInternalServerError, "import proxy failed: "+err.Error())
			return
		}
		imported += int(tag.RowsAffected())
	}
	ok(c, gin.H{"submitted": len(req.Proxies), "imported": imported, "skipped": len(req.Proxies) - imported})
}

type deviceRow struct {
	ID              int64   `json:"id"`
	TenantID        int64   `json:"tenant_id"`
	AccountJID      string  `json:"account_jid"`
	Phone           string  `json:"phone_number"`
	BanStatus       string  `json:"ban_status"`
	OwnerNode       *string `json:"owner_node"`
	LastConnectedAt *string `json:"last_connected_at"`
}

// handleAdminListDevices: GET /api/v1/admin/resources/devices (cross-tenant).
func (s *Server) handleAdminListDevices(c *gin.Context) {
	rows, err := s.deps.Mgr.SystemPool().Query(c.Request.Context(), `
SELECT id, tenant_id, account_jid, phone_number, ban_status::text, owner_node, last_connected_at::text
  FROM account_devices ORDER BY id DESC LIMIT 500`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list devices")
		return
	}
	defer rows.Close()
	out := make([]deviceRow, 0)
	for rows.Next() {
		var d deviceRow
		if err := rows.Scan(&d.ID, &d.TenantID, &d.AccountJID, &d.Phone, &d.BanStatus, &d.OwnerNode, &d.LastConnectedAt); err != nil {
			fail(c, http.StatusInternalServerError, "scan device")
			return
		}
		out = append(out, d)
	}
	ok(c, out)
}

// handleAdminImportDevices: POST /api/v1/admin/resources/devices
// { devices: [{tenant_id, account_jid, phone, push_name?}] }. Inserts metadata
// rows (status 'init'); actual WhatsApp QR pairing is a node-side flow in
// cmd/wadist and is not performed here.
func (s *Server) handleAdminImportDevices(c *gin.Context) {
	var req struct {
		Devices []struct {
			TenantID   int64  `json:"tenant_id" binding:"required"`
			AccountJID string `json:"account_jid" binding:"required"`
			Phone      string `json:"phone" binding:"required"`
			PushName   string `json:"push_name"`
		} `json:"devices" binding:"required,dive"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "devices[].tenant_id, account_jid, phone are required")
		return
	}
	ctx := c.Request.Context()
	pool := s.deps.Mgr.SystemPool()
	imported := 0
	for _, d := range req.Devices {
		var pushName any
		if d.PushName != "" {
			pushName = d.PushName
		}
		tag, err := pool.Exec(ctx, `
INSERT INTO account_devices (tenant_id, account_jid, phone_number, push_name, ban_status)
VALUES ($1, $2, $3, $4, 'init') ON CONFLICT (account_jid) DO NOTHING`,
			d.TenantID, d.AccountJID, d.Phone, pushName)
		if err != nil {
			fail(c, http.StatusInternalServerError, "import device failed: "+err.Error())
			return
		}
		imported += int(tag.RowsAffected())
	}
	ok(c, gin.H{"submitted": len(req.Devices), "imported": imported, "skipped": len(req.Devices) - imported})
}

// ---------------------------------------------------------------------------
// Global campaigns (god view) + force-stop
// ---------------------------------------------------------------------------

type adminCampaignRow struct {
	ID        int64  `json:"id"`
	TenantID  int64  `json:"tenant_id"`
	State     string `json:"state"`
	Total     int    `json:"total"`
	Sent      int    `json:"sent"`
	Failed    int    `json:"failed"`
	CreatedAt string `json:"created_at"`
}

// handleAdminListCampaigns: GET /api/v1/admin/campaigns (all tenants).
func (s *Server) handleAdminListCampaigns(c *gin.Context) {
	rows, err := s.deps.Mgr.SystemPool().Query(c.Request.Context(), `
SELECT id, tenant_id, state::text, total, sent, failed, created_at::text
  FROM campaigns ORDER BY id DESC LIMIT 200`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list campaigns")
		return
	}
	defer rows.Close()
	out := make([]adminCampaignRow, 0)
	for rows.Next() {
		var cp adminCampaignRow
		if err := rows.Scan(&cp.ID, &cp.TenantID, &cp.State, &cp.Total, &cp.Sent, &cp.Failed, &cp.CreatedAt); err != nil {
			fail(c, http.StatusInternalServerError, "scan campaign")
			return
		}
		out = append(out, cp)
	}
	ok(c, out)
}

// handleAdminStopCampaign: POST /api/v1/admin/campaigns/:id/stop. Force-pauses a
// running campaign by flipping its state to 'paused' — the dispatcher only
// processes 'running', so it stops picking this campaign up. This is a plain
// control-plane UPDATE; no dispatch/billing engine code is touched.
func (s *Server) handleAdminStopCampaign(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad campaign id")
		return
	}
	tag, err := s.deps.Mgr.SystemPool().Exec(c.Request.Context(),
		`UPDATE campaigns SET state='paused' WHERE id=$1 AND state='running'`, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "stop campaign failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusConflict, "campaign not found or not in 'running' state")
		return
	}
	ok(c, gin.H{"id": id, "state": "paused"})
}

// ---------------------------------------------------------------------------
// User management (enable/disable + admin password reset)
//
// console_users is a platform table (no RLS); these are plain UPDATEs via the
// SystemPool. A disabled account is rejected at login by UserRepo.Authenticate
// (returns ErrInvalidCredentials), so the flag takes effect immediately.
// ---------------------------------------------------------------------------

// handleAdminSetUserDisabled: POST /api/v1/admin/users/:id/disable {disabled}.
func (s *Server) handleAdminSetUserDisabled(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad user id")
		return
	}
	var req struct {
		Disabled bool `json:"disabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "disabled (bool) is required")
		return
	}
	// Guard: an admin cannot disable their own account (lock-out protection).
	if me := sessionFrom(c); me != nil && me.UserID == id && req.Disabled {
		fail(c, http.StatusBadRequest, "不能禁用当前登录的管理员账号")
		return
	}
	tag, err := s.deps.Mgr.SystemPool().Exec(c.Request.Context(),
		`UPDATE console_users SET disabled=$1, updated_at=now() WHERE id=$2`, req.Disabled, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "update user failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	ok(c, gin.H{"id": id, "disabled": req.Disabled})
}

// handleAdminResetUserPassword: POST /api/v1/admin/users/:id/password {password}.
func (s *Server) handleAdminResetUserPassword(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad user id")
		return
	}
	var req struct {
		Password string `json:"password" binding:"required,min=8"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "password (>=8) is required")
		return
	}
	hash, err := console.HashPassword(req.Password)
	if err != nil {
		fail(c, http.StatusInternalServerError, "hash password failed")
		return
	}
	tag, err := s.deps.Mgr.SystemPool().Exec(c.Request.Context(),
		`UPDATE console_users SET password_hash=$1, updated_at=now() WHERE id=$2`, hash, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "reset password failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	ok(c, gin.H{"id": id, "password_reset": true})
}
