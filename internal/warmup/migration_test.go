package warmup

import "testing"

func TestMigrationSeedsPolicies(t *testing.T) {
	pool, ctx := pgPool(t)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM warmup_policies`).Scan(&n); err != nil {
		t.Fatalf("query policies: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 seeded lanes, got %d", n)
	}
	var scripts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM warmup_scripts WHERE enabled`).Scan(&scripts); err != nil {
		t.Fatalf("query scripts: %v", err)
	}
	if scripts < 3 {
		t.Fatalf("want >=3 seeded scripts, got %d", scripts)
	}
}
