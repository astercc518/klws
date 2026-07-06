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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acme/wadist/internal/console"
)

// Sentinel errors for the device⇄proxy binding handlers, so the SystemPool
// transaction below can signal a clean HTTP status to the caller.
var (
	errDeviceNotFound   = errors.New("device not found")
	errProxyUnavailable = errors.New("proxy is offline or at capacity")
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
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: id, ActorID: actorID(c),
		Action: "tenant.create", ResourceType: "tenant", ResourceID: id,
		Details: map[string]any{"name": req.Name}})
	ok(c, gin.H{"id": id, "name": req.Name, "status": "active"})
}

// handleAdminUpdateTenant: PUT /api/v1/admin/tenants/:id {name}.
func (s *Server) handleAdminUpdateTenant(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid tenant id")
		return
	}
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "name is required")
		return
	}
	tag, err := s.systemPool().Exec(c.Request.Context(),
		`UPDATE tenants SET name=$1, updated_at=now() WHERE id=$2`, req.Name, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "update tenant failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "tenant not found")
		return
	}
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: id, ActorID: actorID(c),
		Action: "tenant.update", ResourceType: "tenant", ResourceID: id,
		Details: map[string]any{"name": req.Name}})
	ok(c, gin.H{"id": id, "name": req.Name})
}

// handleAdminSetTenantStatus: POST /api/v1/admin/tenants/:id/status {status}.
func (s *Server) handleAdminSetTenantStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid tenant id")
		return
	}
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Status != "active" && req.Status != "suspended") {
		fail(c, http.StatusBadRequest, "status must be 'active' or 'suspended'")
		return
	}
	tag, err := s.systemPool().Exec(c.Request.Context(),
		`UPDATE tenants SET status=$1, updated_at=now() WHERE id=$2`, req.Status, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "update status failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "tenant not found")
		return
	}
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: id, ActorID: actorID(c),
		Action: "tenant.status", ResourceType: "tenant", ResourceID: id,
		Details: map[string]any{"status": req.Status}})
	ok(c, gin.H{"id": id, "status": req.Status})
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
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "user.create", ResourceType: "user", ResourceID: id,
		Details: map[string]any{"email": req.Email, "role": req.Role, "tenant_id": req.TenantID}})
	ok(c, gin.H{"id": id, "email": req.Email, "role": req.Role, "tenant_id": req.TenantID})
}

// isUniqueViolation reports whether err is a Postgres 23505 unique-constraint error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// handleAdminUpdateUser: PUT /api/v1/admin/users/:id {email, role, tenant_id?}.
// Same role/tenant rule as create: customer needs a tenant; admin/sales none.
func (s *Server) handleAdminUpdateUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid user id")
		return
	}
	var req struct {
		Email    string `json:"email" binding:"required,email"`
		Role     string `json:"role" binding:"required"`
		TenantID *int64 `json:"tenant_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body (email, role required)")
		return
	}
	switch console.Role(req.Role) {
	case console.RoleAdmin, console.RoleSales:
		req.TenantID = nil
	case console.RoleCustomer:
		if req.TenantID == nil || *req.TenantID <= 0 {
			fail(c, http.StatusBadRequest, "customer requires a tenant_id")
			return
		}
	default:
		fail(c, http.StatusBadRequest, "role must be admin, sales, or customer")
		return
	}
	tag, err := s.systemPool().Exec(c.Request.Context(),
		`UPDATE console_users SET email=$1, role=$2, tenant_id=$3, updated_at=now() WHERE id=$4`,
		req.Email, req.Role, req.TenantID, id)
	if err != nil {
		if isUniqueViolation(err) {
			fail(c, http.StatusConflict, "email already exists")
			return
		}
		fail(c, http.StatusInternalServerError, "update user failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "user not found")
		return
	}
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "user.update", ResourceType: "user", ResourceID: id,
		Details: map[string]any{"email": req.Email, "role": req.Role, "tenant_id": req.TenantID}})
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
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: tenantID, ActorID: actorID(c),
		Action: "sales.assign", ResourceType: "tenant", ResourceID: tenantID,
		Details: map[string]any{"sales_user_id": req.SalesUserID}})
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
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: req.TenantID, ActorID: actorID(c),
		Action: "finance.topup", ResourceType: "tenant", ResourceID: req.TenantID,
		Details: map[string]any{"amount": req.Amount, "ref": req.Ref}})
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
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: req.TenantID, ActorID: actorID(c),
		Action: "finance.pricing", ResourceType: "tenant", ResourceID: req.TenantID,
		Details: map[string]any{"country": req.Country, "unit_price": req.UnitPrice}})
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
	// BoundDevices is the live count of WS accounts currently bound to this
	// proxy (account_devices.proxy_id = p.id), independent of the cached
	// current_bindings counter so the admin sees the real association fan-out.
	BoundDevices int `json:"bound_devices"`
}

type proxyStats struct {
	Total int64 `json:"total"`
	Alive int64 `json:"alive"`
	Dead  int64 `json:"dead"`
}

func parseProxyFilter(c *gin.Context) proxyFilter {
	f := proxyFilter{Q: c.Query("q"), ProxyType: c.Query("proxy_type")}
	if v := c.Query("alive"); v == "true" {
		b := true
		f.Alive = &b
	} else if v == "false" {
		b := false
		f.Alive = &b
	}
	if n, err := strconv.Atoi(c.Query("limit")); err == nil {
		f.Limit = n
	}
	if n, err := strconv.Atoi(c.Query("offset")); err == nil && n > 0 {
		f.Offset = n
	}
	return f
}

// handleAdminListProxies: GET /api/v1/admin/resources/proxies.
func (s *Server) handleAdminListProxies(c *gin.Context) {
	ctx := c.Request.Context()
	f := parseProxyFilter(c)
	where, args := buildProxyWhere(f)

	listArgs := append(append([]any{}, args...), clampPage(f.Limit, 20, 200), f.Offset)
	list := `
SELECT p.id, p.proxy_url, p.proxy_type::text, p.country_code, p.is_alive,
       p.current_bindings, p.max_bindings, p.failure_count,
       (SELECT count(*)::int FROM account_devices a WHERE a.proxy_id = p.id) AS bound_devices
  FROM proxy_pool p` + where +
		fmt.Sprintf(" ORDER BY p.id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := s.systemPool().Query(ctx, list, listArgs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list proxies")
		return
	}
	defer rows.Close()
	out := make([]proxyRow, 0)
	for rows.Next() {
		var p proxyRow
		if err := rows.Scan(&p.ID, &p.URL, &p.Type, &p.Country, &p.IsAlive,
			&p.CurrentBindings, &p.MaxBindings, &p.FailureCount, &p.BoundDevices); err != nil {
			fail(c, http.StatusInternalServerError, "scan proxy")
			return
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate proxies")
		return
	}

	var total int64
	if err := s.systemPool().QueryRow(ctx, `SELECT count(*) FROM proxy_pool p`+where, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count proxies")
		return
	}

	var st proxyStats
	if err := s.systemPool().QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE is_alive), count(*) FILTER (WHERE NOT is_alive)
  FROM proxy_pool`).Scan(&st.Total, &st.Alive, &st.Dead); err != nil {
		fail(c, http.StatusInternalServerError, "proxy stats")
		return
	}

	ok(c, gin.H{"rows": out, "total": total, "stats": st})
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
	s.recordAudit(ctx, auditEvent{ActorID: actorID(c),
		Action: "proxy.import", ResourceType: "proxy",
		Details: map[string]any{"submitted": len(req.Proxies), "imported": imported, "skipped": len(req.Proxies) - imported}})
	ok(c, gin.H{"submitted": len(req.Proxies), "imported": imported, "skipped": len(req.Proxies) - imported})
}

// handleAdminUpdateProxy: PUT /api/v1/admin/resources/proxies/:id
// {proxy_type, country_code, max_bindings}. proxy_url is immutable.
func (s *Server) handleAdminUpdateProxy(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid proxy id")
		return
	}
	var req struct {
		ProxyType   string `json:"proxy_type" binding:"required"`
		CountryCode string `json:"country_code" binding:"required"`
		MaxBindings int    `json:"max_bindings" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "proxy_type, country_code, max_bindings are required")
		return
	}
	if req.ProxyType != "socks5" && req.ProxyType != "http" && req.ProxyType != "https" {
		fail(c, http.StatusBadRequest, "proxy_type must be socks5, http or https")
		return
	}
	if len(req.CountryCode) != 2 || req.MaxBindings < 1 {
		fail(c, http.StatusBadRequest, "country_code must be 2 chars and max_bindings >= 1")
		return
	}
	// The WHERE guard rejects lowering max_bindings below current_bindings
	// (which would violate chk_bindings).
	tag, err := s.systemPool().Exec(c.Request.Context(),
		`UPDATE proxy_pool SET proxy_type=$1::proxy_type_t, country_code=$2, max_bindings=$3, updated_at=now()
		   WHERE id=$4 AND $3 >= current_bindings`,
		req.ProxyType, strings.ToUpper(req.CountryCode), req.MaxBindings, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "update proxy failed")
		return
	}
	if tag.RowsAffected() == 0 {
		// distinguish not-found from max_bindings-too-low
		var exists bool
		s.systemPool().QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM proxy_pool WHERE id=$1)`, id).Scan(&exists)
		if exists {
			fail(c, http.StatusBadRequest, "max_bindings below current bindings")
		} else {
			fail(c, http.StatusNotFound, "proxy not found")
		}
		return
	}
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "proxy.update", ResourceType: "proxy", ResourceID: id,
		Details: map[string]any{"proxy_type": req.ProxyType, "country_code": strings.ToUpper(req.CountryCode), "max_bindings": req.MaxBindings}})
	ok(c, gin.H{"id": id})
}

// handleAdminDeleteProxy: DELETE /api/v1/admin/resources/proxies/:id.
// FK ON DELETE SET NULL nulls account_devices.proxy_id automatically.
func (s *Server) handleAdminDeleteProxy(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid proxy id")
		return
	}
	tag, err := s.systemPool().Exec(c.Request.Context(), `DELETE FROM proxy_pool WHERE id=$1`, id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "delete proxy failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "proxy not found")
		return
	}
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "proxy.delete", ResourceType: "proxy", ResourceID: id})
	ok(c, gin.H{"id": id, "deleted": true})
}

type deviceRow struct {
	ID              int64    `json:"id"`
	TenantID        int64    `json:"tenant_id"`
	AccountJID      string   `json:"account_jid"`
	Phone           string   `json:"phone_number"`
	BanStatus       string   `json:"ban_status"`
	OwnerNode       *string  `json:"owner_node"`
	LastConnectedAt *string  `json:"last_connected_at"`
	Tags            []string `json:"tags"`
	ProxyID         *int64   `json:"proxy_id"`
	ProxyURL        *string  `json:"proxy_url"`
}

type deviceStats struct {
	Total     int64 `json:"total"`
	Online    int64 `json:"online"`
	Banned    int64 `json:"banned"`
	Flagged   int64 `json:"flagged"`
	LoggedOut int64 `json:"logged_out"`
}

func parseDeviceFilter(c *gin.Context) deviceFilter {
	f := deviceFilter{Q: c.Query("q"), BanStatus: c.Query("ban_status")}
	if v := c.Query("online"); v == "true" {
		b := true
		f.Online = &b
	} else if v == "false" {
		b := false
		f.Online = &b
	}
	if n, err := strconv.Atoi(c.Query("limit")); err == nil {
		f.Limit = n
	}
	if n, err := strconv.Atoi(c.Query("offset")); err == nil && n > 0 {
		f.Offset = n
	}
	return f
}

// handleAdminListDevices: GET /api/v1/admin/resources/devices (cross-tenant).
func (s *Server) handleAdminListDevices(c *gin.Context) {
	ctx := c.Request.Context()
	f := parseDeviceFilter(c)
	where, args := buildDeviceWhere(f)

	listArgs := append(append([]any{}, args...), clampPage(f.Limit, 20, 200), f.Offset)
	list := `
SELECT id, tenant_id, account_jid, phone_number, ban_status::text, owner_node, last_connected_at::text,
       tags, proxy_id, proxy_url_cache
  FROM account_devices` + where +
		fmt.Sprintf(" ORDER BY id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := s.systemPool().Query(ctx, list, listArgs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list devices")
		return
	}
	defer rows.Close()
	out := make([]deviceRow, 0)
	for rows.Next() {
		var d deviceRow
		if err := rows.Scan(&d.ID, &d.TenantID, &d.AccountJID, &d.Phone, &d.BanStatus, &d.OwnerNode, &d.LastConnectedAt,
			&d.Tags, &d.ProxyID, &d.ProxyURL); err != nil {
			fail(c, http.StatusInternalServerError, "scan device")
			return
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate devices")
		return
	}

	var total int64
	if err := s.systemPool().QueryRow(ctx, `SELECT count(*) FROM account_devices`+where, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count devices")
		return
	}

	var st deviceStats
	if err := s.systemPool().QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE owner_node IS NOT NULL),
       count(*) FILTER (WHERE ban_status = 'banned'),
       count(*) FILTER (WHERE ban_status = 'flagged'),
       count(*) FILTER (WHERE ban_status = 'logged_out')
  FROM account_devices`).Scan(&st.Total, &st.Online, &st.Banned, &st.Flagged, &st.LoggedOut); err != nil {
		fail(c, http.StatusInternalServerError, "device stats")
		return
	}

	ok(c, gin.H{"rows": out, "total": total, "stats": st})
}

// handleAdminImportDevices: POST /api/v1/admin/resources/devices
// { devices: [{tenant_id, account_jid, phone, push_name?}] }. Inserts metadata
// rows (status 'init'); actual WhatsApp QR pairing is a node-side flow in
// cmd/wadist and is not performed here.
func (s *Server) handleAdminImportDevices(c *gin.Context) {
	var req struct {
		Devices []struct {
			TenantID   int64    `json:"tenant_id" binding:"required"`
			AccountJID string   `json:"account_jid" binding:"required"`
			Phone      string   `json:"phone" binding:"required"`
			PushName   string   `json:"push_name"`
			Tags       []string `json:"tags"`
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
		tags := d.Tags
		if tags == nil {
			tags = []string{} // NOT NULL column; never send a SQL NULL
		}
		tag, err := pool.Exec(ctx, `
INSERT INTO account_devices (tenant_id, account_jid, phone_number, push_name, ban_status, tags)
VALUES ($1, $2, $3, $4, 'init', $5) ON CONFLICT (account_jid) DO NOTHING`,
			d.TenantID, d.AccountJID, d.Phone, pushName, tags)
		if err != nil {
			fail(c, http.StatusInternalServerError, "import device failed: "+err.Error())
			return
		}
		imported += int(tag.RowsAffected())
	}
	s.recordAudit(ctx, auditEvent{ActorID: actorID(c),
		Action: "device.import", ResourceType: "device",
		Details: map[string]any{"submitted": len(req.Devices), "imported": imported, "skipped": len(req.Devices) - imported}})
	ok(c, gin.H{"submitted": len(req.Devices), "imported": imported, "skipped": len(req.Devices) - imported})
}

// handleAdminBindDeviceProxy: POST /api/v1/admin/resources/devices/:id/proxy
// { proxy_id }. Binds a specific static proxy to a WS account for per-account
// network isolation ("防关联"). This mirrors the counter invariants of
// store.BindProxy (release the old proxy, claim the target only if alive and
// under capacity) but lives entirely in this admin layer on the BYPASSRLS
// SystemPool — it does NOT touch the internal/store engine package. The
// proxy_pool chk_bindings CHECK is the backstop against counter corruption.
func (s *Server) handleAdminBindDeviceProxy(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid device id")
		return
	}
	var req struct {
		ProxyID int64 `json:"proxy_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "proxy_id is required")
		return
	}
	ctx := c.Request.Context()
	var proxyURL string
	err = pgx.BeginTxFunc(ctx, s.deps.Mgr.SystemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Lock the device row and read its current proxy.
		var oldProxyID *int64
		err := tx.QueryRow(ctx, `SELECT proxy_id FROM account_devices WHERE id=$1 FOR UPDATE`, id).Scan(&oldProxyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errDeviceNotFound
		}
		if err != nil {
			return err
		}
		// No-op if it is already bound to the requested proxy; just refresh cache.
		if oldProxyID != nil && *oldProxyID == req.ProxyID {
			return tx.QueryRow(ctx, `SELECT proxy_url FROM proxy_pool WHERE id=$1`, req.ProxyID).Scan(&proxyURL)
		}
		// Release the old proxy's slot (GREATEST guards against underflow).
		if oldProxyID != nil {
			if _, err := tx.Exec(ctx,
				`UPDATE proxy_pool SET current_bindings = GREATEST(current_bindings-1, 0) WHERE id=$1`,
				*oldProxyID); err != nil {
				return err
			}
		}
		// Claim the target proxy atomically: the WHERE enforces aliveness and
		// free capacity, so a 0-row result means "unavailable".
		err = tx.QueryRow(ctx, `
UPDATE proxy_pool
   SET current_bindings = current_bindings + 1,
       usage_count      = usage_count + 1
 WHERE id = $1 AND is_alive = TRUE AND current_bindings < max_bindings
RETURNING proxy_url`, req.ProxyID).Scan(&proxyURL)
		if errors.Is(err, pgx.ErrNoRows) {
			return errProxyUnavailable
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE account_devices SET proxy_id=$1, proxy_url_cache=$2 WHERE id=$3`,
			req.ProxyID, proxyURL, id)
		return err
	})
	switch {
	case errors.Is(err, errDeviceNotFound):
		fail(c, http.StatusNotFound, "device not found")
		return
	case errors.Is(err, errProxyUnavailable):
		fail(c, http.StatusConflict, "proxy is offline or already at capacity")
		return
	case err != nil:
		fail(c, http.StatusInternalServerError, "bind proxy failed: "+err.Error())
		return
	}
	s.recordAudit(ctx, auditEvent{ActorID: actorID(c),
		Action: "device.bind_proxy", ResourceType: "device", ResourceID: id,
		Details: map[string]any{"proxy_id": req.ProxyID}})
	ok(c, gin.H{"device_id": id, "proxy_id": req.ProxyID, "proxy_url": proxyURL})
}

// handleAdminUnbindDeviceProxy: DELETE /api/v1/admin/resources/devices/:id/proxy.
// Releases the account's proxy binding and frees the proxy's slot. Idempotent.
func (s *Server) handleAdminUnbindDeviceProxy(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid device id")
		return
	}
	ctx := c.Request.Context()
	err = pgx.BeginTxFunc(ctx, s.deps.Mgr.SystemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
		var oldProxyID *int64
		err := tx.QueryRow(ctx, `SELECT proxy_id FROM account_devices WHERE id=$1 FOR UPDATE`, id).Scan(&oldProxyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errDeviceNotFound
		}
		if err != nil {
			return err
		}
		if oldProxyID == nil {
			return nil // already unbound
		}
		if _, err := tx.Exec(ctx,
			`UPDATE proxy_pool SET current_bindings = GREATEST(current_bindings-1, 0) WHERE id=$1`,
			*oldProxyID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE account_devices SET proxy_id=NULL, proxy_url_cache=NULL WHERE id=$1`, id)
		return err
	})
	switch {
	case errors.Is(err, errDeviceNotFound):
		fail(c, http.StatusNotFound, "device not found")
		return
	case err != nil:
		fail(c, http.StatusInternalServerError, "unbind proxy failed: "+err.Error())
		return
	}
	s.recordAudit(ctx, auditEvent{ActorID: actorID(c),
		Action: "device.unbind_proxy", ResourceType: "device", ResourceID: id})
	ok(c, gin.H{"device_id": id, "proxy_id": nil})
}

// handleAdminUpdateDevice: PUT /api/v1/admin/resources/devices/:id
// {tenant_id?, phone_number?, tags?}. account_jid is immutable. Only provided
// fields update.
func (s *Server) handleAdminUpdateDevice(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid device id")
		return
	}
	var req struct {
		TenantID *int64    `json:"tenant_id"`
		Phone    *string   `json:"phone_number"`
		Tags     *[]string `json:"tags"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := c.Request.Context()
	if req.TenantID != nil {
		var exists bool
		s.systemPool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1)`, *req.TenantID).Scan(&exists)
		if !exists {
			fail(c, http.StatusBadRequest, "tenant_id does not exist")
			return
		}
	}
	var sets []string
	var args []any
	details := map[string]any{}
	if req.TenantID != nil {
		args = append(args, *req.TenantID)
		sets = append(sets, fmt.Sprintf("tenant_id=$%d", len(args)))
		details["tenant_id"] = *req.TenantID
	}
	if req.Phone != nil {
		args = append(args, *req.Phone)
		sets = append(sets, fmt.Sprintf("phone_number=$%d", len(args)))
		details["phone_number"] = *req.Phone
	}
	if req.Tags != nil {
		args = append(args, *req.Tags)
		sets = append(sets, fmt.Sprintf("tags=$%d", len(args)))
		details["tags"] = *req.Tags
	}
	if len(sets) == 0 {
		fail(c, http.StatusBadRequest, "no fields to update")
		return
	}
	args = append(args, id)
	q := "UPDATE account_devices SET " + strings.Join(sets, ", ") + ", updated_at=now() WHERE id=$" + strconv.Itoa(len(args))
	tag, err := s.systemPool().Exec(ctx, q, args...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "update device failed")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(c, http.StatusNotFound, "device not found")
		return
	}
	s.recordAudit(ctx, auditEvent{ActorID: actorID(c),
		Action: "device.update", ResourceType: "device", ResourceID: id, Details: details})
	ok(c, gin.H{"id": id})
}

// handleAdminDeleteDevice: DELETE /api/v1/admin/resources/devices/:id. In one tx:
// release the bound proxy's slot (if any), delete the row, audit.
func (s *Server) handleAdminDeleteDevice(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid device id")
		return
	}
	ctx := c.Request.Context()
	err = pgx.BeginTxFunc(ctx, s.systemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
		var proxyID *int64
		e := tx.QueryRow(ctx, `SELECT proxy_id FROM account_devices WHERE id=$1 FOR UPDATE`, id).Scan(&proxyID)
		if errors.Is(e, pgx.ErrNoRows) {
			return errDeviceNotFound
		}
		if e != nil {
			return e
		}
		if proxyID != nil {
			if _, e := tx.Exec(ctx, `UPDATE proxy_pool SET current_bindings=GREATEST(current_bindings-1,0) WHERE id=$1`, *proxyID); e != nil {
				return e
			}
		}
		if _, e := tx.Exec(ctx, `DELETE FROM account_devices WHERE id=$1`, id); e != nil {
			return e
		}
		return s.recordAuditTx(ctx, tx, auditEvent{ActorID: actorID(c),
			Action: "device.delete", ResourceType: "device", ResourceID: id})
	})
	switch {
	case errors.Is(err, errDeviceNotFound):
		fail(c, http.StatusNotFound, "device not found")
		return
	case err != nil:
		fail(c, http.StatusInternalServerError, "delete device failed")
		return
	}
	ok(c, gin.H{"id": id, "deleted": true})
}

// ---------------------------------------------------------------------------
// Risk / anti-ban policy (control-plane). Persists the admin's intended global
// strategy to system_risk_config via the BYPASSRLS SystemPool. This is a
// control-plane store only: it does NOT alter the dispatch/sendgate engine,
// which keeps its compiled defaults until a future integration reads this row.
// No red-line package (dispatch/sendgate/store) is touched here.
// ---------------------------------------------------------------------------

type riskConfig struct {
	MinDelaySeconds       int     `json:"min_delay_seconds"`
	MaxDelaySeconds       int     `json:"max_delay_seconds"`
	DailyLimitPerDevice   int     `json:"daily_limit_per_device"`
	BanRateCircuitBreaker float64 `json:"ban_rate_circuit_breaker"`
	// Circuit-breaker control knobs (consumed by the future internal/riskbreaker
	// supervisor; see docs/RISK-CIRCUIT-BREAKER-DESIGN-zh.md). Persisted here so
	// they are hot-reloadable without an engine restart.
	CircuitBreakerEnabled bool `json:"circuit_breaker_enabled"`
	CircuitBreakerDryRun  bool `json:"circuit_breaker_dry_run"`
	MinSample             int  `json:"min_sample"`
	WindowSeconds         int  `json:"window_seconds"`
	EvalIntervalSeconds   int  `json:"eval_interval_seconds"`

	UpdatedAt *string `json:"updated_at"`
	UpdatedBy *int64  `json:"updated_by"`
}

// riskConfigDefaults mirrors the engine's current compiled behavior, used both
// to seed and as a defensive fallback if the singleton row is somehow absent.
// Breaker ships disabled + dry-run (safe by default).
func riskConfigDefaults() riskConfig {
	return riskConfig{
		MinDelaySeconds: 3, MaxDelaySeconds: 8, DailyLimitPerDevice: 1000, BanRateCircuitBreaker: 0.15,
		CircuitBreakerEnabled: false, CircuitBreakerDryRun: true,
		MinSample: 20, WindowSeconds: 900, EvalIntervalSeconds: 20,
	}
}

// handleAdminGetRiskConfig: GET /api/v1/admin/settings/risk.
func (s *Server) handleAdminGetRiskConfig(c *gin.Context) {
	var rc riskConfig
	err := s.deps.Mgr.SystemPool().QueryRow(c.Request.Context(), `
SELECT min_delay_seconds, max_delay_seconds, daily_limit_per_device, ban_rate_circuit_breaker,
       circuit_breaker_enabled, circuit_breaker_dry_run, min_sample, window_seconds, eval_interval_seconds,
       updated_at::text, updated_by
  FROM system_risk_config WHERE id = 1`).
		Scan(&rc.MinDelaySeconds, &rc.MaxDelaySeconds, &rc.DailyLimitPerDevice, &rc.BanRateCircuitBreaker,
			&rc.CircuitBreakerEnabled, &rc.CircuitBreakerDryRun, &rc.MinSample, &rc.WindowSeconds, &rc.EvalIntervalSeconds,
			&rc.UpdatedAt, &rc.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		ok(c, riskConfigDefaults()) // migration seeds id=1; be defensive anyway
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "load risk config")
		return
	}
	ok(c, rc)
}

// handleAdminUpdateRiskConfig: PUT /api/v1/admin/settings/risk. Pointers +
// "required" ensure every field is present (so a legitimate 0 is distinguished
// from a missing field); ranges are validated below, with the table CHECKs as
// a backstop.
func (s *Server) handleAdminUpdateRiskConfig(c *gin.Context) {
	var req struct {
		MinDelaySeconds       *int     `json:"min_delay_seconds" binding:"required"`
		MaxDelaySeconds       *int     `json:"max_delay_seconds" binding:"required"`
		DailyLimitPerDevice   *int     `json:"daily_limit_per_device" binding:"required"`
		BanRateCircuitBreaker *float64 `json:"ban_rate_circuit_breaker" binding:"required"`
		// Booleans intentionally use plain types (not pointers + required): a
		// false value is meaningful and must not be rejected as "missing".
		CircuitBreakerEnabled bool `json:"circuit_breaker_enabled"`
		CircuitBreakerDryRun  bool `json:"circuit_breaker_dry_run"`
		MinSample             *int `json:"min_sample" binding:"required"`
		WindowSeconds         *int `json:"window_seconds" binding:"required"`
		EvalIntervalSeconds   *int `json:"eval_interval_seconds" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "min_delay_seconds, max_delay_seconds, daily_limit_per_device, ban_rate_circuit_breaker, min_sample, window_seconds, eval_interval_seconds are required")
		return
	}
	switch {
	case *req.MinDelaySeconds < 0:
		fail(c, http.StatusBadRequest, "min_delay_seconds must be >= 0")
		return
	case *req.MaxDelaySeconds < *req.MinDelaySeconds:
		fail(c, http.StatusBadRequest, "max_delay_seconds must be >= min_delay_seconds")
		return
	case *req.DailyLimitPerDevice <= 0:
		fail(c, http.StatusBadRequest, "daily_limit_per_device must be > 0")
		return
	case *req.BanRateCircuitBreaker < 0 || *req.BanRateCircuitBreaker > 1:
		fail(c, http.StatusBadRequest, "ban_rate_circuit_breaker must be between 0 and 1")
		return
	case *req.MinSample < 1:
		fail(c, http.StatusBadRequest, "min_sample must be >= 1")
		return
	case *req.WindowSeconds < 1:
		fail(c, http.StatusBadRequest, "window_seconds must be >= 1")
		return
	case *req.EvalIntervalSeconds < 1:
		fail(c, http.StatusBadRequest, "eval_interval_seconds must be >= 1")
		return
	}
	var updatedBy *int64
	if sd := sessionFrom(c); sd != nil {
		updatedBy = &sd.UserID
	}
	_, err := s.deps.Mgr.SystemPool().Exec(c.Request.Context(), `
INSERT INTO system_risk_config
       (id, min_delay_seconds, max_delay_seconds, daily_limit_per_device, ban_rate_circuit_breaker,
        circuit_breaker_enabled, circuit_breaker_dry_run, min_sample, window_seconds, eval_interval_seconds,
        updated_at, updated_by)
VALUES (1,  $1, $2, $3, $4,  $5, $6, $7, $8, $9,  now(), $10)
ON CONFLICT (id) DO UPDATE SET
       min_delay_seconds        = EXCLUDED.min_delay_seconds,
       max_delay_seconds        = EXCLUDED.max_delay_seconds,
       daily_limit_per_device   = EXCLUDED.daily_limit_per_device,
       ban_rate_circuit_breaker = EXCLUDED.ban_rate_circuit_breaker,
       circuit_breaker_enabled  = EXCLUDED.circuit_breaker_enabled,
       circuit_breaker_dry_run  = EXCLUDED.circuit_breaker_dry_run,
       min_sample               = EXCLUDED.min_sample,
       window_seconds           = EXCLUDED.window_seconds,
       eval_interval_seconds    = EXCLUDED.eval_interval_seconds,
       updated_at               = now(),
       updated_by               = EXCLUDED.updated_by`,
		*req.MinDelaySeconds, *req.MaxDelaySeconds, *req.DailyLimitPerDevice, *req.BanRateCircuitBreaker,
		req.CircuitBreakerEnabled, req.CircuitBreakerDryRun, *req.MinSample, *req.WindowSeconds, *req.EvalIntervalSeconds,
		updatedBy)
	if err != nil {
		fail(c, http.StatusInternalServerError, "save risk config failed: "+err.Error())
		return
	}
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "risk.update", ResourceType: "system_risk_config", ResourceID: 1,
		Details: map[string]any{
			"min_delay_seconds": *req.MinDelaySeconds, "max_delay_seconds": *req.MaxDelaySeconds,
			"daily_limit_per_device": *req.DailyLimitPerDevice, "ban_rate_circuit_breaker": *req.BanRateCircuitBreaker,
			"circuit_breaker_enabled": req.CircuitBreakerEnabled, "circuit_breaker_dry_run": req.CircuitBreakerDryRun,
			"min_sample": *req.MinSample, "window_seconds": *req.WindowSeconds, "eval_interval_seconds": *req.EvalIntervalSeconds}})
	ok(c, riskConfig{
		MinDelaySeconds:       *req.MinDelaySeconds,
		MaxDelaySeconds:       *req.MaxDelaySeconds,
		DailyLimitPerDevice:   *req.DailyLimitPerDevice,
		BanRateCircuitBreaker: *req.BanRateCircuitBreaker,
		CircuitBreakerEnabled: req.CircuitBreakerEnabled,
		CircuitBreakerDryRun:  req.CircuitBreakerDryRun,
		MinSample:             *req.MinSample,
		WindowSeconds:         *req.WindowSeconds,
		EvalIntervalSeconds:   *req.EvalIntervalSeconds,
		UpdatedBy:             updatedBy,
	})
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
	// AutoTripped is true when the campaign is currently paused because the
	// risk circuit breaker tripped it (a circuit_break audit newer than any
	// later resume), as opposed to a manual operator stop.
	AutoTripped bool `json:"auto_tripped"`
}

// handleAdminListCampaigns: GET /api/v1/admin/campaigns (all tenants).
func (s *Server) handleAdminListCampaigns(c *gin.Context) {
	rows, err := s.deps.Mgr.SystemPool().Query(c.Request.Context(), `
SELECT c.id, c.tenant_id, c.state::text, c.total, c.sent, c.failed, c.created_at::text,
       (c.state = 'paused' AND
        COALESCE((SELECT max(occurred_at) FROM audit_log a
                   WHERE a.action='campaign.circuit_break' AND a.resource_id=c.id), 'epoch') >
        COALESCE((SELECT max(occurred_at) FROM audit_log a
                   WHERE a.action='campaign.resume' AND a.resource_id=c.id), 'epoch')
       ) AS auto_tripped
  FROM campaigns c ORDER BY c.id DESC LIMIT 200`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list campaigns")
		return
	}
	defer rows.Close()
	out := make([]adminCampaignRow, 0)
	for rows.Next() {
		var cp adminCampaignRow
		if err := rows.Scan(&cp.ID, &cp.TenantID, &cp.State, &cp.Total, &cp.Sent, &cp.Failed, &cp.CreatedAt, &cp.AutoTripped); err != nil {
			fail(c, http.StatusInternalServerError, "scan campaign")
			return
		}
		out = append(out, cp)
	}
	ok(c, out)
}

// handleAdminStopCampaign: POST /api/v1/admin/campaigns/:id/stop. Force-pauses a
// running campaign by flipping its state to 'paused' — the dispatcher only
// processes 'running', so it stops picking this campaign up. The state flip and
// its audit row are written atomically in one tx; a no-op stop (already
// paused/not found) audits nothing. No dispatch/billing engine code is touched.
func (s *Server) handleAdminStopCampaign(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad campaign id")
		return
	}
	ctx := c.Request.Context()
	paused := false
	err = pgx.BeginTxFunc(ctx, s.systemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
		var tenantID int64
		e := tx.QueryRow(ctx,
			`UPDATE campaigns SET state='paused' WHERE id=$1 AND state='running' RETURNING tenant_id`, id).
			Scan(&tenantID)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil // not running / not found; paused stays false
		}
		if e != nil {
			return e
		}
		paused = true
		return s.recordAuditTx(ctx, tx, auditEvent{TenantID: tenantID, ActorID: actorID(c),
			Action: "campaign.stop", ResourceType: "campaign", ResourceID: id})
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "stop campaign failed")
		return
	}
	if !paused {
		fail(c, http.StatusConflict, "campaign not found or not in 'running' state")
		return
	}
	ok(c, gin.H{"id": id, "state": "paused"})
}

// handleAdminResumeCampaign: POST /api/v1/admin/campaigns/:id/resume. Reverses a
// pause (manual stop OR an automatic circuit-breaker trip) by flipping 'paused'
// back to 'running'. Operators use this after investigating a tripped campaign;
// there is intentionally no auto-resume (the ban root-cause may be unresolved).
// Plain control-plane UPDATE; no dispatch engine code is touched.
func (s *Server) handleAdminResumeCampaign(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad campaign id")
		return
	}
	ctx := c.Request.Context()
	resumed := false
	err = pgx.BeginTxFunc(ctx, s.systemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
		var tenantID int64
		err := tx.QueryRow(ctx,
			`UPDATE campaigns SET state='running' WHERE id=$1 AND state='paused' RETURNING tenant_id`, id).
			Scan(&tenantID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // not paused / not found; resumed stays false
		}
		if err != nil {
			return err
		}
		resumed = true
		// Audit the resume so AutoTripped (latest circuit_break vs latest resume)
		// reads correctly afterwards, and operators have a trail.
		return s.recordAuditTx(ctx, tx, auditEvent{TenantID: tenantID, ActorID: actorID(c),
			Action: "campaign.resume", ResourceType: "campaign", ResourceID: id})
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "resume campaign failed")
		return
	}
	if !resumed {
		fail(c, http.StatusConflict, "campaign not found or not in 'paused' state")
		return
	}
	ok(c, gin.H{"id": id, "state": "running"})
}

// handleAdminListCampaignRecipients: GET /api/v1/admin/campaigns/:id/recipients
// (cross-tenant). Reuses loadRecipients (campaign.go) over the BYPASSRLS
// SystemPool so super-admins can drill into any tenant's campaign.
func (s *Server) handleAdminListCampaignRecipients(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "bad campaign id")
		return
	}
	page, pageSize := parsePageParams(c)
	resp, found, err := loadRecipients(c.Request.Context(), s.deps.Mgr.SystemPool(), id, page, pageSize)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list recipients failed")
		return
	}
	if !found {
		fail(c, http.StatusNotFound, "campaign not found")
		return
	}
	ok(c, resp)
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
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "user.disable", ResourceType: "user", ResourceID: id,
		Details: map[string]any{"disabled": req.Disabled}})
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
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "user.password_reset", ResourceType: "user", ResourceID: id})
	ok(c, gin.H{"id": id, "password_reset": true})
}

// ---------------------------------------------------------------------------
// Audit log (read-only)

type auditRow struct {
	ID           int64           `json:"id"`
	OccurredAt   string          `json:"occurred_at"`
	TenantID     *int64          `json:"tenant_id"`
	TenantName   *string         `json:"tenant_name"`
	ActorID      *int64          `json:"actor_id"`
	ActorEmail   *string         `json:"actor_email"`
	Action       string          `json:"action"`
	ResourceType *string         `json:"resource_type"`
	ResourceID   *int64          `json:"resource_id"`
	Details      json.RawMessage `json:"details"`
}

// parseAuditFilter reads the optional query params for the audit list.
func parseAuditFilter(c *gin.Context) (auditFilter, error) {
	f := auditFilter{Action: c.Query("action")}
	if v := c.Query("actor_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return f, fmt.Errorf("bad actor_id")
		}
		f.ActorID = n
	}
	if v := c.Query("tenant_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return f, fmt.Errorf("bad tenant_id")
		}
		f.TenantID = n
	}
	if v := c.Query("since"); v != "" {
		ts, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, fmt.Errorf("bad since (want RFC3339)")
		}
		f.Since = ts
	}
	if v := c.Query("until"); v != "" {
		ts, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, fmt.Errorf("bad until (want RFC3339)")
		}
		f.Until = ts
	}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Offset = n
		}
	}
	return f, nil
}

// handleAdminListAudit: GET /api/v1/admin/audit — filtered, paginated audit log
// with actor-email and tenant-name joins.
func (s *Server) handleAdminListAudit(c *gin.Context) {
	f, err := parseAuditFilter(c)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	ctx := c.Request.Context()
	listSQL, listArgs := buildAuditListSQL(f)
	rows, err := s.systemPool().Query(ctx, listSQL, listArgs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "audit query")
		return
	}
	defer rows.Close()
	out := make([]auditRow, 0)
	for rows.Next() {
		var r auditRow
		var details []byte
		if err := rows.Scan(&r.ID, &r.OccurredAt, &r.TenantID, &r.TenantName,
			&r.ActorID, &r.ActorEmail, &r.Action, &r.ResourceType, &r.ResourceID, &details); err != nil {
			fail(c, http.StatusInternalServerError, "scan audit")
			return
		}
		r.Details = details
		out = append(out, r)
	}
	countSQL, countArgs := buildAuditCountSQL(f)
	var total int64
	if err := s.systemPool().QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "audit count")
		return
	}
	ok(c, gin.H{"rows": out, "total": total})
}
