// internal/store/node_test.go
package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// readMigration0007 reads the cluster_nodes migration.
func readMigration0007(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../migrations/0007_cluster_nodes.sql")
	if err != nil {
		t.Fatalf("read migration 0007: %v", err)
	}
	return string(b)
}

// applyMigrations0001And0007 applies 0001 then 0007 on the given pool.
func applyMigrations0001And0007(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, readMigration(t)); err != nil {
		t.Fatalf("apply migration 0001: %v", err)
	}
	if _, err := pool.Exec(ctx, readMigration0007(t)); err != nil {
		t.Fatalf("apply migration 0007: %v", err)
	}
}

func TestMigration0007_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	// Apply 0001 first (cluster_nodes references account_devices indirectly via touch_updated_at).
	if _, err := pool.Exec(ctx, readMigration(t)); err != nil {
		t.Fatalf("apply migration 0001: %v", err)
	}

	sql := readMigration0007(t)
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("apply migration 0007 #%d: %v", i+1, err)
		}
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='cluster_nodes' AND table_schema='public'`).
		Scan(&n); err != nil {
		t.Fatalf("verify cluster_nodes table: %v", err)
	}
	if n != 1 {
		t.Fatalf("cluster_nodes table count = %d, want 1", n)
	}
}

func TestUpsertNodeHeartbeat_InsertsAndUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)

	// Apply 0007 migration on top of the already-applied 0001 (newTestManager applies 0001).
	if _, err := m.bizPool.Exec(ctx, readMigration0007(t)); err != nil {
		t.Fatalf("apply migration 0007: %v", err)
	}

	const nodeID = "node-abc"

	// Insert via upsert.
	if err := m.UpsertNodeHeartbeat(ctx, nodeID); err != nil {
		t.Fatalf("first UpsertNodeHeartbeat: %v", err)
	}

	// Capture last_heartbeat_at after first upsert.
	var first time.Time
	if err := m.bizPool.QueryRow(ctx,
		`SELECT last_heartbeat_at FROM cluster_nodes WHERE node_id = $1`, nodeID).
		Scan(&first); err != nil {
		t.Fatalf("scan first heartbeat: %v", err)
	}

	// Wait briefly to ensure clock advance.
	time.Sleep(10 * time.Millisecond)

	// Upsert again — should advance last_heartbeat_at, still one row.
	if err := m.UpsertNodeHeartbeat(ctx, nodeID); err != nil {
		t.Fatalf("second UpsertNodeHeartbeat: %v", err)
	}

	var second time.Time
	var rowCount int
	if err := m.bizPool.QueryRow(ctx,
		`SELECT last_heartbeat_at FROM cluster_nodes WHERE node_id = $1`, nodeID).
		Scan(&second); err != nil {
		t.Fatalf("scan second heartbeat: %v", err)
	}
	if err := m.bizPool.QueryRow(ctx,
		`SELECT count(*) FROM cluster_nodes WHERE node_id = $1`, nodeID).
		Scan(&rowCount); err != nil {
		t.Fatalf("count rows: %v", err)
	}

	if !second.After(first) {
		t.Fatalf("expected second heartbeat (%v) to be after first (%v)", second, first)
	}
	if rowCount != 1 {
		t.Fatalf("expected 1 row for nodeID %q, got %d", nodeID, rowCount)
	}
}

func TestDeregisterNode_ClearsOwnerAndRow(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)

	// Apply 0007 migration.
	if _, err := m.bizPool.Exec(ctx, readMigration0007(t)); err != nil {
		t.Fatalf("apply migration 0007: %v", err)
	}

	const nodeID = "node-xyz"

	// Register the node.
	if err := m.UpsertNodeHeartbeat(ctx, nodeID); err != nil {
		t.Fatalf("UpsertNodeHeartbeat: %v", err)
	}

	// Seed an account device owned by this node.
	seedAccountDevice(t, ctx, m, "jid-deregister-test")
	if _, err := m.bizPool.Exec(ctx,
		`UPDATE account_devices SET owner_node = $1 WHERE account_jid = $2`,
		nodeID, "jid-deregister-test"); err != nil {
		t.Fatalf("set owner_node: %v", err)
	}

	// Deregister the node.
	if err := m.DeregisterNode(ctx, nodeID); err != nil {
		t.Fatalf("DeregisterNode: %v", err)
	}

	// owner_node on account_devices should be cleared.
	var ownerNode *string
	if err := m.bizPool.QueryRow(ctx,
		`SELECT owner_node FROM account_devices WHERE account_jid = $1`, "jid-deregister-test").
		Scan(&ownerNode); err != nil {
		t.Fatalf("scan owner_node: %v", err)
	}
	if ownerNode != nil {
		t.Fatalf("expected owner_node to be NULL after DeregisterNode, got %q", *ownerNode)
	}

	// cluster_nodes row should be deleted.
	var nodeCount int
	if err := m.bizPool.QueryRow(ctx,
		`SELECT count(*) FROM cluster_nodes WHERE node_id = $1`, nodeID).
		Scan(&nodeCount); err != nil {
		t.Fatalf("count cluster_nodes: %v", err)
	}
	if nodeCount != 0 {
		t.Fatalf("expected cluster_nodes row deleted, got %d rows", nodeCount)
	}
}
