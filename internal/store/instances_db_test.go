// internal/store/instances_db_test.go
package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigration0020_AccountInstances_Idempotent(t *testing.T) {
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
	applyMigrations(t, ctx, pool) // pass 2 — must not error: proves replay-all idempotency

	var reg bool
	err = pool.QueryRow(ctx,
		`SELECT to_regclass('public.account_instances') IS NOT NULL`).Scan(&reg)
	if err != nil || !reg {
		t.Fatalf("account_instances not present after migrate-twice: reg=%v err=%v", reg, err)
	}
}
