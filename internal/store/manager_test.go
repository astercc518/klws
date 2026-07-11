// internal/store/manager_test.go
package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	wlog "github.com/acme/wadist/internal/log"
)

// TestManager_Init verifies the singleton Manager boots against a real
// Postgres+Redis. The former whatsmeow/badger session store has been removed
// (Evolution owns the WhatsApp data plane), so no device container is opened.
func TestManager_Init(t *testing.T) {
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

	m, err := Init(ctx, Config{DSN: dsn, Redis: newTestRedis(t)}, wlog.Noop)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer m.Close()

	// The whatsmeow_device PG table must NOT exist: it was only ever created by
	// the removed session store's migrations.
	var n int
	if err := m.BizPool().QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='whatsmeow_device'`).
		Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Fatalf("whatsmeow_device table count = %d, want 0 (no session store)", n)
	}

	// Business pool is live and usable.
	if err := m.BizPool().Ping(ctx); err != nil {
		t.Fatalf("biz pool ping: %v", err)
	}
}

// TestManager_MultiInstance_NoCollision verifies that two Managers pointed at
// the SAME Postgres+Redis can both be opened successfully. This is the scenario
// cmd/console/main.go's redis-injection fix defends against: the console's
// Manager must not collide with a co-located cmd/wadist send node's Manager,
// and store.Init's hard "redis is required" check must be satisfied.
func TestManager_MultiInstance_NoCollision(t *testing.T) {
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

	m1, err := newManager(ctx, Config{DSN: dsn, Redis: rdb}, wlog.Noop)
	if err != nil {
		t.Fatalf("manager 1 (send-node-like): %v", err)
	}
	defer m1.Close()

	m2, err := newManager(ctx, Config{DSN: dsn, Redis: rdb}, wlog.Noop)
	if err != nil {
		t.Fatalf("manager 2 (console-like): %v", err)
	}
	defer m2.Close()

	if err := m1.BizPool().Ping(ctx); err != nil {
		t.Fatalf("manager 1 ping: %v", err)
	}
	if err := m2.BizPool().Ping(ctx); err != nil {
		t.Fatalf("manager 2 ping: %v", err)
	}
}
