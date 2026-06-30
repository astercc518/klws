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

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/console"
	"github.com/acme/wadist/internal/pricing"
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

	SessionKey []byte // HMAC key for signing/verifying the session token
	BlindKey   []byte // HMAC blind-index key (campaign recipient dedup/suppression)
	CORSOrigin string // allowed browser origin (default http://localhost:3000)
}

// Server holds the injected deps plus a readiness flag (mirrors console.Server
// so the same drain-on-SIGTERM lifecycle applies).
type Server struct {
	deps  Deps
	ready atomic.Bool
	srv   *http.Server
	ln    net.Listener
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

	// --- Sales controller (a sales user's own customers) ---
	sales := v1.Group("/sales", s.requireAuth(), s.requireRole(console.RoleSales))
	{
		sales.GET("/customers", s.handleSalesCustomers)
		sales.GET("/customers/:id", s.handleSalesCustomerDetail)
		sales.POST("/customers/:id/pricing", s.handleSalesSetPricing)
	}

	// --- Admin controller (super-admin "god view") ---
	admin := v1.Group("/admin", s.requireAuth(), s.requireRole(console.RoleAdmin))
	{
		admin.GET("/stats", s.handleAdminStats)

		admin.GET("/tenants", s.handleAdminListTenants)
		admin.POST("/tenants", s.handleAdminCreateTenant)
		admin.GET("/users", s.handleAdminListUsers)
		admin.POST("/users", s.handleAdminCreateUser)
		admin.POST("/users/:id/disable", s.handleAdminSetUserDisabled)
		admin.POST("/users/:id/password", s.handleAdminResetUserPassword)
		admin.POST("/sales/:id/assign", s.handleAdminAssignSales)

		admin.POST("/finance/topup", s.handleAdminTopup)
		admin.POST("/finance/pricing", s.handleAdminSetPricing)
		admin.GET("/finance/ledger", s.handleAdminLedger)

		admin.GET("/resources/proxies", s.handleAdminListProxies)
		admin.POST("/resources/proxies", s.handleAdminImportProxies)
		admin.GET("/resources/devices", s.handleAdminListDevices)
		admin.POST("/resources/devices", s.handleAdminImportDevices)
		admin.POST("/resources/devices/:id/proxy", s.handleAdminBindDeviceProxy)
		admin.DELETE("/resources/devices/:id/proxy", s.handleAdminUnbindDeviceProxy)

		admin.GET("/settings/risk", s.handleAdminGetRiskConfig)
		admin.PUT("/settings/risk", s.handleAdminUpdateRiskConfig)

		admin.GET("/campaigns", s.handleAdminListCampaigns)
		admin.GET("/campaigns/:id/recipients", s.handleAdminListCampaignRecipients)
		admin.POST("/campaigns/:id/stop", s.handleAdminStopCampaign)
		admin.POST("/campaigns/:id/resume", s.handleAdminResumeCampaign)
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
