// internal/store/lock_test.go
package store

import (
	"context"
	"errors"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// newTestManager constructs an isolated Manager backed by a fresh Postgres
// container, applies the 0001 account_devices migration, and closes when done.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	ctx := context.Background()
	m, err := newManager(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	if err != nil {
		t.Fatalf("newTestManager: %v", err)
	}
	t.Cleanup(m.Close)
	// Apply the application migration (account_devices) so seedAccountDevice works.
	if _, err := m.bizPool.Exec(ctx, readMigration(t)); err != nil {
		t.Fatalf("newTestManager apply migration: %v", err)
	}
	return m
}

// seedAccountDevice inserts a minimal account_devices row so that
// AcquireDeviceLock has a real row to reference (closer to production).
func seedAccountDevice(t *testing.T, ctx context.Context, m *Manager, jid string) {
	t.Helper()
	_, err := m.bizPool.Exec(ctx,
		`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status)
		 VALUES (1, $1, '+100', 'active')
		 ON CONFLICT (account_jid) DO NOTHING`,
		jid,
	)
	if err != nil {
		t.Fatalf("seedAccountDevice(%q): %v", jid, err)
	}
}

func TestDeviceLock_Healthy_OwnershipValid(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t)
	ctx := context.Background()
	seedAccountDevice(t, ctx, m, "jid-h1")

	lock, err := m.AcquireDeviceLock(ctx, "jid-h1")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release(ctx)
	if !lock.Healthy(ctx) {
		t.Fatal("freshly-acquired lock must be Healthy (owns advisory lock)")
	}
}

func TestDeviceLock_Healthy_FalseAfterBackendTerminated(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t)
	ctx := context.Background()
	seedAccountDevice(t, ctx, m, "jid-h2")

	lock, err := m.AcquireDeviceLock(ctx, "jid-h2")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release(ctx)
	// Capture the backend PID of the pinned lock connection (same package → can read unexported field).
	pid := lock.conn.Conn().PgConn().PID()
	// Terminate that backend from a different connection.
	if _, err := m.bizPool.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	if lock.Healthy(ctx) {
		t.Fatal("Healthy must be false after the lock's backend was terminated")
	}
}

func TestDeviceLock_Healthy_FalseAfterRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t)
	ctx := context.Background()
	seedAccountDevice(t, ctx, m, "jid-h3")
	lock, err := m.AcquireDeviceLock(ctx, "jid-h3")
	if err != nil {
		t.Fatal(err)
	}
	lock.Release(ctx)
	if lock.Healthy(ctx) {
		t.Fatal("Healthy must be false after Release (conn nil)")
	}
}

func TestDeviceLock_Healthy_FalseAfterLockReleased_ConnStillAlive(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t)
	ctx := context.Background()
	seedAccountDevice(t, ctx, m, "jid-h4")
	lock, err := m.AcquireDeviceLock(ctx, "jid-h4")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release(ctx)
	// Manually release the advisory lock on the pinned conn, keeping the conn alive.
	if _, err := lock.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lock.key); err != nil {
		t.Fatal(err)
	}
	// Conn is alive (Ping would pass) but the lock is gone — Healthy must detect this.
	if lock.Healthy(ctx) {
		t.Fatal("Healthy must be false: conn alive but advisory lock no longer held")
	}
}

func TestAcquireDeviceLock_MutualExclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, err := newManager(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	defer m.Close()

	const jid = "1234567890.0:0@s.whatsapp.net"

	l1, err := m.AcquireDeviceLock(ctx, jid)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !l1.Healthy(ctx) {
		t.Fatal("lock should be healthy while held")
	}

	// 第二次抢同一 jid:不同连接 → 抢不到
	if _, err := m.AcquireDeviceLock(ctx, jid); !errors.Is(err, ErrDeviceLocked) {
		t.Fatalf("second acquire err = %v, want ErrDeviceLocked", err)
	}

	// 释放后可再抢
	l1.Release(ctx)
	l2, err := m.AcquireDeviceLock(ctx, jid)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	l2.Release(ctx)
}
