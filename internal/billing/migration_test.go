package billing

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigration0003_Idempotent(t *testing.T) {
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

	for _, tbl := range []string{"tenant_wallets", "billing_charges", "wallet_ledger", "refund_requests"} {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.tables WHERE table_name=$1 AND table_schema='public'`, tbl).
			Scan(&n); err != nil {
			t.Fatalf("verify %s: %v", tbl, err)
		}
		if n != 1 {
			t.Fatalf("table %s count = %d, want 1", tbl, n)
		}
	}
}
