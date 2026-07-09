// internal/pricing/testsupport_test.go
package pricing

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	walog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/store"
)

// newTestRedis starts a throwaway Redis 7 container for tests that need the
// store.Manager's proxy allocator (redis is a hard requirement of NewManager).
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

// newTestPool starts a throwaway PG16, applies every migration BEFORE
// constructing the Manager (newManager's boot-time redis proxy-index rebuild
// queries proxy_pool immediately, so that table must already exist), and
// returns a non-singleton store.Manager wired to it.
func newTestPool(t *testing.T) *store.Manager {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("pricing_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}

	migPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("migration pool: %v", err)
	}
	files, _ := filepath.Glob("../../migrations/*.sql")
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, ".down.sql") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := migPool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	migPool.Close()

	logger, flush, err := walog.Production()
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(flush)
	mgr, err := store.NewManager(ctx, store.Config{DSN: dsn, NodeID: "pricing-test", Redis: newTestRedis(t), BadgerDir: t.TempDir()}, logger)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(mgr.Close)
	return mgr
}

// seedTenant inserts a tenant and returns its id (tenant_pricing has an FK to tenants).
func seedTenant(t *testing.T, ctx context.Context, mgr *store.Manager, name string) int64 {
	t.Helper()
	var id int64
	if err := mgr.SystemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return id
}
