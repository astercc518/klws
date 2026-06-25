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

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	walog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/store"
)

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
	logger, flush, err := walog.Production()
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(flush)
	mgr, err := store.NewManager(ctx, store.Config{DSN: dsn, NodeID: "pricing-test"}, logger)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(mgr.Close)
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
		if _, err := mgr.SystemPool().Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
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
