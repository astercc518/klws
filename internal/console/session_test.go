// internal/console/session_test.go
package console

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

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

func TestSessionStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewSessionStore(newTestRedis(t), time.Hour)

	sid, err := store.Create(ctx, SessionData{UserID: 42, Role: RoleCustomer, TenantID: ptrInt64(7)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.Get(ctx, sid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserID != 42 || got.Role != RoleCustomer || got.TenantID == nil || *got.TenantID != 7 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if err := store.Destroy(ctx, sid); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := store.Get(ctx, sid); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after destroy want ErrNoSession, got %v", err)
	}
}

func TestSignVerifyCookie(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	signed := SignCookie(secret, "session-id-123")
	sid, ok := VerifyCookie(secret, signed)
	if !ok || sid != "session-id-123" {
		t.Fatalf("verify failed: sid=%q ok=%v", sid, ok)
	}
	if _, ok := VerifyCookie(secret, signed+"tamper"); ok {
		t.Fatalf("tampered cookie must fail")
	}
	if _, ok := VerifyCookie([]byte("wrong-secret-wrong-secret-32byte"), signed); ok {
		t.Fatalf("wrong secret must fail")
	}
}
