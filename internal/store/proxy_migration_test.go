// internal/store/proxy_migration_test.go
package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrations_AllIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	applyMigrations(t, ctx, pool) // pass 1
	applyMigrations(t, ctx, pool) // pass 2 — must not error

	// proxy_pool exists and account_devices.proxy_id column exists
	var nTab, nCol int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='proxy_pool' AND table_schema='public'`).
		Scan(&nTab); err != nil {
		t.Fatalf("verify table: %v", err)
	}
	if nTab != 1 {
		t.Fatalf("proxy_pool table count = %d, want 1", nTab)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_name='account_devices' AND column_name='proxy_id' AND table_schema='public'`).
		Scan(&nCol); err != nil {
		t.Fatalf("verify column: %v", err)
	}
	if nCol != 1 {
		t.Fatalf("account_devices.proxy_id column count = %d, want 1", nCol)
	}
}
