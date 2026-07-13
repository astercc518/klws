package api

import (
	"context"
	"testing"
)

func TestAgentSchemaApplies(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var tabs, cols int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_name IN ('agent_cost_pricing','agent_allocations','agent_settlements')`).Scan(&tabs); err != nil {
		t.Fatal(err)
	}
	if tabs != 3 { t.Fatalf("want 3 agent tables, got %d", tabs) }
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_name='console_users' AND column_name IN ('parent_id','credit_limit')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 2 { t.Fatalf("want parent_id+credit_limit, got %d", cols) }
}
