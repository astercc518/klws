// Package api is the front-end-facing JSON API for the wadist management
// platform. It REPLACES the html/template + htmx view layer in
// internal/console, but deliberately REUSES that package's security primitives
// (SessionStore, UserRepo, TenantRepo, cookie HMAC) and the store.Manager's
// RLS machinery (WithTenant / SystemPool) verbatim — no auth or tenant-isolation
// logic is reinvented here, and no red-line package (billing/dispatch/store/
// sendgate/cluster) is modified.
package api

import (
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/audit"
	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/cluster"
	"github.com/acme/wadist/internal/console"
	"github.com/acme/wadist/internal/pricing"
	"github.com/acme/wadist/internal/receipt"
	"github.com/acme/wadist/internal/store"
)

// Deps bundles the shared, red-line-safe dependencies the API handlers need.
// main.go constructs these (the SAME instances the console server uses) and
// injects them — the API owns no database or session logic of its own.
type Deps struct {
	Mgr      *store.Manager        // RLS via WithTenant(...); admin/sales via SystemPool()
	Billing  *billing.Repo         // frozen-funds wallet (read-only use here)
	Pricing  *pricing.Repo         // tenant×country unit price
	Users    *console.UserRepo     // login / Authenticate (BYPASSRLS system pool)
	Tenants  *console.TenantRepo   // tenant registry (admin/sales)
	Sessions *console.SessionStore // Redis-backed sessions (shared with console)
	Audit    *audit.AuditWriter    // append-only audit_log writer (shared with billing)
	Receipt  *receipt.Recorder     // delivery/read receipt writer (Evolution webhook)

	// ProtectedAdmins holds console_users.email identifiers (the login field,
	// email or username) of super/bootstrap admins that must be hidden from
	// GET /admin/users and rejected (403) by the user write endpoints. Empty =
	// nothing protected. Configured via WADIST_PROTECTED_ADMINS.
	ProtectedAdmins map[string]bool

	SessionKey []byte // HMAC key for signing/verifying the session token
	BlindKey   []byte // HMAC blind-index key (campaign recipient dedup/suppression)
	CORSOrigin string // allowed browser origin (default http://localhost:3000)

	EvolutionWebhookSecret string // Authorization token Evolution echoes on webhook callbacks (WADIST_EVOLUTION_WEBHOOK_SECRET); empty = dev, accept unauthenticated

	// EvoCluster is the registry of per-node Evolution HTTP clients (same
	// construction as cmd/wadist/main.go's worker-side wiring). Reached via
	// EvoCluster.For(node) by POST /admin/instances (T3) and the admin
	// instance list. EvolutionCapPerNode rides along for the same reason.
	EvoCluster    *cluster.EvoCluster
	EvoCapPerNode int

	// EvolutionWebhookURL is the callback URL handed to Evolution on
	// CreateInstance (WADIST_EVOLUTION_WEBHOOK_URL) — same config.Config field
	// cmd/wadist/main.go's buildEvoSession uses. Wired by T3 (POST
	// /admin/instances); empty is valid in dev (Evolution just gets no webhook).
	EvolutionWebhookURL string
}

// Server holds the injected deps plus a readiness flag (mirrors console.Server
// so the same drain-on-SIGTERM lifecycle applies).
type Server struct {
	deps  Deps
	ready atomic.Bool
	srv   *http.Server
	ln    net.Listener

	// sysPool, when non-nil, overrides deps.Mgr.SystemPool() for the audit READ
	// endpoint — set ONLY by tests so it can run against a raw pool without a
	// full store.Manager.
	sysPool *pgxpool.Pool

	// evoNodesFn / evoForFn, when non-nil, override evoNodes()/evoClientFor()
	// (internal/api/instances_api.go) — set ONLY by tests so
	// handleAdminCreateInstance can be exercised against a fake instanceEvoAPI
	// instead of a real EvoCluster/HTTP round-trip. Mirrors the sysPool pattern.
	evoNodesFn func() []string
	evoForFn   func(node string) (instanceEvoAPI, bool)
}

// NewServer wires the dependencies. Call Router() to get the gin.Engine.
func NewServer(d Deps) *Server {
	if d.CORSOrigin == "" {
		d.CORSOrigin = "http://localhost:3000"
	}
	s := &Server{deps: d}
	s.ready.Store(true)
	return s
}

// SetReady toggles the /readyz drain signal (used during graceful shutdown).
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// Router builds the full route tree. All business routes live under /api/v1.
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), s.cors())

	// --- k8s probes (no auth) ---
	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.GET("/readyz", func(c *gin.Context) {
		if s.ready.Load() {
			c.String(http.StatusOK, "ready")
			return
		}
		c.String(http.StatusServiceUnavailable, "draining")
	})

	v1 := r.Group("/api/v1")

	// Evolution 数据面回调（HMAC 鉴权，非用户鉴权）。E0 log-only。
	// health sink (sendgate) deferred: E6-followup wires console sendgate
	NewEvolutionWebhook(s.deps.EvolutionWebhookSecret, s.deps.Receipt, s.deps.Mgr, nil).Register(v1)

	// --- Auth controller ---
	// login is public; logout/me require a valid token.
	auth := v1.Group("/auth")
	{
		auth.POST("/login", s.handleLogin)
		auth.POST("/register", s.handleRegister)
		auth.POST("/password/forgot", s.handleForgotPassword)
		auth.POST("/password/reset", s.handleResetPassword)
		auth.POST("/logout", s.requireAuth(), s.handleLogout)
		auth.GET("/me", s.requireAuth(), s.handleMe)
	}

	// --- Tenant controller (customer self-service: wallet + own info) ---
	// requireRole(customer) guarantees a session.TenantID is present for RLS.
	tenant := v1.Group("/tenant", s.requireAuth(), s.requireRole(console.RoleCustomer))
	{
		tenant.GET("/wallet", s.handleWallet)
		tenant.GET("/info", s.handleTenantInfo)
		tenant.GET("/stats", s.handleStats)
	}

	// --- Campaign controller (customer: submit bulk send + list history) ---
	campaigns := v1.Group("/campaigns", s.requireAuth(), s.requireRole(console.RoleCustomer))
	{
		campaigns.POST("", s.handleCreateCampaign)
		campaigns.GET("", s.handleListCampaigns)
		campaigns.GET("/:id/recipients", s.handleListCampaignRecipients)
	}

	// --- Contact controller (customer: contact library CRUD + import/export) ---
	contacts := v1.Group("/contacts", s.requireAuth(), s.requireRole(console.RoleCustomer))
	{
		contacts.GET("", s.handleListContacts)
		contacts.POST("", s.handleCreateContact)
		contacts.PUT("/:id", s.handleUpdateContact)
		contacts.DELETE("/:id", s.handleDeleteContact)
		contacts.POST("/import", s.handleImportContacts)
		contacts.GET("/export", s.handleExportContacts)

		contacts.GET("/tags", s.handleListTags)
		contacts.POST("/tags", s.handleCreateTag)
		contacts.DELETE("/tags/:id", s.handleDeleteTag)
		contacts.POST("/tags/:id/apply", s.handleApplyTag)

		contacts.GET("/segments", s.handleListSegments)
		contacts.POST("/segments", s.handleCreateSegment)
		contacts.DELETE("/segments/:id", s.handleDeleteSegment)
		contacts.GET("/segments/:id/preview", s.handleSegmentPreview)
	}

	// --- Suppression controller (customer: manual blacklist/opt-out mgmt) ---
	// Append-only by compliance design: migration 0008 REVOKEs UPDATE/DELETE on
	// suppression_list from both app roles ("tenants must not be able to
	// un-suppress opt-outs"), so there is deliberately NO un-suppress endpoint.
	suppression := v1.Group("/suppression", s.requireAuth(), s.requireRole(console.RoleCustomer))
	{
		suppression.GET("", s.handleListSuppression)
		suppression.POST("", s.handleAddSuppression)
		suppression.POST("/import", s.handleImportSuppression)
	}

	// --- Sales controller (a sales user's own customers) ---
	sales := v1.Group("/sales", s.requireAuth(), s.requireRole(console.RoleSales))
	{
		sales.GET("/customers", s.handleSalesCustomers)
		sales.GET("/customers/:id", s.handleSalesCustomerDetail)
		sales.POST("/customers/:id/pricing", s.handleSalesSetPricing)
		sales.POST("/sub-agents", s.handleAgentCreateSubAgent)
		sales.POST("/allocate", s.handleAgentAllocate)
		sales.GET("/statement", s.handleAgentStatement)
		sales.GET("/overview", s.handleAgentOverview)
	}

	// --- Admin controller (super-admin "god view") ---
	admin := v1.Group("/admin", s.requireAuth(), s.requireRole(console.RoleAdmin))
	{
		admin.GET("/stats", s.handleAdminStats)

		admin.GET("/tenants", s.handleAdminListTenants)
		admin.POST("/tenants", s.handleAdminCreateTenant)
		admin.PUT("/tenants/:id", s.handleAdminUpdateTenant)
		admin.POST("/tenants/:id/status", s.handleAdminSetTenantStatus)
		admin.GET("/users", s.handleAdminListUsers)
		admin.POST("/users", s.handleAdminCreateUser)
		admin.PUT("/users/:id", s.handleAdminUpdateUser)
		admin.POST("/users/:id/disable", s.handleAdminSetUserDisabled)
		admin.POST("/users/:id/password", s.handleAdminResetUserPassword)
		admin.POST("/users/:id/impersonate", s.handleAdminImpersonate)
		admin.POST("/sales/:id/assign", s.handleAdminAssignSales)
		admin.GET("/agents", s.handleAdminListAgents)
		admin.POST("/agents/:id/parent", s.handleAdminSetAgentParent)
		admin.POST("/agents/:id/terms", s.handleAdminSetAgentTerms)

		admin.POST("/finance/topup", s.handleAdminTopup)
		admin.POST("/finance/pricing", s.handleAdminSetPricing)
		admin.GET("/finance/ledger", s.handleAdminLedger)
		admin.GET("/finance/ledger/export", s.handleAdminLedgerExport)
		admin.GET("/finance/stats", s.handleAdminFinanceStats)
		admin.GET("/finance/bill", s.handleAdminFinanceBill)
		admin.GET("/finance/bill/export", s.handleAdminFinanceBillExport)
		admin.GET("/commissions", s.handleAdminCommissions)

		admin.GET("/resources/proxies", s.handleAdminListProxies)
		admin.POST("/resources/proxies", s.handleAdminImportProxies)
		admin.PUT("/resources/proxies/:id", s.handleAdminUpdateProxy)
		admin.DELETE("/resources/proxies/:id", s.handleAdminDeleteProxy)
		admin.GET("/resources/devices", s.handleAdminListDevices)
		admin.POST("/resources/devices", s.handleAdminImportDevices)
		admin.POST("/resources/devices/:id/proxy", s.handleAdminBindDeviceProxy)
		admin.DELETE("/resources/devices/:id/proxy", s.handleAdminUnbindDeviceProxy)
		admin.PUT("/resources/devices/:id", s.handleAdminUpdateDevice)
		admin.DELETE("/resources/devices/:id", s.handleAdminDeleteDevice)

		admin.GET("/instances", s.handleAdminListInstances)
		admin.POST("/instances", s.handleAdminCreateInstance)
		admin.GET("/instances/:name/qr", s.handleAdminInstanceQR)
		admin.GET("/instances/:name/state", s.handleAdminInstanceState)
		admin.POST("/instances/:name/reconnect", s.handleAdminInstanceReconnect)
		admin.POST("/instances/:name/logout", s.handleAdminInstanceLogout)
		admin.DELETE("/instances/:name", s.handleAdminInstanceDelete)
		admin.GET("/nodes", s.handleAdminListNodes)

		admin.GET("/settings/risk", s.handleAdminGetRiskConfig)
		admin.PUT("/settings/risk", s.handleAdminUpdateRiskConfig)

		admin.GET("/risk/overview", s.handleAdminRiskOverview)
		admin.GET("/risk/accounts", s.handleAdminRiskAccounts)

		admin.GET("/reports/trend", s.handleAdminReportsTrend)
		admin.GET("/reports/tenant-consumption", s.handleAdminReportsTenantConsumption)

		admin.GET("/campaigns", s.handleAdminListCampaigns)
		admin.GET("/campaigns/:id/recipients", s.handleAdminListCampaignRecipients)
		admin.GET("/recipients", s.handleAdminListRecipients)
		admin.POST("/campaigns/:id/stop", s.handleAdminStopCampaign)
		admin.POST("/campaigns/:id/resume", s.handleAdminResumeCampaign)

		admin.GET("/contacts", s.handleAdminListContacts)

		admin.GET("/audit", s.handleAdminListAudit)

		admin.GET("/agent/cost-pricing", s.handleAdminListCostPricing)
		admin.POST("/agent/cost-pricing", s.handleAdminSetCostPricing)
		admin.GET("/agent/settlements", s.handleAdminSettlementOverview)
		admin.POST("/agent/settlements/close", s.handleAdminCloseSettlement)
	}

	return r
}

// ---------------------------------------------------------------------------
// Standard JSON envelope: { "code": <int>, "data": <any>, "message": <string> }
// ---------------------------------------------------------------------------

func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "data": data, "message": "success"})
}

// fail writes the envelope with an error message. httpStatus is the real HTTP
// status; the envelope "code" mirrors it so the frontend can branch on one field.
func fail(c *gin.Context, httpStatus int, message string) {
	c.AbortWithStatusJSON(httpStatus, gin.H{"code": httpStatus, "data": nil, "message": message})
}

// ---------------------------------------------------------------------------
// Auth middleware — reuses console's session primitives, NOT reimplemented.
// ---------------------------------------------------------------------------

const sessionCtxKey = "wadist_session"

// requireAuth extracts a "Bearer <sid.hmac>" token, verifies the HMAC with the
// same key/algorithm as the console cookie, looks the session up in Redis, and
// stashes the SessionData in the gin context. 401 on any failure.
func (s *Server) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if token == "" {
			fail(c, http.StatusUnauthorized, "missing bearer token")
			return
		}
		sid, ok := console.VerifyCookie(s.deps.SessionKey, token)
		if !ok {
			fail(c, http.StatusUnauthorized, "invalid token signature")
			return
		}
		data, err := s.deps.Sessions.Get(c.Request.Context(), sid)
		if err != nil {
			fail(c, http.StatusUnauthorized, "session expired")
			return
		}
		c.Set(sessionCtxKey, data)
		c.Next()
	}
}

// requireRole 403s unless the session role is in the allowed set. Must be
// chained AFTER requireAuth.
func (s *Server) requireRole(roles ...console.Role) gin.HandlerFunc {
	allowed := make(map[console.Role]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(c *gin.Context) {
		data := sessionFrom(c)
		if data == nil || !allowed[data.Role] {
			fail(c, http.StatusForbidden, "forbidden")
			return
		}
		c.Next()
	}
}

// sessionFrom returns the authenticated session, or nil if unauthenticated.
func sessionFrom(c *gin.Context) *console.SessionData {
	v, ok := c.Get(sessionCtxKey)
	if !ok {
		return nil
	}
	d, _ := v.(*console.SessionData)
	return d
}

// bearer extracts the raw token from the "Authorization: Bearer <token>" header.
func bearer(c *gin.Context) string {
	return strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
}

// cors allows the browser frontend (default http://localhost:3000) to call the
// API with credentials. Because credentialed requests forbid a wildcard origin,
// it reflects the configured origin and short-circuits preflight OPTIONS.
func (s *Server) cors() gin.HandlerFunc {
	allowed := s.deps.CORSOrigin
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowed == "*" || origin == allowed) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
			c.Header("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
