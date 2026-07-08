# SP9 — 账户自助(退出/改密)+ 系统设置 + 系统日志 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the four operator-reported gaps: verified logout + visible entry, self-service password change (all roles, revokes other sessions), a 系统设置 page (registration toggle + default new-tenant pricing + risk policy merged in), and a 系统日志 page (operation audit / login log / runtime log).

**Architecture:** All backend work lives in `internal/api` (handlers) + `internal/console` (one SessionStore method) + `cmd/console` (log tee) + `migrations/0018`. Login events reuse the append-only `audit_log` (nullable `actor_id`). Runtime logs are an in-process ring buffer — no persistence, no docker access. Frontend follows the existing house patterns (button-tab rows, `Dialog` + `sonner` toasts, `ProDataTable`).

**Tech Stack:** Go/Gin + pgx + testcontainers (PG & Redis), Next.js (App Router — **this repo's Next differs from training data; read `frontend/node_modules/next/dist/docs/` before using an unfamiliar API**, e.g. `redirect`), Base UI, sonner, lucide.

**Spec:** `docs/superpowers/specs/2026-07-08-admin-console-sp9-account-settings-logs-design.md`

## Global Constraints

- **红线**: engine packages (`internal/{billing,dispatch,store,sendgate,cluster}`) untouched. (`billing`/`pricing` repos are *called*, never modified.)
- Wrong old password on change → **HTTP 400**, never 401 (frontend `request()` treats 401 as session-expired → clears token + redirects `/login`).
- Passwords never appear in audit details or logs.
- Unit prices are **integer cents** (`unit_price_cents`), matching `pricing.Repo.SetPrice(ctx, tenantID, country string, unitPrice int64)`.
- `registration_open` absent row = **true** (preserves current behavior).
- Login audit rows are written **only after Turnstile passes**.
- New audit actions (exact strings): `user.change_password`, `auth.login`, `auth.login_failed`, `auth.register`, `auth.logout`, `settings.update`.
- Backend tests: testcontainers (postgres:16 / redis:7), run with `go test ./internal/... -run <Name> -count=1` (Docker required). Frontend: `npm run build` + `npx eslint .` — the ~29-warning house baseline (`react-hooks/set-state-in-effect` etc.) must not grow with NEW rule types; matching existing house patterns is fine.
- Commit style: `feat(admin)/fix(auth)/docs(spec): …` one-line, imperative.

---

### Task 1: `SessionStore.DestroyAllForUser` (internal/console)

**Files:**
- Modify: `internal/console/session.go` (add method at end of file)
- Test: `internal/console/session_test.go` (append)

**Interfaces:**
- Produces: `func (s *SessionStore) DestroyAllForUser(ctx context.Context, uid int64, exceptSID string) (int, error)` — deletes every Redis session whose `SessionData.UserID == uid` except `exceptSID`; returns count destroyed. Used by Task 2.

- [ ] **Step 1: Write the failing test** — append to `internal/console/session_test.go` (reuses the existing `newTestRedis` helper in that file):

```go
func TestDestroyAllForUserSparesCurrentSession(t *testing.T) {
	ctx := context.Background()
	store := NewSessionStore(newTestRedis(t), time.Hour)

	keep, err := store.Create(ctx, SessionData{UserID: 42, Role: RoleAdmin})
	if err != nil {
		t.Fatalf("create keep: %v", err)
	}
	kill1, err := store.Create(ctx, SessionData{UserID: 42, Role: RoleAdmin})
	if err != nil {
		t.Fatalf("create kill1: %v", err)
	}
	other, err := store.Create(ctx, SessionData{UserID: 7, Role: RoleCustomer, TenantID: ptrInt64(1)})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}

	n, err := store.DestroyAllForUser(ctx, 42, keep)
	if err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if n != 1 {
		t.Fatalf("destroyed = %d, want 1", n)
	}
	if _, err := store.Get(ctx, keep); err != nil {
		t.Fatalf("current session must survive: %v", err)
	}
	if _, err := store.Get(ctx, kill1); !errors.Is(err, ErrNoSession) {
		t.Fatalf("other session of uid 42 must be gone, got %v", err)
	}
	if _, err := store.Get(ctx, other); err != nil {
		t.Fatalf("unrelated user's session must survive: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/console/ -run TestDestroyAllForUserSparesCurrentSession -count=1`
Expected: FAIL — `store.DestroyAllForUser undefined`

- [ ] **Step 3: Implement** — append to `internal/console/session.go` (add `"strings"` to imports; `encoding/json`, `errors`, `fmt` already imported):

```go
// DestroyAllForUser deletes every session belonging to uid except exceptSID
// (the caller's own, e.g. the session that just changed the password) and
// returns how many were destroyed. SCAN-based full sweep: console sessions
// number in the tens, so an index would be overkill. Payloads that fail to
// unmarshal are skipped, never deleted — they may not be ours.
func (s *SessionStore) DestroyAllForUser(ctx context.Context, uid int64, exceptSID string) (int, error) {
	destroyed := 0
	iter := s.rdb.Scan(ctx, 0, sessionKeyPrefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		if strings.TrimPrefix(key, sessionKeyPrefix) == exceptSID {
			continue
		}
		raw, err := s.rdb.Get(ctx, key).Bytes()
		if errors.Is(err, redis.Nil) {
			continue // expired between SCAN and GET
		}
		if err != nil {
			return destroyed, fmt.Errorf("redis get %s: %w", key, err)
		}
		var d SessionData
		if err := json.Unmarshal(raw, &d); err != nil || d.UserID != uid {
			continue
		}
		if err := s.rdb.Del(ctx, key).Err(); err != nil {
			return destroyed, fmt.Errorf("redis del %s: %w", key, err)
		}
		destroyed++
	}
	if err := iter.Err(); err != nil {
		return destroyed, fmt.Errorf("redis scan: %w", err)
	}
	return destroyed, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/console/ -run 'TestDestroyAllForUser|TestSessionStore' -count=1`
Expected: PASS (both new and existing session tests)

- [ ] **Step 5: Commit**

```bash
git add internal/console/session.go internal/console/session_test.go
git commit -m "feat(console): SessionStore.DestroyAllForUser — revoke a user's other sessions"
```

---

### Task 2: `POST /auth/password/change` (all roles)

**Files:**
- Modify: `internal/api/router.go` (route + stash sid in requireAuth)
- Modify: `internal/api/auth.go` (new handler at end of file)
- Test: `internal/api/auth_db_test.go` (append)

**Interfaces:**
- Consumes: `DestroyAllForUser(ctx, uid, exceptSID)` from Task 1.
- Produces: route `POST /api/v1/auth/password/change` body `{old_password, new_password}` → `{"changed": true, "revoked_sessions": <n>}`; gin ctx key `sidCtxKey` + helper `sidFrom(c *gin.Context) string`; audit action `user.change_password`. Frontend Task 8 calls this route.

- [ ] **Step 1: Write the failing test** — append to `internal/api/auth_db_test.go`. Note `newAuthServer` in this file wires real PG+Redis but no Audit; extend the test with its own audit writer so the audit row is asserted too:

```go
// seedConsoleUser inserts a console user with the given plaintext password and
// returns its id.
func seedConsoleUser(t *testing.T, s *Server, email, plain string, role console.Role) int64 {
	t.Helper()
	hash, err := console.HashPassword(plain)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	uid, err := s.deps.Users.Create(context.Background(), email, hash, role, nil)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return uid
}

func doChangePassword(s *Server, sd *console.SessionData, sid, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/password/change", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(sessionCtxKey, sd)
	c.Set(sidCtxKey, sid)
	s.handleChangePassword(c)
	return w
}

func TestChangePassword(t *testing.T) {
	s, ctx := newAuthServer(t)
	s.deps.Audit = audit.NewAuditWriter(s.sysPool)
	uid := seedConsoleUser(t, s, "cp-admin", "oldpass123", console.RoleAdmin)
	sd := &console.SessionData{UserID: uid, Role: console.RoleAdmin}

	current, err := s.deps.Sessions.Create(ctx, *sd)
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}
	stale, err := s.deps.Sessions.Create(ctx, *sd)
	if err != nil {
		t.Fatalf("create stale session: %v", err)
	}

	// Wrong old password → 400 (NOT 401: frontend would treat 401 as expiry).
	w := doChangePassword(s, sd, current, `{"old_password":"WRONG","new_password":"newpass456"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("wrong-old status = %d, want 400; body=%s", w.Code, w.Body.String())
	}

	// Short new password → 400 via binding.
	w = doChangePassword(s, sd, current, `{"old_password":"oldpass123","new_password":"short"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("short-new status = %d, want 400", w.Code)
	}

	// Happy path.
	w = doChangePassword(s, sd, current, `{"old_password":"oldpass123","new_password":"newpass456"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("change status = %d, body=%s", w.Code, w.Body.String())
	}
	// New password authenticates; old one no longer does.
	if _, err := s.deps.Users.Authenticate(ctx, "cp-admin", "newpass456"); err != nil {
		t.Fatalf("authenticate with new password: %v", err)
	}
	if _, err := s.deps.Users.Authenticate(ctx, "cp-admin", "oldpass123"); !errors.Is(err, console.ErrInvalidCredentials) {
		t.Fatalf("old password must be rejected, got %v", err)
	}
	// Other session revoked, current survives.
	if _, err := s.deps.Sessions.Get(ctx, stale); !errors.Is(err, console.ErrNoSession) {
		t.Fatalf("stale session must be revoked, got %v", err)
	}
	if _, err := s.deps.Sessions.Get(ctx, current); err != nil {
		t.Fatalf("current session must survive: %v", err)
	}
	// Audited without any password material.
	var details []byte
	var action string
	if err := s.sysPool.QueryRow(ctx,
		`SELECT action, coalesce(details,'{}'::jsonb)::text FROM audit_log WHERE actor_id=$1 ORDER BY id DESC LIMIT 1`,
		uid).Scan(&action, &details); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if action != "user.change_password" {
		t.Fatalf("audit action = %q", action)
	}
	if strings.Contains(string(details), "pass") {
		t.Fatalf("audit details must not contain password material: %s", details)
	}
}
```

Add to the file's imports: `"errors"`, `"github.com/acme/wadist/internal/audit"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestChangePassword -count=1`
Expected: FAIL — `s.handleChangePassword undefined`, `sidCtxKey undefined`

- [ ] **Step 3: Implement.** In `internal/api/router.go`:

(a) below `const sessionCtxKey = "wadist_session"` add:

```go
// sidCtxKey holds the raw (verified) session id, so handlers that revoke the
// user's OTHER sessions can spare the one making the request.
const sidCtxKey = "wadist_sid"
```

(b) in `requireAuth()`, right after `c.Set(sessionCtxKey, data)` add:

```go
			c.Set(sidCtxKey, sid)
```

(c) below `sessionFrom` add:

```go
// sidFrom returns the current request's verified session id ("" if absent).
func sidFrom(c *gin.Context) string {
	v, ok := c.Get(sidCtxKey)
	if !ok {
		return ""
	}
	sid, _ := v.(string)
	return sid
}
```

(d) in the auth route group, after the `password/reset` line add:

```go
		auth.POST("/password/change", s.requireAuth(), s.handleChangePassword)
```

Then append to `internal/api/auth.go`:

```go
// changePasswordRequest is the POST /api/v1/auth/password/change body.
type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=6"`
}

// handleChangePassword: POST /api/v1/auth/password/change (auth required, any
// role). Verifies the old password, swaps the hash, then revokes every OTHER
// session of this user so a leaked old credential can't keep a live session.
// A wrong old password is HTTP 400 — NOT 401 — because the frontend globally
// treats 401 as "session expired" (clears token, redirects to /login), which
// would log the user out over a typo.
func (s *Server) handleChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "old_password + new_password(>=6) required")
		return
	}
	sd := sessionFrom(c)
	if sd == nil { // requireAuth guarantees this; stay defensive
		fail(c, http.StatusUnauthorized, "no session")
		return
	}
	if req.NewPassword == req.OldPassword {
		fail(c, http.StatusBadRequest, "新密码不能与旧密码相同")
		return
	}
	ctx := c.Request.Context()

	var hash string
	if err := s.systemPool().QueryRow(ctx,
		`SELECT password_hash FROM console_users WHERE id=$1 AND disabled=false`, sd.UserID,
	).Scan(&hash); err != nil {
		fail(c, http.StatusInternalServerError, "load account failed")
		return
	}
	okOld, err := console.VerifyPassword(hash, req.OldPassword)
	if err != nil {
		fail(c, http.StatusInternalServerError, "verify password failed")
		return
	}
	if !okOld {
		fail(c, http.StatusBadRequest, "旧密码错误")
		return
	}

	newHash, err := console.HashPassword(req.NewPassword)
	if err != nil {
		fail(c, http.StatusInternalServerError, "hash password failed")
		return
	}
	if _, err := s.systemPool().Exec(ctx,
		`UPDATE console_users SET password_hash=$1, updated_at=now() WHERE id=$2`, newHash, sd.UserID,
	); err != nil {
		fail(c, http.StatusInternalServerError, "could not update password")
		return
	}

	// Best-effort revocation: the password IS changed at this point; a Redis
	// hiccup must not fail the request. The count is reported so the UI can
	// tell the user their other logins were signed out.
	revoked := 0
	if n, err := s.deps.Sessions.DestroyAllForUser(ctx, sd.UserID, sidFrom(c)); err != nil {
		log.Printf("[auth] revoke sessions uid=%d: %v", sd.UserID, err)
	} else {
		revoked = n
	}

	s.recordAudit(ctx, auditEvent{ActorID: sd.UserID, Action: "user.change_password",
		ResourceType: "user", ResourceID: sd.UserID,
		Details: map[string]any{"revoked_sessions": revoked}})
	ok(c, gin.H{"changed": true, "revoked_sessions": revoked})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/api/ -run TestChangePassword -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/router.go internal/api/auth.go internal/api/auth_db_test.go
git commit -m "feat(auth): self-service password change — verifies old, revokes other sessions, audited"
```

---

### Task 3: Login/register/logout audit events

**Files:**
- Modify: `internal/api/auth.go` (`handleLogin`, `handleRegister`, `handleLogout`)
- Test: `internal/api/auth_db_test.go` (append)

**Interfaces:**
- Produces: audit rows `auth.login` (actor=uid, details `{ip,ua}`), `auth.login_failed` (actor NULL, details `{identifier,ip,ua,reason:"invalid_credentials"}`), `auth.register` (actor=new uid), `auth.logout` (actor=uid). Task 10's 登录日志 tab reads these via the audit list API. (Deviation from spec noted: `Authenticate` collapses wrong-password and disabled into one error by design — reason is always `invalid_credentials`.)

- [ ] **Step 1: Write the failing test** — append to `internal/api/auth_db_test.go`:

```go
func TestLoginAuditEvents(t *testing.T) {
	s, ctx := newAuthServer(t)
	s.deps.Audit = audit.NewAuditWriter(s.sysPool)
	uid := seedConsoleUser(t, s, "aud-user", "secret123", console.RoleAdmin)

	// Failure first: wrong password → auth.login_failed with NULL actor.
	if w := doLogin(s, `{"email":"aud-user","password":"nope"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d", w.Code)
	}
	var action, detailsTxt string
	var actor *int64
	if err := s.sysPool.QueryRow(ctx,
		`SELECT action, actor_id, details::text FROM audit_log WHERE action='auth.login_failed' ORDER BY id DESC LIMIT 1`,
	).Scan(&action, &actor, &detailsTxt); err != nil {
		t.Fatalf("login_failed row: %v", err)
	}
	if actor != nil {
		t.Fatalf("login_failed actor must be NULL, got %v", *actor)
	}
	if !strings.Contains(detailsTxt, `"identifier":"aud-user"`) || !strings.Contains(detailsTxt, `"reason":"invalid_credentials"`) {
		t.Fatalf("login_failed details = %s", detailsTxt)
	}
	if strings.Contains(detailsTxt, "nope") {
		t.Fatalf("attempted password leaked into details: %s", detailsTxt)
	}

	// Success → auth.login with actor = uid.
	if w := doLogin(s, `{"email":"aud-user","password":"secret123"}`); w.Code != http.StatusOK {
		t.Fatalf("good login status = %d", w.Code)
	}
	var gotActor int64
	if err := s.sysPool.QueryRow(ctx,
		`SELECT actor_id FROM audit_log WHERE action='auth.login' ORDER BY id DESC LIMIT 1`,
	).Scan(&gotActor); err != nil {
		t.Fatalf("auth.login row: %v", err)
	}
	if gotActor != uid {
		t.Fatalf("auth.login actor = %d, want %d", gotActor, uid)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestLoginAuditEvents -count=1`
Expected: FAIL — no `auth.login_failed` row found

- [ ] **Step 3: Implement.** In `internal/api/auth.go` `handleLogin`:

(a) replace the invalid-credentials branch body:

```go
	user, err := s.deps.Users.Authenticate(ctx, req.Email, req.Password)
	if errors.Is(err, console.ErrInvalidCredentials) {
		// Post-Turnstile only, so bot floods don't bloat audit_log. The
		// attempted password is deliberately NOT recorded.
		s.recordAudit(ctx, auditEvent{Action: "auth.login_failed",
			Details: map[string]any{
				"identifier": req.Email, "ip": c.ClientIP(),
				"ua": c.Request.UserAgent(), "reason": "invalid_credentials",
			}})
		// Same generic message for wrong email or password — no account enumeration.
		fail(c, http.StatusUnauthorized, "邮箱或密码错误")
		return
	}
```

(b) just before `ok(c, loginResponse{...})` add:

```go
	ev := auditEvent{ActorID: user.ID, Action: "auth.login", ResourceType: "user",
		ResourceID: user.ID,
		Details:    map[string]any{"ip": c.ClientIP(), "ua": c.Request.UserAgent()}}
	if user.TenantID != nil {
		ev.TenantID = *user.TenantID
	}
	s.recordAudit(ctx, ev)
```

(c) in `handleRegister`, just before its final `ok(c, loginResponse{...})` add:

```go
	s.recordAudit(ctx, auditEvent{TenantID: tid, ActorID: uid, Action: "auth.register",
		ResourceType: "user", ResourceID: uid,
		Details: map[string]any{"ip": c.ClientIP(), "ua": c.Request.UserAgent()}})
```

(d) in `handleLogout`, before `ok(c, gin.H{"logged_out": true})` add:

```go
	if aid := actorID(c); aid != 0 {
		s.recordAudit(c.Request.Context(), auditEvent{ActorID: aid, Action: "auth.logout",
			ResourceType: "user", ResourceID: aid})
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -run 'TestLoginAuditEvents|TestLoginAcceptsUsernameIdentifier' -count=1`
Expected: PASS (new + the SP6 login regression test)

- [ ] **Step 5: Commit**

```bash
git add internal/api/auth.go internal/api/auth_db_test.go
git commit -m "feat(auth): audit login success/failure, register, logout to audit_log"
```

---

### Task 4: `platform_settings` storage + GET/PUT endpoints

**Files:**
- Create: `migrations/0018_platform_settings.sql`
- Create: `internal/api/settings.go`
- Modify: `internal/api/router.go` (2 routes)
- Test: `internal/api/settings_db_test.go` (new)

**Interfaces:**
- Produces:
  - `type platformSettings struct { RegistrationOpen bool `json:"registration_open"`; DefaultPricing []defaultPricingRow `json:"default_pricing"` }`
  - `type defaultPricingRow struct { Country string `json:"country"`; UnitPriceCents int64 `json:"unit_price_cents"` }`
  - `func (s *Server) loadPlatformSettings(ctx context.Context) (platformSettings, error)` — defaults: open=true, pricing=[] when rows absent. Used by Task 5.
  - Routes `GET/PUT /api/v1/admin/settings/platform`; audit `settings.update`. Frontend Task 9 consumes.

- [ ] **Step 1: Write the migration** — `migrations/0018_platform_settings.sql`:

```sql
-- Platform-wide operator settings (registration toggle, new-tenant defaults).
-- Key/value JSONB so future settings need no schema change. Absent key =
-- built-in default (registration_open=true, default_pricing=[]).
CREATE TABLE IF NOT EXISTS platform_settings (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
GRANT SELECT, INSERT, UPDATE ON platform_settings TO app_system;
```

- [ ] **Step 2: Write the failing test** — `internal/api/settings_db_test.go`:

```go
// internal/api/settings_db_test.go — DB tests for the platform settings
// endpoints (SP9): defaults when unset, PUT validation, round-trip, audit.
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/audit"
	"github.com/acme/wadist/internal/console"
)

// newSettingsServer wires a Server around a migrated pool (sysPool seam), with
// a real audit writer.
func newSettingsServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	pool := testPool(t)
	s := &Server{sysPool: pool}
	s.deps.Audit = audit.NewAuditWriter(pool)
	return s, context.Background()
}

func adminCtx(w *httptest.ResponseRecorder, method, path, body string) *gin.Context {
	c, _ := gin.CreateTestContext(w)
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	c.Request = httptest.NewRequest(method, path, rdr)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(sessionCtxKey, &console.SessionData{UserID: 1, Role: console.RoleAdmin})
	return c
}

func TestPlatformSettingsDefaultsAndRoundTrip(t *testing.T) {
	s, ctx := newSettingsServer(t)

	// Defaults when no rows exist.
	w := httptest.NewRecorder()
	s.handleAdminGetPlatformSettings(adminCtx(w, http.MethodGet, "/api/v1/admin/settings/platform", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"registration_open":true`) ||
		!strings.Contains(w.Body.String(), `"default_pricing":[]`) {
		t.Fatalf("defaults body = %s", w.Body.String())
	}

	// PUT rejects a bad country / non-positive price / duplicate country.
	for _, bad := range []string{
		`{"registration_open":true,"default_pricing":[{"country":"USA","unit_price_cents":5}]}`,
		`{"registration_open":true,"default_pricing":[{"country":"US","unit_price_cents":0}]}`,
		`{"registration_open":true,"default_pricing":[{"country":"US","unit_price_cents":5},{"country":"us","unit_price_cents":7}]}`,
	} {
		w = httptest.NewRecorder()
		s.handleAdminUpdatePlatformSettings(adminCtx(w, http.MethodPut, "/api/v1/admin/settings/platform", bad))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("bad body %s → status %d, want 400", bad, w.Code)
		}
	}

	// Valid PUT round-trips (country normalized to upper case).
	w = httptest.NewRecorder()
	s.handleAdminUpdatePlatformSettings(adminCtx(w, http.MethodPut, "/api/v1/admin/settings/platform",
		`{"registration_open":false,"default_pricing":[{"country":"us","unit_price_cents":5}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("put status = %d, body=%s", w.Code, w.Body.String())
	}
	got, err := s.loadPlatformSettings(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.RegistrationOpen || len(got.DefaultPricing) != 1 ||
		got.DefaultPricing[0].Country != "US" || got.DefaultPricing[0].UnitPriceCents != 5 {
		t.Fatalf("round-trip = %+v", got)
	}

	// Audited.
	var action string
	if err := s.sysPool.QueryRow(ctx,
		`SELECT action FROM audit_log WHERE action='settings.update' ORDER BY id DESC LIMIT 1`,
	).Scan(&action); err != nil {
		t.Fatalf("settings.update audit row: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestPlatformSettingsDefaultsAndRoundTrip -count=1`
Expected: FAIL — `s.handleAdminGetPlatformSettings undefined`

- [ ] **Step 4: Implement** — `internal/api/settings.go`:

```go
// internal/api/settings.go — platform-wide operator settings (SP9).
// Storage: platform_settings key/value JSONB (migration 0018). Absent row =
// built-in default, so a fresh database behaves exactly like pre-SP9.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"
)

const (
	settingRegistrationOpen = "registration_open"
	settingDefaultPricing   = "default_pricing"
)

var countryRe = regexp.MustCompile(`^[A-Z]{2}$`)

// defaultPricingRow is one default price applied to newly created tenants.
// UnitPriceCents matches pricing.Repo.SetPrice's integer-cents contract.
type defaultPricingRow struct {
	Country        string `json:"country"`
	UnitPriceCents int64  `json:"unit_price_cents"`
}

// platformSettings is the full settings document the admin UI edits.
type platformSettings struct {
	RegistrationOpen bool                `json:"registration_open"`
	DefaultPricing   []defaultPricingRow `json:"default_pricing"`
}

// loadPlatformSettings reads both keys, filling defaults for absent rows.
func (s *Server) loadPlatformSettings(ctx context.Context) (platformSettings, error) {
	out := platformSettings{RegistrationOpen: true, DefaultPricing: []defaultPricingRow{}}
	rows, err := s.systemPool().Query(ctx,
		`SELECT key, value FROM platform_settings WHERE key = ANY($1)`,
		[]string{settingRegistrationOpen, settingDefaultPricing})
	if err != nil {
		return out, fmt.Errorf("load platform settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return out, fmt.Errorf("scan platform setting: %w", err)
		}
		switch key {
		case settingRegistrationOpen:
			_ = json.Unmarshal(raw, &out.RegistrationOpen)
		case settingDefaultPricing:
			_ = json.Unmarshal(raw, &out.DefaultPricing)
		}
	}
	return out, rows.Err()
}

// handleAdminGetPlatformSettings: GET /api/v1/admin/settings/platform.
func (s *Server) handleAdminGetPlatformSettings(c *gin.Context) {
	ps, err := s.loadPlatformSettings(c.Request.Context())
	if err != nil {
		fail(c, http.StatusInternalServerError, "load settings failed")
		return
	}
	ok(c, ps)
}

// handleAdminUpdatePlatformSettings: PUT /api/v1/admin/settings/platform.
// Both fields are required-present (pointer + binding) so a false toggle is
// distinguishable from a missing one — same convention as the risk config.
func (s *Server) handleAdminUpdatePlatformSettings(c *gin.Context) {
	var req struct {
		RegistrationOpen *bool                `json:"registration_open" binding:"required"`
		DefaultPricing   *[]defaultPricingRow `json:"default_pricing" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "registration_open + default_pricing required")
		return
	}
	pricing := *req.DefaultPricing
	seen := map[string]bool{}
	for i := range pricing {
		pricing[i].Country = strings.ToUpper(pricing[i].Country)
		if !countryRe.MatchString(pricing[i].Country) {
			fail(c, http.StatusBadRequest, "country must be a 2-letter code")
			return
		}
		if pricing[i].UnitPriceCents <= 0 {
			fail(c, http.StatusBadRequest, "unit_price_cents must be positive")
			return
		}
		if seen[pricing[i].Country] {
			fail(c, http.StatusBadRequest, "duplicate country: "+pricing[i].Country)
			return
		}
		seen[pricing[i].Country] = true
	}

	ctx := c.Request.Context()
	tx, err := s.systemPool().Begin(ctx)
	if err != nil {
		fail(c, http.StatusInternalServerError, "begin failed")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	upsert := `INSERT INTO platform_settings (key, value, updated_at) VALUES ($1, $2, now())
	           ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`
	openJSON, _ := json.Marshal(*req.RegistrationOpen)
	pricingJSON, _ := json.Marshal(pricing)
	if _, err := tx.Exec(ctx, upsert, settingRegistrationOpen, openJSON); err != nil {
		fail(c, http.StatusInternalServerError, "save settings failed")
		return
	}
	if _, err := tx.Exec(ctx, upsert, settingDefaultPricing, pricingJSON); err != nil {
		fail(c, http.StatusInternalServerError, "save settings failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit failed")
		return
	}

	s.recordAudit(ctx, auditEvent{ActorID: actorID(c), Action: "settings.update",
		ResourceType: "settings",
		Details: map[string]any{
			"registration_open": *req.RegistrationOpen,
			"default_pricing":   pricing,
		}})
	ok(c, platformSettings{RegistrationOpen: *req.RegistrationOpen, DefaultPricing: pricing})
}

```

(`normalizeCountry` is just `strings.ToUpper` — use it directly and add `"strings"` to the imports instead of defining a helper.)

In `internal/api/router.go`, after the `settings/risk` lines add:

```go
		admin.GET("/settings/platform", s.handleAdminGetPlatformSettings)
		admin.PUT("/settings/platform", s.handleAdminUpdatePlatformSettings)
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/api/ -run TestPlatformSettingsDefaultsAndRoundTrip -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add migrations/0018_platform_settings.sql internal/api/settings.go internal/api/router.go internal/api/settings_db_test.go
git commit -m "feat(admin): platform_settings storage + GET/PUT /admin/settings/platform"
```

---

### Task 5: Enforcement — registration gate + default pricing on tenant create

**Files:**
- Modify: `internal/api/auth.go` (`handleRegister` — gate + default pricing + pool seam)
- Modify: `internal/api/admin_api.go` (`handleAdminCreateTenant` — default pricing)
- Test: `internal/api/settings_db_test.go` (append)

**Interfaces:**
- Consumes: `loadPlatformSettings` (Task 4), `s.deps.Pricing.SetPrice(ctx, tenantID, country, unitPriceCents)`.
- Produces: closed registration → HTTP 403 `注册已关闭`; new tenants (admin-created AND self-registered) get `tenant_pricing` rows from `default_pricing`; `tenant.create` audit details gain `default_pricing_applied` count.

- [ ] **Step 1: Write the failing test** — append to `internal/api/settings_db_test.go` (imports gain `"github.com/acme/wadist/internal/pricing"`):

```go
func TestRegistrationGateAndDefaultPricing(t *testing.T) {
	s, ctx := newSettingsServer(t)
	s.deps.Pricing = pricing.NewRepo(s.sysPool)

	// Seed: registration CLOSED + one default price.
	for k, v := range map[string]string{
		"registration_open": `false`,
		"default_pricing":   `[{"country":"US","unit_price_cents":5}]`,
	} {
		if _, err := s.sysPool.Exec(ctx,
			`INSERT INTO platform_settings (key, value) VALUES ($1, $2)`, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	// Register is refused with 403.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/register",
		strings.NewReader(`{"email":"gate@x.com","password":"secret123"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleRegister(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("closed-registration status = %d, want 403; body=%s", w.Code, w.Body.String())
	}

	// Admin tenant create applies the default pricing.
	w = httptest.NewRecorder()
	s.handleAdminCreateTenant(adminCtx(w, http.MethodPost, "/api/v1/admin/tenants", `{"name":"Defaults Inc"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("create tenant status = %d, body=%s", w.Code, w.Body.String())
	}
	var n int
	if err := s.sysPool.QueryRow(ctx,
		`SELECT count(*) FROM tenant_pricing tp JOIN tenants t ON t.id = tp.tenant_id
		  WHERE t.name='Defaults Inc' AND tp.country='US' AND tp.unit_price=5`,
	).Scan(&n); err != nil || n != 1 {
		t.Fatalf("default pricing rows = %d (err %v), want 1", n, err)
	}
}
```

> If the `tenant_pricing` column names differ (check `migrations/0011_tenant_pricing.sql`), adjust the assertion's column names — NOT the handler contract.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestRegistrationGateAndDefaultPricing -count=1`
Expected: FAIL — register returns 500/nil-pointer (deps.Mgr nil) or 200; pricing count 0. (First fix in Step 3 is the `systemPool()` seam that also makes this handler testable.)

- [ ] **Step 3: Implement.**

(a) `internal/api/auth.go` `handleRegister`: replace `pool := s.deps.Mgr.SystemPool()` with `pool := s.systemPool()` (test seam, same production behavior), then insert the gate right after the Turnstile check:

```go
	ctx := c.Request.Context()
	pool := s.systemPool()

	// Platform gate: the operator can close self-signup entirely.
	if ps, err := s.loadPlatformSettings(ctx); err == nil && !ps.RegistrationOpen {
		fail(c, http.StatusForbidden, "注册已关闭，请联系管理员开通账号")
		return
	}
```

(remove the now-duplicate `ctx := c.Request.Context()` line below).

(b) same handler, after the user is created (before the session step), apply defaults:

```go
	// New-tenant defaults (best-effort): price rows from platform settings.
	s.applyDefaultPricing(ctx, tenantID)
```

(c) `internal/api/settings.go`: add the shared helper:

```go
// applyDefaultPricing writes the platform default price rows for a newly
// created tenant. Best-effort by design: pricing failures must not undo an
// already-created tenant; they are logged and surfaced via the returned count.
func (s *Server) applyDefaultPricing(ctx context.Context, tenantID int64) int {
	if s.deps.Pricing == nil {
		return 0
	}
	ps, err := s.loadPlatformSettings(ctx)
	if err != nil {
		log.Printf("[settings] load defaults for tenant %d: %v", tenantID, err)
		return 0
	}
	applied := 0
	for _, p := range ps.DefaultPricing {
		if err := s.deps.Pricing.SetPrice(ctx, tenantID, p.Country, p.UnitPriceCents); err != nil {
			log.Printf("[settings] default price %s tenant=%d: %v", p.Country, tenantID, err)
			continue
		}
		applied++
	}
	return applied
}
```

(add `"log"` to settings.go imports.)

(d) `internal/api/admin_api.go` `handleAdminCreateTenant`: first replace its `s.deps.Mgr.SystemPool()` with `s.systemPool()` (same test seam as (a) — without it the Step-1 test panics on the nil `Mgr`). Then, after the INSERT succeeds, before `recordAudit`:

```go
	applied := s.applyDefaultPricing(c.Request.Context(), id)
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: id, ActorID: actorID(c),
		Action: "tenant.create", ResourceType: "tenant", ResourceID: id,
		Details: map[string]any{"name": req.Name, "default_pricing_applied": applied}})
```

(replacing the existing `recordAudit` call.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -run 'TestRegistrationGateAndDefaultPricing|TestPlatformSettings' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/auth.go internal/api/admin_api.go internal/api/settings.go internal/api/settings_db_test.go
git commit -m "feat(admin): enforce registration toggle; apply default pricing to new tenants"
```

---

### Task 6: Audit list `action_prefix` / `exclude_prefix` filters

**Files:**
- Modify: `internal/api/audit.go` (`auditFilter`, `buildAuditWhere`)
- Modify: `internal/api/admin_api.go` (`parseAuditFilter`)
- Test: `internal/api/audit_test.go` (append — pure-function tests live there)

**Interfaces:**
- Produces: `GET /admin/audit?action_prefix=auth.` (only auth events) and `?exclude_prefix=auth.` (operations view without login noise). Frontend Task 10 consumes both.

- [ ] **Step 1: Write the failing test** — append to `internal/api/audit_test.go`:

```go
func TestBuildAuditWherePrefixFilters(t *testing.T) {
	where, args := buildAuditWhere(auditFilter{ActionPrefix: "auth."})
	if where != " WHERE a.action LIKE $1" || len(args) != 1 || args[0] != "auth.%" {
		t.Fatalf("prefix: where=%q args=%v", where, args)
	}
	where, args = buildAuditWhere(auditFilter{ExcludePrefix: "auth."})
	if where != " WHERE a.action NOT LIKE $1" || len(args) != 1 || args[0] != "auth.%" {
		t.Fatalf("exclude: where=%q args=%v", where, args)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestBuildAuditWherePrefixFilters -count=1`
Expected: FAIL — unknown fields `ActionPrefix`/`ExcludePrefix`

- [ ] **Step 3: Implement.** In `internal/api/audit.go`:

(a) `auditFilter` gains two fields after `Action string`:

```go
	ActionPrefix  string // matches action LIKE '<prefix>%'
	ExcludePrefix string // matches action NOT LIKE '<prefix>%'
```

(b) in `buildAuditWhere`, after the `f.Action` block:

```go
	if f.ActionPrefix != "" {
		add("a.action LIKE $%d", f.ActionPrefix+"%")
	}
	if f.ExcludePrefix != "" {
		add("a.action NOT LIKE $%d", f.ExcludePrefix+"%")
	}
```

(c) in `internal/api/admin_api.go` `parseAuditFilter`, extend the first line:

```go
	f := auditFilter{
		Action:        c.Query("action"),
		ActionPrefix:  c.Query("action_prefix"),
		ExcludePrefix: c.Query("exclude_prefix"),
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api/ -run 'TestBuildAudit' -count=1`
Expected: PASS (new + existing buildAudit tests)

- [ ] **Step 5: Commit**

```bash
git add internal/api/audit.go internal/api/admin_api.go internal/api/audit_test.go
git commit -m "feat(admin): audit list action_prefix/exclude_prefix filters for the login-log split"
```

---

### Task 7: Runtime log ring buffer + endpoint + main.go tee

**Files:**
- Create: `internal/api/runtimelog.go`
- Create: `internal/api/runtimelog_test.go`
- Modify: `internal/api/router.go` (Deps field + route)
- Modify: `cmd/console/main.go` (tee both log streams)

**Interfaces:**
- Produces: `func NewRingWriter(capacity int) *RingWriter`; `(io.Writer)`-compatible `Write`; `func (w *RingWriter) Tail(limit int) []string` (chronological, newest last); `Deps.RuntimeLog *RingWriter`; route `GET /api/v1/admin/logs/runtime?limit=` → `{"lines": [...], "capacity": 2000}`. Frontend Task 10 consumes.

- [ ] **Step 1: Write the failing test** — `internal/api/runtimelog_test.go`:

```go
// internal/api/runtimelog_test.go — pure unit tests for the runtime-log ring
// buffer (SP9): ordering, wrap-around, redaction, multi-line writes.
package api

import (
	"fmt"
	"strings"
	"testing"
)

func TestRingWriterTailOrderAndWrap(t *testing.T) {
	w := NewRingWriter(3)
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(w, "line-%d\n", i)
	}
	got := w.Tail(10)
	want := []string{"line-3", "line-4", "line-5"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("tail = %v, want %v", got, want)
	}
	if got := w.Tail(2); len(got) != 2 || got[0] != "line-4" || got[1] != "line-5" {
		t.Fatalf("tail(2) = %v", got)
	}
}

func TestRingWriterRedactsTokens(t *testing.T) {
	w := NewRingWriter(4)
	fmt.Fprintf(w, "[auth] password reset requested for x — link: /reset-password?token=abc.123_x-Y\n")
	got := w.Tail(1)
	if len(got) != 1 || strings.Contains(got[0], "abc.123") || !strings.Contains(got[0], "token=[REDACTED]") {
		t.Fatalf("redaction failed: %v", got)
	}
}

func TestRingWriterSplitsMultiLineWrites(t *testing.T) {
	w := NewRingWriter(4)
	if _, err := w.Write([]byte("a\nb\nc\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := w.Tail(10); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("split = %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestRingWriter -count=1`
Expected: FAIL — `NewRingWriter undefined`

- [ ] **Step 3: Implement** — `internal/api/runtimelog.go`:

```go
// internal/api/runtimelog.go — in-process runtime log capture (SP9).
//
// A fixed-capacity, thread-safe, line-oriented ring buffer that cmd/console
// tees its own log output into, exposed read-only at GET /admin/logs/runtime.
// Deliberate boundaries: this process only (the send-engine container's logs
// stay ops-only), no persistence (restart clears it), and secret-bearing
// patterns are redacted BEFORE a line enters the buffer.
package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// tokenRe matches signed-token query params (e.g. the password-reset link the
// forgot-password handler logs). Redacted at write time so the admin UI can
// never leak a live credential.
var tokenRe = regexp.MustCompile(`token=[A-Za-z0-9._~-]+`)

// RingWriter is an io.Writer that keeps the most recent `capacity` lines.
type RingWriter struct {
	mu    sync.Mutex
	lines []string
	next  int
	full  bool
}

func NewRingWriter(capacity int) *RingWriter {
	if capacity < 1 {
		capacity = 1
	}
	return &RingWriter{lines: make([]string, capacity)}
}

// Write splits p into lines, redacts each, and appends them. Always reports
// the full length as written (a log tee must never error the primary stream).
func (w *RingWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		w.push(tokenRe.ReplaceAllString(line, "token=[REDACTED]"))
	}
	return len(p), nil
}

func (w *RingWriter) push(line string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines[w.next] = line
	w.next = (w.next + 1) % len(w.lines)
	if w.next == 0 {
		w.full = true
	}
}

// Tail returns up to limit of the most recent lines in chronological order
// (oldest first, newest last).
func (w *RingWriter) Tail(limit int) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var ordered []string
	if w.full {
		ordered = append(ordered, w.lines[w.next:]...)
		ordered = append(ordered, w.lines[:w.next]...)
	} else {
		ordered = append(ordered, w.lines[:w.next]...)
	}
	if limit > 0 && len(ordered) > limit {
		ordered = ordered[len(ordered)-limit:]
	}
	return ordered
}

// Capacity returns the fixed line capacity (0 for a nil writer).
func (w *RingWriter) Capacity() int {
	if w == nil {
		return 0
	}
	return len(w.lines)
}

// handleAdminRuntimeLogs: GET /api/v1/admin/logs/runtime?limit= (admin).
// Read-only; not audited (it is a read, like the audit list itself).
func (s *Server) handleAdminRuntimeLogs(c *gin.Context) {
	limit := 500
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	lines := []string{}
	if s.deps.RuntimeLog != nil {
		lines = s.deps.RuntimeLog.Tail(limit)
	}
	ok(c, gin.H{"lines": lines, "capacity": s.deps.RuntimeLog.Capacity()})
}
```

In `internal/api/router.go`:

(a) `Deps` gains (after `CORSOrigin string`):

```go
	RuntimeLog *RingWriter // in-process log capture for GET /admin/logs/runtime (nil = empty)
```

(b) admin group, after the `audit` route:

```go
		admin.GET("/logs/runtime", s.handleAdminRuntimeLogs)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/api/ -run TestRingWriter -count=1`
Expected: PASS

- [ ] **Step 5: Tee both log streams in `cmd/console/main.go`.** Replace:

```go
	logger, flush, err := walog.Production()
	if err != nil {
		return nil, nil, err
	}
```

with:

```go
	// SP9: tee BOTH log streams (stdlib log used by the api handlers, and the
	// zap logger used by store internals) into an in-process ring buffer that
	// GET /admin/logs/runtime serves. Stderr stays the primary sink.
	ring := api.NewRingWriter(2000)
	log.SetOutput(io.MultiWriter(os.Stderr, ring))
	zl := zap.New(zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.NewMultiWriteSyncer(zapcore.Lock(os.Stderr), zapcore.AddSync(ring)),
		zapcore.InfoLevel,
	))
	logger := walog.New(zl)
	flush := func() { _ = zl.Sync() }
```

Imports gain `"io"`, `"go.uber.org/zap"`, `"go.uber.org/zap/zapcore"` (zap is already an indirect dependency via internal/log; `go mod tidy` promotes it). And in the `api.Deps{...}` literal add:

```go
		RuntimeLog: ring,
```

- [ ] **Step 6: Verify the binary still builds and the smoke test passes**

Run: `go build ./cmd/console && go test ./cmd/console/ -run TestSmoke -count=1 && go mod tidy && git diff --stat go.mod go.sum`
Expected: build OK; smoke PASS (skips if no docker); go.mod diff only adds zap as direct.

- [ ] **Step 7: Commit**

```bash
git add internal/api/runtimelog.go internal/api/runtimelog_test.go internal/api/router.go cmd/console/main.go go.mod go.sum
git commit -m "feat(admin): runtime log ring buffer + GET /admin/logs/runtime, teed from both log streams"
```

---

### Task 8: Frontend — ChangePasswordDialog + sidebar logout + header wiring

**Files:**
- Create: `frontend/components/change-password-dialog.tsx`
- Modify: `frontend/components/admin-header.tsx` (menu item + dialog)
- Modify: `frontend/components/admin-sidebar.tsx` (footer logout button)
- Modify: `frontend/components/header.tsx` (customer: button + dialog)
- Modify: `frontend/components/sales-header.tsx` (sales: button + dialog)

**Interfaces:**
- Consumes: `POST /auth/password/change` (Task 2).
- Produces: `export function ChangePasswordDialog({ open, onClose }: { open: boolean; onClose: () => void })`.

- [ ] **Step 1: Create `frontend/components/change-password-dialog.tsx`:**

```tsx
"use client";

// change-password-dialog.tsx — self-service password change (SP9), shared by
// the admin header menu and the customer/sales headers. POSTs
// /auth/password/change; the backend revokes the user's OTHER sessions, so on
// success the current tab stays logged in.

import { useEffect, useState } from "react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

export function ChangePasswordDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);

  // Reset fields each time the dialog opens.
  useEffect(() => {
    if (open) {
      setOldPassword("");
      setNewPassword("");
      setConfirm("");
      setBusy(false);
    }
  }, [open]);

  const mismatch = confirm !== "" && confirm !== newPassword;
  const tooShort = newPassword !== "" && newPassword.length < 6;
  const canSubmit = oldPassword !== "" && newPassword.length >= 6 && confirm === newPassword && !busy;

  async function submit() {
    if (!canSubmit) return;
    setBusy(true);
    try {
      const res = await api.post<{ changed: boolean; revoked_sessions: number }>(
        "/auth/password/change",
        { old_password: oldPassword, new_password: newPassword },
      );
      toast.success("密码已修改", {
        description:
          res.revoked_sessions > 0
            ? `其他 ${res.revoked_sessions} 个登录会话已退出`
            : "当前会话保持登录",
      });
      onClose();
    } catch (e) {
      toast.error("修改失败", { description: e instanceof ApiError ? e.message : "请重试" });
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>修改密码</DialogTitle>
          <DialogDescription>修改成功后,其他设备上的登录会话将被退出。</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 py-2">
          <Input
            type="password"
            autoComplete="current-password"
            placeholder="旧密码"
            value={oldPassword}
            onChange={(e) => setOldPassword(e.target.value)}
          />
          <Input
            type="password"
            autoComplete="new-password"
            placeholder="新密码(至少 6 位)"
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
            aria-invalid={tooShort || undefined}
          />
          <Input
            type="password"
            autoComplete="new-password"
            placeholder="确认新密码"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            aria-invalid={mismatch || undefined}
          />
          {mismatch && <p className="text-xs text-destructive">两次输入的新密码不一致</p>}
          {tooShort && <p className="text-xs text-destructive">新密码至少 6 位</p>}
        </div>
        <DialogFooter>
          <DialogClose
            render={
              <Button variant="outline" onClick={onClose}>
                取消
              </Button>
            }
          />
          <Button onClick={submit} disabled={!canSubmit}>
            {busy ? "提交中…" : "确认修改"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
```

> Check `frontend/components/ui/dialog.tsx` + an existing usage (`admin-user-dialogs.tsx`) for the exact `DialogClose` composition — mirror whatever pattern the house dialogs use (e.g. plain `<DialogClose>` child vs `render` prop) rather than the sketch above if they differ.

- [ ] **Step 2: Wire into `admin-header.tsx`.** Add imports:

```tsx
import { useState } from "react";
import { KeyRound } from "lucide-react"; // merge into the existing lucide import
import { ChangePasswordDialog } from "@/components/change-password-dialog";
```

In `AdminHeader()` add state `const [pwOpen, setPwOpen] = useState(false);`, then in the dropdown content, ABOVE the logout item:

```tsx
          <DropdownMenuItem onClick={() => setPwOpen(true)}>
            <KeyRound className="size-4" />
            修改密码
          </DropdownMenuItem>
```

and after the closing `</DropdownMenu>` render:

```tsx
      <ChangePasswordDialog open={pwOpen} onClose={() => setPwOpen(false)} />
```

- [ ] **Step 3: Sidebar footer logout in `admin-sidebar.tsx`.** Add imports (`LogOut` into the lucide import, `logout` from `@/lib/api`), a handler inside the component:

```tsx
  async function handleLogout() {
    await logout().catch(() => {});
    window.location.href = "/login";
  }
```

and in the footer `div` (between `AdminIdentity` and the collapse toggle) add:

```tsx
          <button
            type="button"
            onClick={handleLogout}
            title="退出登录"
            aria-label="退出登录"
            className="flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <LogOut className="size-4" />
          </button>
```

(When collapsed the footer is `justify-center`; the two icon buttons stack fine — verify visually in Task 11.)

- [ ] **Step 4: Customer + sales headers.** In `frontend/components/header.tsx` and `frontend/components/sales-header.tsx`: add `"use client"` is already present; import `useState`, `KeyRound`, `ChangePasswordDialog`; add `const [pwOpen, setPwOpen] = useState(false);`; next to the existing 退出登录 button add:

```tsx
        <Button variant="ghost" size="sm" onClick={() => setPwOpen(true)} className="text-muted-foreground">
          <KeyRound className="size-4" />
          修改密码
        </Button>
```

and render `<ChangePasswordDialog open={pwOpen} onClose={() => setPwOpen(false)} />` before the closing `</header>`.

- [ ] **Step 5: Build + lint**

Run: `cd frontend && npm run build && npx eslint components/change-password-dialog.tsx components/admin-header.tsx components/admin-sidebar.tsx components/header.tsx components/sales-header.tsx`
Expected: build OK; no NEW lint rule types beyond the house baseline.

- [ ] **Step 6: Commit**

```bash
git add frontend/components/change-password-dialog.tsx frontend/components/admin-header.tsx frontend/components/admin-sidebar.tsx frontend/components/header.tsx frontend/components/sales-header.tsx
git commit -m "feat(frontend): change-password dialog in all three shells + persistent sidebar logout"
```

---

### Task 9: Frontend — 系统设置 page (平台设置 + 风控策略 tabs)

**Files:**
- Create: `frontend/components/admin-platform-settings.tsx`
- Create: `frontend/components/admin-settings-tabs.tsx`
- Create: `frontend/app/admin/settings/page.tsx`
- Modify: `frontend/app/admin/settings/risk/page.tsx` (→ redirect)
- Modify: `frontend/components/admin/nav.ts` (策略中心 → 系统设置)

**Interfaces:**
- Consumes: `GET/PUT /admin/settings/platform` (Task 4) — shape `{registration_open: boolean, default_pricing: {country: string, unit_price_cents: number}[]}`; existing `AdminRiskSettings` component.
- Produces: route `/admin/settings` (tab deep-link `?tab=risk`).

- [ ] **Step 1: `frontend/components/admin-platform-settings.tsx`:**

```tsx
"use client";

// admin-platform-settings.tsx — the 平台设置 tab (SP9): registration toggle +
// default new-tenant pricing rows. Prices are edited in dollars but stored/
// transported as integer cents (matching the pricing repo's contract).

import { useCallback, useEffect, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

interface PricingRow {
  country: string;
  price: string; // dollars, as typed
}

interface PlatformSettings {
  registration_open: boolean;
  default_pricing: { country: string; unit_price_cents: number }[];
}

// Same pill-switch pattern as admin-risk-settings.tsx (no Switch primitive).
function Toggle({
  checked,
  onChange,
  disabled,
  label,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        "relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors disabled:cursor-not-allowed disabled:opacity-50",
        checked ? "bg-primary" : "bg-muted-foreground/30",
      )}
    >
      <span
        className={cn(
          "inline-block size-4 transform rounded-full bg-background shadow transition-transform",
          checked ? "translate-x-4" : "translate-x-0.5",
        )}
      />
    </button>
  );
}

export function AdminPlatformSettings() {
  const [open, setOpen] = useState(true);
  const [rows, setRows] = useState<PricingRow[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const data = await api.get<PlatformSettings>("/admin/settings/platform");
      setOpen(data.registration_open);
      setRows(
        data.default_pricing.map((p) => ({
          country: p.country,
          price: (p.unit_price_cents / 100).toFixed(2),
        })),
      );
      setLoaded(true);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  function updateRow(i: number, patch: Partial<PricingRow>) {
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  }

  const invalid = rows.some(
    (r) =>
      !/^[A-Za-z]{2}$/.test(r.country) ||
      !(parseFloat(r.price) > 0) ||
      Math.round(parseFloat(r.price) * 100) <= 0,
  );
  const dupes = new Set(rows.map((r) => r.country.toUpperCase())).size !== rows.length;

  async function save() {
    if (invalid || dupes || busy) return;
    setBusy(true);
    try {
      await api.put("/admin/settings/platform", {
        registration_open: open,
        default_pricing: rows.map((r) => ({
          country: r.country.toUpperCase(),
          unit_price_cents: Math.round(parseFloat(r.price) * 100),
        })),
      });
      toast.success("平台设置已保存");
    } catch (e) {
      toast.error("保存失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  if (error) return <p className="text-sm text-destructive">{error}</p>;
  if (!loaded) return <p className="text-sm text-muted-foreground">加载中…</p>;

  return (
    <div className="grid gap-4">
      <Card>
        <CardContent className="flex items-center justify-between p-4">
          <div>
            <div className="text-sm font-medium">开放自助注册</div>
            <p className="text-xs text-muted-foreground">
              关闭后,访客无法自行注册账号;管理员仍可在客户页手动开户。
            </p>
          </div>
          <Toggle checked={open} onChange={setOpen} disabled={busy} label="开放自助注册" />
        </CardContent>
      </Card>

      <Card>
        <CardContent className="grid gap-3 p-4">
          <div>
            <div className="text-sm font-medium">新租户默认发信单价</div>
            <p className="text-xs text-muted-foreground">
              新建租户(含自助注册)时自动写入的国家单价;留空则新租户无默认定价。
            </p>
          </div>
          {rows.map((r, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                value={r.country}
                onChange={(e) => updateRow(i, { country: e.target.value })}
                placeholder="国家码(US)"
                className="w-28 font-mono uppercase"
                maxLength={2}
              />
              <Input
                value={r.price}
                onChange={(e) => updateRow(i, { price: e.target.value })}
                placeholder="单价($)"
                className="w-32 font-mono"
                inputMode="decimal"
              />
              <button
                type="button"
                aria-label="删除此行"
                onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}
                className="flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-destructive"
              >
                <Trash2 className="size-4" />
              </button>
            </div>
          ))}
          {dupes && <p className="text-xs text-destructive">国家码重复</p>}
          <div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setRows((rs) => [...rs, { country: "", price: "" }])}
            >
              <Plus className="size-4" />
              添加国家
            </Button>
          </div>
        </CardContent>
      </Card>

      <div>
        <Button onClick={save} disabled={busy || invalid || dupes}>
          {busy ? "保存中…" : "保存平台设置"}
        </Button>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: `frontend/components/admin-settings-tabs.tsx`:**

```tsx
"use client";

// admin-settings-tabs.tsx — 系统设置 page shell (SP9). Two tabs: 平台设置
// (new) and 风控策略 (the pre-SP9 standalone page, folded in unchanged).
// Deep-linkable via ?tab=risk (the old /admin/settings/risk route redirects
// here). Future settings sections slot in as additional tabs.

import { useState } from "react";
import { useSearchParams } from "next/navigation";
import { AdminPlatformSettings } from "@/components/admin-platform-settings";
import { AdminRiskSettings } from "@/components/admin-risk-settings";

const TABS = [
  { key: "platform", label: "平台设置" },
  { key: "risk", label: "风控策略" },
] as const;

type TabKey = (typeof TABS)[number]["key"];

export function AdminSettingsTabs() {
  const params = useSearchParams();
  const [tab, setTab] = useState<TabKey>(params.get("tab") === "risk" ? "risk" : "platform");

  return (
    <div className="grid gap-4">
      <div className="inline-flex w-fit gap-1 rounded-lg bg-muted p-1">
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
              (tab === t.key
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground")
            }
          >
            {t.label}
          </button>
        ))}
      </div>
      {tab === "platform" ? <AdminPlatformSettings /> : <AdminRiskSettings />}
    </div>
  );
}
```

- [ ] **Step 3: Pages.** `frontend/app/admin/settings/page.tsx` (new):

```tsx
import { Suspense } from "react";
import { PageHeader } from "@/components/admin/page-header";
import { AdminSettingsTabs } from "@/components/admin-settings-tabs";

export default function AdminSettingsPage() {
  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader
        eyebrow="God View · Settings"
        title="系统设置"
        description="平台级开关与默认值,以及全局风控策略。"
      />
      <Suspense>
        <AdminSettingsTabs />
      </Suspense>
    </div>
  );
}
```

(`useSearchParams` requires a Suspense boundary in App Router — **verify against `frontend/node_modules/next/dist/docs/`**.)

Replace the body of `frontend/app/admin/settings/risk/page.tsx`:

```tsx
import { redirect } from "next/navigation";

// SP9: the standalone risk page folded into 系统设置 as a tab. Old deep links
// keep working via this redirect.
export default function AdminRiskSettingsPage() {
  redirect("/admin/settings?tab=risk");
}
```

- [ ] **Step 4: Nav.** In `frontend/components/admin/nav.ts`: add `Settings` to the lucide import; replace the 策略中心 group with:

```ts
  {
    title: "系统设置",
    items: [
      { href: "/admin/settings", label: "系统设置", en: "Settings", icon: Settings },
    ],
  },
```

Remove the now-unused `SlidersHorizontal` import if nothing else uses it.

- [ ] **Step 5: Build + lint**

Run: `cd frontend && npm run build && npx eslint components/admin-platform-settings.tsx components/admin-settings-tabs.tsx app/admin/settings`
Expected: build OK (both `/admin/settings` and the redirect compile); no new lint rule types.

- [ ] **Step 6: Commit**

```bash
git add frontend/components/admin-platform-settings.tsx frontend/components/admin-settings-tabs.tsx frontend/app/admin/settings/ frontend/components/admin/nav.ts
git commit -m "feat(frontend): 系统设置 page — platform settings tab + risk policy folded in, nav rename"
```

---

### Task 10: Frontend — 系统日志 page (操作审计 / 登录日志 / 运行日志)

**Files:**
- Create: `frontend/components/admin-login-log.tsx`
- Create: `frontend/components/admin-runtime-logs.tsx`
- Create: `frontend/components/admin-logs-tabs.tsx`
- Modify: `frontend/components/admin-audit-log.tsx` (exclude auth.* from the ops view)
- Modify: `frontend/app/admin/audit/page.tsx` (rename + tabs)
- Modify: `frontend/components/admin/nav.ts` (系统审计 → 系统日志)

**Interfaces:**
- Consumes: `GET /admin/audit?action_prefix=auth.` / `?exclude_prefix=auth.` (Task 6), login audit rows (Task 3), `GET /admin/logs/runtime` → `{lines: string[], capacity: number}` (Task 7).

- [ ] **Step 1: Ops view excludes auth noise.** In `frontend/components/admin-audit-log.tsx` change the fetch line to:

```tsx
      const data = await api.get<{ rows: AuditRow[]; total: number }>(
        "/admin/audit?limit=200&exclude_prefix=auth.",
      );
```

- [ ] **Step 2: `frontend/components/admin-login-log.tsx`:**

```tsx
"use client";

// admin-login-log.tsx — 登录日志 tab (SP9). Reads the auth.* slice of
// audit_log (login success/failure, register, logout) with purpose-built
// columns. Failures carry the attempted identifier + ip in details.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";

interface LoginRow {
  id: number;
  occurred_at: string;
  actor_id: number | null;
  actor_email: string | null;
  action: string;
  details: { identifier?: string; ip?: string; ua?: string; reason?: string } | null;
}

const ACTION_LABEL: Record<string, { label: string; variant: "default" | "secondary" | "destructive" | "outline" }> = {
  "auth.login": { label: "登录成功", variant: "default" },
  "auth.login_failed": { label: "登录失败", variant: "destructive" },
  "auth.register": { label: "注册", variant: "secondary" },
  "auth.logout": { label: "退出登录", variant: "outline" },
};

export function AdminLoginLog() {
  const [rows, setRows] = useState<LoginRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const data = await api.get<{ rows: LoginRow[]; total: number }>(
        "/admin/audit?limit=200&action_prefix=auth.",
      );
      setRows(data.rows);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const columns: Column<LoginRow>[] = [
    {
      key: "occurred_at",
      header: "时间",
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.occurred_at}</span>,
    },
    {
      key: "action",
      header: "动作",
      cell: (r) => {
        const a = ACTION_LABEL[r.action] ?? { label: r.action, variant: "outline" as const };
        return <Badge variant={a.variant}>{a.label}</Badge>;
      },
    },
    {
      key: "account",
      header: "账号",
      cell: (r) => (
        <span className="text-sm">{r.actor_email ?? r.details?.identifier ?? (r.actor_id ? `#${r.actor_id}` : "—")}</span>
      ),
    },
    {
      key: "ip",
      header: "IP",
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.details?.ip ?? "—"}</span>,
    },
    {
      key: "ua",
      header: "客户端",
      cell: (r) => {
        const ua = r.details?.ua ?? "";
        return (
          <span className="font-mono text-xs text-muted-foreground" title={ua}>
            {ua.length > 40 ? ua.slice(0, 39) + "…" : ua || "—"}
          </span>
        );
      },
    },
  ];

  return (
    <ProDataTable
      data={rows}
      error={error}
      columns={columns}
      getRowKey={(r) => r.id}
      search={{
        placeholder: "搜索账号/IP…",
        accessor: (r) => `${r.actor_email ?? ""} ${r.details?.identifier ?? ""} ${r.details?.ip ?? ""}`,
      }}
      emptyState="暂无登录记录"
    />
  );
}
```

> `Badge` may not have a `destructive` variant in this repo — it does (checked `ui/badge.tsx`), but if the variant union differs, map 登录失败 to the closest allowed variant instead of changing badge.tsx.

- [ ] **Step 3: `frontend/components/admin-runtime-logs.tsx`:**

```tsx
"use client";

// admin-runtime-logs.tsx — 运行日志 tab (SP9). Read-only view of the console
// backend's in-process ring buffer. Boundaries are stated in the UI: this
// process only (send-engine logs stay ops-side), cleared on restart.

import { useCallback, useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export function AdminRuntimeLogs() {
  const [lines, setLines] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setBusy(true);
    try {
      const data = await api.get<{ lines: string[]; capacity: number }>(
        "/admin/logs/runtime?limit=1000",
      );
      setLines(data.lines);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const shown = (lines ?? []).filter((l) => !filter || l.toLowerCase().includes(filter.toLowerCase()));

  return (
    <div className="grid gap-3">
      <div className="flex items-center gap-2">
        <Input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="关键字过滤…"
          className="w-64"
        />
        <Button variant="outline" size="sm" onClick={load} disabled={busy}>
          <RefreshCw className={busy ? "size-4 animate-spin" : "size-4"} />
          刷新
        </Button>
        <span className="text-xs text-muted-foreground">
          仅当前后端进程日志(重启即清);发送引擎日志请在服务器查看。
        </span>
      </div>
      {error ? (
        <p className="text-sm text-destructive">{error}</p>
      ) : lines === null ? (
        <p className="text-sm text-muted-foreground">加载中…</p>
      ) : shown.length === 0 ? (
        <p className="text-sm text-muted-foreground">暂无日志</p>
      ) : (
        <div className="max-h-[60vh] overflow-auto rounded-lg border bg-muted/30 p-3">
          <pre className="whitespace-pre-wrap break-all font-mono text-xs leading-5">
            {shown.join("\n")}
          </pre>
        </div>
      )}
    </div>
  );
}
```

- [ ] **Step 4: `frontend/components/admin-logs-tabs.tsx`:**

```tsx
"use client";

// admin-logs-tabs.tsx — 系统日志 page shell (SP9): 操作审计 (the pre-SP9
// audit table, now excluding auth.* noise) / 登录日志 / 运行日志.

import { useState } from "react";
import { AdminAuditLog } from "@/components/admin-audit-log";
import { AdminLoginLog } from "@/components/admin-login-log";
import { AdminRuntimeLogs } from "@/components/admin-runtime-logs";

const TABS = [
  { key: "audit", label: "操作审计" },
  { key: "login", label: "登录日志" },
  { key: "runtime", label: "运行日志" },
] as const;

type TabKey = (typeof TABS)[number]["key"];

export function AdminLogsTabs() {
  const [tab, setTab] = useState<TabKey>("audit");

  return (
    <div className="grid gap-4">
      <div className="inline-flex w-fit gap-1 rounded-lg bg-muted p-1">
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
              (tab === t.key
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground")
            }
          >
            {t.label}
          </button>
        ))}
      </div>
      {tab === "audit" ? <AdminAuditLog /> : tab === "login" ? <AdminLoginLog /> : <AdminRuntimeLogs />}
    </div>
  );
}
```

- [ ] **Step 5: Page + nav.** Replace `frontend/app/admin/audit/page.tsx`:

```tsx
import { PageHeader } from "@/components/admin/page-header";
import { AdminLogsTabs } from "@/components/admin-logs-tabs";

export default function AdminAuditPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="God View · Logs"
        title="系统日志"
        description="平台操作审计、账号登录记录与后台运行日志。"
      />
      <AdminLogsTabs />
    </div>
  );
}
```

In `frontend/components/admin/nav.ts` replace the 系统审计 group with:

```ts
  {
    title: "系统日志",
    items: [{ href: "/admin/audit", label: "系统日志", en: "Logs", icon: ScrollText }],
  },
```

- [ ] **Step 6: Build + lint**

Run: `cd frontend && npm run build && npx eslint components/admin-login-log.tsx components/admin-runtime-logs.tsx components/admin-logs-tabs.tsx components/admin-audit-log.tsx app/admin/audit`
Expected: build OK; no new lint rule types.

- [ ] **Step 7: Commit**

```bash
git add frontend/components/admin-login-log.tsx frontend/components/admin-runtime-logs.tsx frontend/components/admin-logs-tabs.tsx frontend/components/admin-audit-log.tsx frontend/app/admin/audit/page.tsx frontend/components/admin/nav.ts
git commit -m "feat(frontend): 系统日志 page — ops audit / login log / runtime log tabs"
```

---

### Task 11: Runtime E2E smoke (local stack + Playwright) + logout diagnosis + final sweep

**Files:**
- No product code expected (fixes only if the smoke finds a defect).

This task settles the "logout did nothing" report with evidence and gives the roadmap its first runtime end-to-end pass. **Never run any of this against the production compose stack.**

- [ ] **Step 1: Local stack.** Start throwaway containers on non-conflicting ports:

```bash
docker run --rm -d --name sp9-pg -e POSTGRES_USER=test -e POSTGRES_PASSWORD=test -e POSTGRES_DB=app_test -p 55432:5432 postgres:16
docker run --rm -d --name sp9-redis -p 56379:6379 redis:7
sleep 3
for f in migrations/*.sql; do [[ "$f" == *.down.sql ]] && continue; docker exec -i sp9-pg psql -U test -d app_test < "$f" || break; done
```

Inspect `migrations/0008_security.sql` for the `app_tenant`/`app_system` role definitions; if they are created without passwords, set them: `docker exec sp9-pg psql -U test -d app_test -c "ALTER ROLE app_system PASSWORD 'test'; ALTER ROLE app_tenant PASSWORD 'test';"`.

Seed an admin (hash via a one-off Go run):

```bash
HASH=$(go run ./scripts/hashpw 2>/dev/null || cat <<'EOF' | go run -
package main

import (
	"fmt"

	"github.com/acme/wadist/internal/console"
)

func main() {
	h, err := console.HashPassword("smoke-admin-pass")
	if err != nil {
		panic(err)
	}
	fmt.Print(h)
}
EOF
)
docker exec sp9-pg psql -U test -d app_test -c "INSERT INTO console_users (email, password_hash, role) VALUES ('smokeadmin', '$HASH', 'admin')"
```

(If `go run -` doesn't accept stdin in this Go version, write the snippet to the session scratchpad and `go run` it from there — do NOT commit it.)

- [ ] **Step 2: Run backend + frontend locally** (Turnstile unset → fail-open; session key is the test one from `cmd/console/main_smoke_test.go`):

```bash
WADIST_POSTGRES_DSN='postgres://test:test@localhost:55432/app_test?sslmode=disable' \
WADIST_APP_TENANT_DSN='postgres://app_tenant:test@localhost:55432/app_test?sslmode=disable' \
WADIST_APP_SYSTEM_DSN='postgres://app_system:test@localhost:55432/app_test?sslmode=disable' \
WADIST_REDIS_ADDR=localhost:56379 \
WADIST_CONSOLE_SESSION_KEY='MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=' \
go run ./cmd/console &          # listens on its default addr (check console.LoadConfig for the port; likely :8080)
cd frontend && npm run dev &    # http://localhost:3000, NEXT_PUBLIC_API_BASE_URL defaults to localhost:8080
```

- [ ] **Step 3: Playwright smoke** (browser MCP tools). Script:
  1. Open `http://localhost:3000/login`, sign in `smokeadmin` / `smoke-admin-pass` → lands on `/admin`.
  2. Avatar menu → **退出登录** → assert URL is `/login` and `localStorage.wadist_token` is gone. *(This is the Component-1 verdict: if it fails, STOP and debug with superpowers:systematic-debugging before continuing.)*
  3. Log back in. Avatar menu → **修改密码**: wrong old password → toast 修改失败/旧密码错误, still logged in (NOT redirected — the 400-not-401 guarantee). Then correct old → new `smoke-admin-pass2` → success toast.
  4. Sidebar footer logout button → `/login`. Old password fails, new password logs in.
  5. Visit `/admin/settings` — toggle registration off, add `US / 0.05`, save; reload → values persist. Visit `/register` → attempt a signup → 「注册已关闭」 message shown.
  6. Visit `/admin/audit` — 登录日志 tab shows the login/failure/logout rows just generated (including the failed change attempt is NOT there — only auth events); 运行日志 tab shows live backend lines; 操作审计 tab has `settings.update` but no `auth.*` rows.
  7. Screenshot each page for the summary.

- [ ] **Step 4: Record the Component-1 verdict** in the final report AND as an addendum line in the spec's Component 1 section (fix committed if a real bug; "local E2E passes — production report attributed to the deploy-window restart" otherwise).

- [ ] **Step 5: Teardown + full test sweep**

```bash
kill %1 %2 2>/dev/null; docker rm -f sp9-pg sp9-redis
go build ./... && go test ./internal/api/ ./internal/console/ -count=1
cd frontend && npm run build && npx eslint .
```

Expected: all green; eslint shows only the pre-existing house-baseline warnings.

- [ ] **Step 6: Commit any smoke-driven fixes + spec addendum**

```bash
git add -A && git commit -m "test(sp9): runtime E2E smoke results + spec addendum"
```

---

## Post-merge deployment note (operator action, not part of the branch)

1. Apply migration 0018 to the live DB the same way 0017 was applied (`psql` against the compose postgres).
2. `make deploy` (never raw `docker compose up` — the two `-f` files rule).
3. Live smoke: log in as a low-stakes account, change its password, check 系统日志 tabs.
