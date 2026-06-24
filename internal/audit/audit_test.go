package audit_test

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/acme/wadist/internal/audit"
)

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

func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, ".down.sql") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
}

func buildAppTenantDSN(t *testing.T, superDSN string) string {
	t.Helper()
	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.User = url.UserPassword("app_tenant", "app_tenant_pw")
	return u.String()
}

func TestAuditWriter_RecordWritesRow(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	superDSN := testDSN(t)
	pool, err := pgxpool.New(ctx, superDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyMigrations(t, ctx, pool)

	aw := audit.NewAuditWriter(pool)
	e := audit.AuditEntry{
		TenantID:     1,
		ActorID:      99,
		Action:       "refund.approve",
		ResourceType: "refund_request",
		ResourceID:   42,
		Details:      []byte(`{"note":"test"}`),
	}
	if err := aw.Record(ctx, e); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var action, resType string
	var tenantID, actorID, resourceID int64
	err = pool.QueryRow(ctx, `
		SELECT tenant_id, actor_id, action, resource_type, resource_id
		FROM audit_log WHERE action='refund.approve'`).
		Scan(&tenantID, &actorID, &action, &resType, &resourceID)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if tenantID != 1 || actorID != 99 || action != "refund.approve" ||
		resType != "refund_request" || resourceID != 42 {
		t.Fatalf("unexpected row: tenant=%d actor=%d action=%s resType=%s resID=%d",
			tenantID, actorID, action, resType, resourceID)
	}
}

func TestAuditLog_AppendOnlyRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	superDSN := testDSN(t)
	superPool, err := pgxpool.New(ctx, superDSN)
	if err != nil {
		t.Fatalf("super pool: %v", err)
	}
	t.Cleanup(superPool.Close)
	applyMigrations(t, ctx, superPool)

	// Insert a row as superuser so we have something to try to UPDATE/DELETE.
	aw := audit.NewAuditWriter(superPool)
	if err := aw.Record(ctx, audit.AuditEntry{Action: "test.seed"}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	// Connect as app_tenant role.
	tenantDSN := buildAppTenantDSN(t, superDSN)
	tenantPool, err := pgxpool.New(ctx, tenantDSN)
	if err != nil {
		t.Fatalf("tenant pool: %v", err)
	}
	t.Cleanup(tenantPool.Close)

	// Self-verification: confirm the role is genuinely non-superuser so the
	// UPDATE/DELETE rejections below are real permission checks, not vacuous.
	var isSuperuser string
	if err := tenantPool.QueryRow(ctx, `SELECT current_setting('is_superuser')`).Scan(&isSuperuser); err != nil {
		t.Fatalf("check is_superuser: %v", err)
	}
	if isSuperuser != "off" {
		t.Fatalf("is_superuser=%q, want 'off' — role is unexpectedly superuser; permission checks would be vacuous", isSuperuser)
	}
	t.Logf("is_superuser=%q (confirmed non-superuser)", isSuperuser)

	t.Run("UPDATE_rejected", func(t *testing.T) {
		_, err := tenantPool.Exec(ctx, `UPDATE audit_log SET action='tampered'`)
		if err == nil {
			t.Fatal("expected UPDATE on audit_log to be rejected, but it succeeded")
		}
		t.Logf("UPDATE correctly rejected: %v", err)
	})

	t.Run("DELETE_rejected", func(t *testing.T) {
		_, err := tenantPool.Exec(ctx, `DELETE FROM audit_log`)
		if err == nil {
			t.Fatal("expected DELETE on audit_log to be rejected, but it succeeded")
		}
		t.Logf("DELETE correctly rejected: %v", err)
	})
}

func TestSuppressionList_DeleteRejectedForAppTenant(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	superDSN := testDSN(t)
	superPool, err := pgxpool.New(ctx, superDSN)
	if err != nil {
		t.Fatalf("super pool: %v", err)
	}
	t.Cleanup(superPool.Close)
	applyMigrations(t, ctx, superPool)

	// Seed a suppression row as superuser so there is something to try to DELETE.
	if _, err := superPool.Exec(ctx, `
		INSERT INTO suppression_list (tenant_id, phone_bidx, reason)
		VALUES (1, '\xdeadbeef', 'opted-out')`); err != nil {
		t.Fatalf("seed suppression_list: %v", err)
	}

	// Connect as app_tenant role.
	tenantDSN := buildAppTenantDSN(t, superDSN)
	tenantPool, err := pgxpool.New(ctx, tenantDSN)
	if err != nil {
		t.Fatalf("tenant pool: %v", err)
	}
	t.Cleanup(tenantPool.Close)

	// Self-verification: confirm the role is genuinely non-superuser.
	var isSuperuser string
	if err := tenantPool.QueryRow(ctx, `SELECT current_setting('is_superuser')`).Scan(&isSuperuser); err != nil {
		t.Fatalf("check is_superuser: %v", err)
	}
	if isSuperuser != "off" {
		t.Fatalf("is_superuser=%q, want 'off' — role is unexpectedly superuser; permission check would be vacuous", isSuperuser)
	}
	t.Logf("is_superuser=%q (confirmed non-superuser)", isSuperuser)

	// app_tenant must NOT be able to DELETE from suppression_list (compliance: opt-outs are permanent).
	_, err = tenantPool.Exec(ctx, `DELETE FROM suppression_list`)
	if err == nil {
		t.Fatal("expected DELETE on suppression_list to be rejected for app_tenant, but it succeeded")
	}
	t.Logf("DELETE on suppression_list correctly rejected: %v", err)

	// app_tenant must NOT be able to UPDATE suppression_list either.
	_, err = tenantPool.Exec(ctx, `UPDATE suppression_list SET reason='tampered'`)
	if err == nil {
		t.Fatal("expected UPDATE on suppression_list to be rejected for app_tenant, but it succeeded")
	}
	t.Logf("UPDATE on suppression_list correctly rejected: %v", err)

	// app_tenant CAN still SELECT and INSERT (non-destructive access is allowed).
	var count int
	if err := tenantPool.QueryRow(ctx, `SELECT count(*) FROM suppression_list`).Scan(&count); err != nil {
		t.Fatalf("SELECT on suppression_list should be allowed for app_tenant: %v", err)
	}
	t.Logf("SELECT on suppression_list allowed, count=%d", count)
}

func TestAuditWriter_RecordNilDetails(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	superDSN := testDSN(t)
	pool, err := pgxpool.New(ctx, superDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyMigrations(t, ctx, pool)

	aw := audit.NewAuditWriter(pool)
	// nil Details should not fail
	if err := aw.Record(ctx, audit.AuditEntry{Action: "test.nil_details"}); err != nil {
		t.Fatalf("Record with nil details: %v", err)
	}

	var count int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='test.nil_details'`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}
}
