package api

import (
	"context"
	"testing"
)

func TestContactsSchemaApplies(t *testing.T) {
	pool := testPool(t) // applies all migrations incl. 0021
	ctx := context.Background()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_name IN ('contacts','contact_tags','contact_tag_map','contact_segments','contact_import_batches')`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 5 {
		t.Fatalf("want 5 contact tables, got %d", n)
	}
}
