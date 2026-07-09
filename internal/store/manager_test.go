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

	m, err := Init(ctx, Config{DSN: dsn, Redis: newTestRedis(t), BadgerDir: t.TempDir()}, waLog.Noop)
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

// TestManager_DistinctBadgerDir_NoCollision verifies that two Managers
// pointed at the SAME Postgres+Redis but DIFFERENT BadgerDir paths can both
// be opened successfully. This is the exact scenario cmd/console/main.go's
// redis-injection + distinct-BadgerDir fix defends against: the console's
// Manager (its Badger session store is unused — the console never sends
// WhatsApp messages) must not dir-lock-collide with a co-located
// cmd/wadist send node's Manager, and store.Init's hard "redis is required"
// check must be satisfied once Redis is injected (mirrors cmd/console's
// run() building store.Config with .Redis set before calling store.Init).
func TestManager_DistinctBadgerDir_NoCollision(t *testing.T) {
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

	rdb := newTestRedis(t)

	// "send node"-like Manager.
	m1, err := newManager(ctx, Config{DSN: dsn, Redis: rdb, BadgerDir: t.TempDir()}, waLog.Noop)
	if err != nil {
		t.Fatalf("manager 1 (send-node-like): %v", err)
	}
	defer m1.Close()

	// "console"-like Manager — distinct BadgerDir, same Postgres+Redis,
	// same shape as cmd/console/main.go's run() after the fix.
	m2, err := newManager(ctx, Config{DSN: dsn, Redis: rdb, BadgerDir: t.TempDir()}, waLog.Noop)
	if err != nil {
		t.Fatalf("manager 2 (console-like): %v", err)
	}
	defer m2.Close()

	// Sanity: both are independently usable badger-backed containers — no
	// dir-lock error was swallowed.
	if dev := m1.container.NewDevice(); dev == nil {
		t.Fatal("manager 1 NewDevice returned nil")
	}
	if dev := m2.container.NewDevice(); dev == nil {
		t.Fatal("manager 2 NewDevice returned nil")
	}
}
