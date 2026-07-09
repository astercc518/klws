// internal/store/node_test.go
package store

import (
	"context"
	"errors"
	"os"
	"testing"

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

func TestListActiveAccounts(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t)
	ctx := context.Background()
	if _, err := m.bizPool.Exec(ctx, readMigration0007(t)); err != nil {
		t.Fatalf("apply migration 0007: %v", err)
	}
	seedAccountDevice(t, ctx, m, "jid-a1")
	seedAccountDevice(t, ctx, m, "jid-a2")
	m.bizPool.Exec(ctx, `UPDATE account_devices SET ban_status='banned' WHERE account_jid='jid-a2'`)
	jids, err := m.ListActiveAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 只含 active
	found := map[string]bool{}
	for _, j := range jids {
		found[j] = true
	}
	if !found["jid-a1"] || found["jid-a2"] {
		t.Fatalf("active filter wrong: %v", jids)
	}
}

func TestClaimAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t)
	ctx := context.Background()
	if _, err := m.bizPool.Exec(ctx, readMigration0007(t)); err != nil {
		t.Fatalf("apply migration 0007: %v", err)
	}
	seedAccountDevice(t, ctx, m, "jid-c1")
	if err := m.ClaimAccount(ctx, "jid-c1", "node-x"); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := m.bizPool.QueryRow(ctx, `SELECT owner_node FROM account_devices WHERE account_jid='jid-c1'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "node-x" {
		t.Fatalf("owner_node=%q", owner)
	}
}

func TestGetBoundProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)
	// Apply all migrations so proxy_pool table exists.
	if _, err := m.bizPool.Exec(ctx, readMigration0007(t)); err != nil {
		t.Fatalf("apply migration 0007: %v", err)
	}
	applyMigrations(t, ctx, m.bizPool)

	// Seed account and bind proxy via direct UPDATE (proxy_pool insert + update).
	seedAccountDevice(t, ctx, m, "jid-proxy-test")

	proxyURL := "socks5://proxy.example.com:1080"
	// Insert a proxy and bind it directly.
	var proxyID int64
	err := m.bizPool.QueryRow(ctx,
		`INSERT INTO proxy_pool (proxy_url, proxy_type, country_code, max_bindings)
		 VALUES ($1, 'socks5', 'US', 10) RETURNING id`, proxyURL).Scan(&proxyID)
	if err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	if _, err := m.bizPool.Exec(ctx,
		`UPDATE account_devices SET proxy_id=$1, proxy_url_cache=$2 WHERE account_jid='jid-proxy-test'`,
		proxyID, proxyURL); err != nil {
		t.Fatalf("bind proxy: %v", err)
	}

	// GetBoundProxy should return the binding with ProxyURL set.
	binding, err := m.GetBoundProxy(ctx, "jid-proxy-test")
	if err != nil {
		t.Fatalf("GetBoundProxy: %v", err)
	}
	if binding.ProxyURL != proxyURL {
		t.Fatalf("ProxyURL=%q, want %q", binding.ProxyURL, proxyURL)
	}
	if binding.ProxyID != proxyID {
		t.Fatalf("ProxyID=%d, want %d", binding.ProxyID, proxyID)
	}

	// NULL case: seed account without proxy.
	seedAccountDevice(t, ctx, m, "jid-no-proxy")
	_, err = m.GetBoundProxy(ctx, "jid-no-proxy")
	if !errors.Is(err, ErrProxyNotBound) {
		t.Fatalf("expected ErrProxyNotBound, got %v", err)
	}
}

func TestMarkAccountLoggedOut_ExcludedFromActive(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t)
	ctx := context.Background()
	if _, err := m.bizPool.Exec(ctx, readMigration0007(t)); err != nil {
		t.Fatalf("apply migration 0007: %v", err)
	}
	seedAccountDevice(t, ctx, m, "jid-keep")
	seedAccountDevice(t, ctx, m, "jid-gone")

	before, err := m.ListActiveAccounts(ctx)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}
	foundBefore := map[string]bool{}
	for _, j := range before {
		foundBefore[j] = true
	}
	if !foundBefore["jid-keep"] || !foundBefore["jid-gone"] {
		t.Fatalf("active before=%v; want both jid-keep and jid-gone", before)
	}

	if err := m.MarkAccountLoggedOut(ctx, "jid-gone"); err != nil {
		t.Fatalf("MarkAccountLoggedOut: %v", err)
	}

	after, err := m.ListActiveAccounts(ctx)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	found := map[string]bool{}
	for _, j := range after {
		found[j] = true
	}
	if !found["jid-keep"] || found["jid-gone"] {
		t.Fatalf("active filter wrong after MarkAccountLoggedOut: %v", after)
	}
}

