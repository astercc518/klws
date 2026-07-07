// internal/api/auth.go — Auth controller.
//
// Login validates credentials with console.UserRepo.Authenticate, creates a
// Redis session via console.SessionStore, and returns a Bearer token (the same
// HMAC-signed session id the old console cookie used). No new crypto/session
// logic is introduced here.
package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/console"
)

// resetTokenTTL bounds how long a password-reset link is valid.
const resetTokenTTL = 30 * time.Minute

// loginRequest is the POST /api/v1/auth/login body.
type loginRequest struct {
	Email     string `json:"email" binding:"required"`
	Password  string `json:"password" binding:"required"`
	Turnstile string `json:"cf_turnstile_token"`
}

// loginResponse is the data payload returned on success.
type loginResponse struct {
	Token    string `json:"token"`     // send as "Authorization: Bearer <token>"
	Role     string `json:"role"`      // admin | sales | customer
	TenantID *int64 `json:"tenant_id"` // non-nil only for customers
}

// meResponse describes the current session (GET /api/v1/auth/me).
type meResponse struct {
	UserID   int64  `json:"user_id"`
	Role     string `json:"role"`
	TenantID *int64 `json:"tenant_id"`
}

// handleLogin: POST /api/v1/auth/login (public).
func (s *Server) handleLogin(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if !verifyTurnstile(c.Request.Context(), req.Turnstile, c.ClientIP()) {
		fail(c, http.StatusBadRequest, "人机验证未通过，请重试")
		return
	}
	ctx := c.Request.Context()

	user, err := s.deps.Users.Authenticate(ctx, req.Email, req.Password)
	if errors.Is(err, console.ErrInvalidCredentials) {
		// Same generic message for wrong email or password — no account enumeration.
		fail(c, http.StatusUnauthorized, "邮箱或密码错误")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "authentication failed")
		return
	}

	sid, err := s.deps.Sessions.Create(ctx, console.SessionData{
		UserID:   user.ID,
		Role:     user.Role,
		TenantID: user.TenantID,
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "could not create session")
		return
	}

	// The Bearer token is the HMAC-signed session id — identical to the cookie value.
	token := console.SignCookie(s.deps.SessionKey, sid)
	ok(c, loginResponse{Token: token, Role: string(user.Role), TenantID: user.TenantID})
}

// registerRequest is the POST /api/v1/auth/register body (customer self-signup).
type registerRequest struct {
	Email     string `json:"email" binding:"required"`
	Password  string `json:"password" binding:"required,min=6"`
	Turnstile string `json:"cf_turnstile_token"` // forwarded for optional CF verification
}

// handleRegister: POST /api/v1/auth/register (public). Provisions a tenant + a
// customer login account, then issues a session — mirroring handleLogin's token.
// Reuses the same repos as the admin "create tenant/user" path; no red-line
// (billing/dispatch/store/sendgate/cluster) logic is touched.
func (s *Server) handleRegister(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body (email + password>=6 required)")
		return
	}
	if !verifyTurnstile(c.Request.Context(), req.Turnstile, c.ClientIP()) {
		fail(c, http.StatusBadRequest, "人机验证未通过，请重试")
		return
	}
	ctx := c.Request.Context()
	pool := s.deps.Mgr.SystemPool()

	// Pre-check the email so we don't leave an orphan tenant behind on conflict.
	var exists int
	if err := pool.QueryRow(ctx,
		`SELECT 1 FROM console_users WHERE email=$1`, req.Email,
	).Scan(&exists); err == nil {
		fail(c, http.StatusConflict, "邮箱已被注册")
		return
	}

	// 1. Provision a tenant for the new customer (named after the email).
	var tenantID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name, status) VALUES ($1,'active') RETURNING id`, req.Email,
	).Scan(&tenantID); err != nil {
		fail(c, http.StatusInternalServerError, "could not create tenant")
		return
	}

	// 2. Create the customer login account (argon2id hash, same as seed/admin).
	hash, err := console.HashPassword(req.Password)
	if err != nil {
		fail(c, http.StatusInternalServerError, "hash password failed")
		return
	}
	tid := tenantID
	uid, err := s.deps.Users.Create(ctx, req.Email, hash, console.RoleCustomer, &tid)
	if err != nil {
		// Roll back the orphan tenant (best-effort) and report a conflict.
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID)
		fail(c, http.StatusConflict, "邮箱已被注册")
		return
	}

	// 3. Issue a session — identical token shape to login.
	sid, err := s.deps.Sessions.Create(ctx, console.SessionData{
		UserID:   uid,
		Role:     console.RoleCustomer,
		TenantID: &tid,
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "could not create session")
		return
	}
	token := console.SignCookie(s.deps.SessionKey, sid)
	ok(c, loginResponse{Token: token, Role: string(console.RoleCustomer), TenantID: &tid})
}

// forgotPasswordRequest is the POST /api/v1/auth/password/forgot body.
type forgotPasswordRequest struct {
	Email     string `json:"email" binding:"required"`
	Turnstile string `json:"cf_turnstile_token"`
}

// handleForgotPassword: POST /api/v1/auth/password/forgot (public). Always
// returns success (no account enumeration). If the email maps to an enabled
// user, a stateless, HMAC-signed, time-boxed reset token is minted; lacking an
// SMTP integration here, the reset link is logged for the operator to deliver.
func (s *Server) handleForgotPassword(c *gin.Context) {
	var req forgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid email")
		return
	}
	if !verifyTurnstile(c.Request.Context(), req.Turnstile, c.ClientIP()) {
		fail(c, http.StatusBadRequest, "人机验证未通过，请重试")
		return
	}
	ctx := c.Request.Context()

	var uid int64
	err := s.deps.Mgr.SystemPool().QueryRow(ctx,
		`SELECT id FROM console_users WHERE email=$1 AND disabled = false`, req.Email,
	).Scan(&uid)
	if err == nil {
		exp := time.Now().Add(resetTokenTTL).Unix()
		// payload "<uid>.<exp>"; SignCookie appends ".<hmac>" → stateless token.
		token := console.SignCookie(s.deps.SessionKey, fmt.Sprintf("%d.%d", uid, exp))
		// No SMTP wired here: log the link so it can be delivered out of band.
		log.Printf("[auth] password reset requested for %s — link: /reset-password?token=%s", req.Email, token)
	}

	// Identical response whether or not the account exists.
	ok(c, gin.H{"sent": true})
}

// resetPasswordRequest is the POST /api/v1/auth/password/reset body.
type resetPasswordRequest struct {
	Token    string `json:"token" binding:"required"`
	Password string `json:"password" binding:"required,min=6"`
}

// handleResetPassword: POST /api/v1/auth/password/reset (public). Verifies the
// stateless reset token (HMAC + expiry) and updates the password hash.
func (s *Server) handleResetPassword(c *gin.Context) {
	var req resetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body (token + password>=6 required)")
		return
	}

	payload, valid := console.VerifyCookie(s.deps.SessionKey, req.Token)
	if !valid {
		fail(c, http.StatusBadRequest, "重置链接无效或已过期")
		return
	}
	dot := strings.LastIndexByte(payload, '.')
	if dot <= 0 {
		fail(c, http.StatusBadRequest, "重置链接无效")
		return
	}
	uid, err1 := strconv.ParseInt(payload[:dot], 10, 64)
	exp, err2 := strconv.ParseInt(payload[dot+1:], 10, 64)
	if err1 != nil || err2 != nil {
		fail(c, http.StatusBadRequest, "重置链接无效")
		return
	}
	if time.Now().Unix() > exp {
		fail(c, http.StatusBadRequest, "重置链接已过期，请重新申请")
		return
	}

	hash, err := console.HashPassword(req.Password)
	if err != nil {
		fail(c, http.StatusInternalServerError, "hash password failed")
		return
	}
	if _, err := s.deps.Mgr.SystemPool().Exec(c.Request.Context(),
		`UPDATE console_users SET password_hash=$1 WHERE id=$2`, hash, uid,
	); err != nil {
		fail(c, http.StatusInternalServerError, "could not update password")
		return
	}
	ok(c, gin.H{"reset": true})
}

// handleLogout: POST /api/v1/auth/logout (auth required). Destroys the session.
func (s *Server) handleLogout(c *gin.Context) {
	if sid, valid := console.VerifyCookie(s.deps.SessionKey, bearer(c)); valid {
		_ = s.deps.Sessions.Destroy(c.Request.Context(), sid)
	}
	ok(c, gin.H{"logged_out": true})
}

// handleMe: GET /api/v1/auth/me (auth required). Echoes the current session.
func (s *Server) handleMe(c *gin.Context) {
	data := sessionFrom(c)
	if data == nil { // requireAuth guarantees this, but stay defensive
		fail(c, http.StatusUnauthorized, "no session")
		return
	}
	ok(c, meResponse{UserID: data.UserID, Role: string(data.Role), TenantID: data.TenantID})
}
