package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// applyAllMigrationsSupp reads and applies all up-migrations.
func applyAllMigrationsSupp(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	applyAllMigrationsRLS(t, ctx, pool) // reuse from rls_test.go
}

func newManagerForSuppression(t *testing.T) (*Manager, *pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	superDSN := testDSN(t)
	pool, err := pgxpool.New(ctx, superDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyAllMigrationsSupp(t, ctx, pool)
	m := &Manager{bizPool: pool}
	return m, pool, ctx
}

func TestAddSuppression_InsertsRow(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, pool, ctx := newManagerForSuppression(t)

	key := make([]byte, 32)
	for i := range key {
		key[i] = 0xAB
	}

	if err := m.AddSuppression(ctx, key, 1, "+15550001111", "test reason"); err != nil {
		t.Fatalf("AddSuppression: %v", err)
	}

	var count int
	pool.QueryRow(ctx, `SELECT count(*) FROM suppression_list WHERE tenant_id=1`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 row in suppression_list, got %d", count)
	}
}

func TestAddSuppression_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, pool, ctx := newManagerForSuppression(t)

	key := make([]byte, 32)

	if err := m.AddSuppression(ctx, key, 1, "+15550001111", "reason1"); err != nil {
		t.Fatalf("first AddSuppression: %v", err)
	}
	// Re-add same phone — must not error (ON CONFLICT DO NOTHING)
	if err := m.AddSuppression(ctx, key, 1, "+15550001111", "reason2"); err != nil {
		t.Fatalf("second AddSuppression (idempotent): %v", err)
	}

	var count int
	pool.QueryRow(ctx, `SELECT count(*) FROM suppression_list WHERE tenant_id=1`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 row after duplicate insert, got %d", count)
	}
}

func TestAddSuppression_DifferentPhoneNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, pool, ctx := newManagerForSuppression(t)

	key := make([]byte, 32)

	if err := m.AddSuppression(ctx, key, 1, "+15550001111", "suppressed"); err != nil {
		t.Fatalf("AddSuppression: %v", err)
	}

	// A different phone should not appear in the suppression list.
	var count int
	pool.QueryRow(ctx, `
		SELECT count(*) FROM suppression_list WHERE tenant_id=1
		AND phone_bidx = $1`, []byte("notthesame")).Scan(&count)
	if count != 0 {
		t.Fatalf("different phone should not be in suppression_list, got %d rows", count)
	}
}

func TestAddSuppression_InvalidKeyLength(t *testing.T) {
	m := &Manager{bizPool: nil}
	ctx := context.Background()
	key := make([]byte, 16) // wrong size
	err := m.AddSuppression(ctx, key, 1, "+15550001111", "")
	if err == nil {
		t.Fatal("expected error for blindKey len != 32")
	}
}
