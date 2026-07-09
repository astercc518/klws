// internal/store/testsupport_test.go
package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// testDSN 起一次性 Postgres,返回 DSN;测试结束自动销毁。
func testDSN(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("app_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	return dsn
}

// readMigration 读取 0001 迁移内容。
func readMigration(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../migrations/0001_account_devices.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	return string(b)
}

// newTestManager constructs an isolated Manager backed by a fresh Postgres
// container, applies the 0001 account_devices migration, and closes when done.
// No Redis client is configured — only usable by tests that don't exercise
// the ownership delegators (AcquireDeviceLock/UpsertNodeHeartbeat/etc.).
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

// seedAccountDevice inserts a minimal account_devices row for tests.
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
