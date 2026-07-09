// internal/store/manager_test.go
package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestManager_Init_BadgerOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	dsn := testDSN(t)

	migPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("migration pool: %v", err)
	}
	applyMigrations(t, ctx, migPool)
	migPool.Close()

	m, err := Init(ctx, Config{DSN: dsn, Redis: newTestRedis(t)}, waLog.Noop)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer m.Close()

	// badger-only: sqlstore.Upgrade no longer runs, so the whatsmeow_device PG
	// table must NOT be created.
	var n int
	if err := m.BizPool().QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='whatsmeow_device'`).
		Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Fatalf("whatsmeow_device table count = %d, want 0 (badger-only backend)", n)
	}

	// container must be a working badger-backed device container.
	if dev := m.container.NewDevice(); dev == nil {
		t.Fatal("NewDevice returned nil")
	}
}
