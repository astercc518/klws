// internal/api/auth_db_test.go — DB+Redis-backed tests for the auth handlers,
// specifically that a plain USERNAME (no "@") identifier is accepted by the
// login binding (SP6 task 1: relax email-only binding on login/register/
// forgot-password so existing email accounts keep working unchanged while a
// username also becomes a valid identifier).
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/redis/go-redis/v9"

	"github.com/acme/wadist/internal/console"
)

// newTestRedis starts a throwaway Redis container (same pattern as
// internal/console/session_test.go) for tests that need a real SessionStore.
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	ctx := context.Background()
	c, err := tcredis.Run(ctx, "redis:7")
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	uri, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis uri: %v", err)
	}
	opt, err := redis.ParseURL(uri)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	rdb := redis.NewClient(opt)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// newAuthServer wires a Server with a real UserRepo (Postgres) and SessionStore
// (Redis) — the full stack handleLogin needs, unlike the lighter newCrudServer.
func newAuthServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	pool := testPool(t)
	rdb := newTestRedis(t)
	deps := Deps{
		Users:      console.NewUserRepo(pool),
		Sessions:   console.NewSessionStore(rdb, time.Hour),
		SessionKey: []byte("0123456789abcdef0123456789abcdef"),
	}
	return &Server{sysPool: pool, deps: deps}, context.Background()
}

func doLogin(s *Server, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	s.handleLogin(c)
	return w
}

// TestLoginAcceptsUsernameIdentifier proves the login binding no longer
// requires an "email"-shaped identifier: a bare username like "admin" (no @)
// must pass gin's binding validation and reach the handler/DB lookup. Seeding
// a real console_users row with username "admin" and the matching password
// lets us assert a full 200 end-to-end, which is the strongest proof the
// relaxed binding didn't break real authentication.
func TestLoginAcceptsUsernameIdentifier(t *testing.T) {
	s, ctx := newAuthServer(t)

	hash, err := console.HashPassword("s3cret!")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := s.deps.Users.Create(ctx, "admin", hash, console.RoleAdmin, nil); err != nil {
		t.Fatalf("seed username-only user: %v", err)
	}

	// Correct username + password → 200, proving the binding change didn't
	// merely stop rejecting usernames but also that auth still works end-to-end.
	w := doLogin(s, `{"email":"admin","password":"s3cret!"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("username login: want 200, got %d body %s", w.Code, w.Body.String())
	}

	// Wrong password with a username identifier → 401 (not 400): confirms the
	// request reached Authenticate rather than being rejected by binding.
	w = doLogin(s, `{"email":"admin","password":"wrong"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("username login wrong pw: want 401, got %d body %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "invalid request body") {
		t.Fatalf("username identifier was rejected by binding, not by Authenticate: %s", w.Body.String())
	}

	// A never-registered username also must not be blocked by binding (still
	// a plain 401, no account enumeration difference from a real user + wrong pw).
	w = doLogin(s, `{"email":"nobody","password":"x"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown username login: want 401, got %d body %s", w.Code, w.Body.String())
	}

	// Existing email-shaped identifiers keep working unchanged.
	hash2, _ := console.HashPassword("emailpw1")
	if _, err := s.deps.Users.Create(ctx, "admin@acme.test", hash2, console.RoleAdmin, nil); err != nil {
		t.Fatalf("seed email user: %v", err)
	}
	w = doLogin(s, `{"email":"admin@acme.test","password":"emailpw1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("email login: want 200, got %d body %s", w.Code, w.Body.String())
	}
}
