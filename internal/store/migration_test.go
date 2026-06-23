// internal/store/migration_test.go
package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigration_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	sql := readMigration(t)
	for i := 0; i < 2; i++ { // 连续两次,第二次不得报错
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("apply migration #%d: %v", i+1, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='account_devices' AND table_schema='public'`).
		Scan(&n); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if n != 1 {
		t.Fatalf("account_devices table count = %d, want 1", n)
	}
}
