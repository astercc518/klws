package dispatch

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigration0006_Idempotent(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	pool, _ := pgPool(t)            // applyMigrations ran once
	applyMigrations(t, ctx, pool)   // twice
	for _, tbl := range []string{"campaigns", "campaign_recipients", "campaign_templates", "media_uploads"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name=$1 AND table_schema='public'`, tbl).Scan(&n); err != nil {
			t.Fatalf("verify %s: %v", tbl, err)
		}
		if n != 1 { t.Fatalf("table %s count=%d", tbl, n) }
	}
	var col int
	pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name='account_devices' AND column_name='sent_today'`).Scan(&col)
	if col != 1 { t.Fatalf("sent_today col=%d", col) }
	_ = pgxpool.Pool{}
}
