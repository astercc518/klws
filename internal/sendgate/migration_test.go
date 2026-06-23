// internal/sendgate/migration_test.go
package sendgate

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigration0005_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)        // applyMigrations already ran once inside pgPool
	applyMigrations(t, ctx, pool) // apply again — must not error

	// columns exist
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_name='account_devices' AND column_name='health_score' AND table_schema='public'`).
		Scan(&n); err != nil {
		t.Fatalf("verify col: %v", err)
	}
	if n != 1 {
		t.Fatalf("health_score column count = %d, want 1", n)
	}
}

func TestEffectiveQuotaSQL_MatchesCurve(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	cases := []struct {
		ageDays, health, want int
	}{
		{0, 100, 20}, {3, 100, 50}, {5, 100, 100}, {10, 100, 250}, {30, 100, 1000},
		{30, 70, 700}, {0, 0, 1}, // GREATEST(1, ...) floor
	}
	for _, c := range cases {
		var got int
		reg := time.Now().Add(-time.Duration(c.ageDays) * 24 * time.Hour)
		if err := pool.QueryRow(ctx, `SELECT effective_quota($1, $2)`, reg, c.health).Scan(&got); err != nil {
			t.Fatalf("effective_quota(%dd,%d): %v", c.ageDays, c.health, err)
		}
		if got != c.want {
			t.Fatalf("effective_quota(%dd,%d) = %d, want %d", c.ageDays, c.health, got, c.want)
		}
	}
}

// compile-check: ensure pgxpool and context imports are used (migration_test uses them via pgPool).
var _ *pgxpool.Pool
var _ context.Context
