// internal/store/rls_test.go
package store

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// applyAllMigrationsRLS reads and applies all migration files in lexicographic order.
func applyAllMigrationsRLS(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
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

// buildAppTenantDSN takes the superuser DSN and returns a DSN for app_tenant role.
func buildAppTenantDSN(t *testing.T, superDSN string) string {
	t.Helper()
	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.User = url.UserPassword("app_tenant", "app_tenant_pw")
	return u.String()
}

func TestRLS_TenantIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()

	// Spin up a superuser pool via testcontainers.
	superDSN := testDSN(t)
	superPool, err := pgxpool.New(ctx, superDSN)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	t.Cleanup(superPool.Close)

	// Apply all migrations (creates roles + RLS policies).
	applyAllMigrationsRLS(t, ctx, superPool)

	// Seed two tenants' account_devices via superuser (bypasses RLS as superuser).
	for _, row := range []struct {
		tenantID int64
		jid      string
		phone    string
	}{
		{1, "1111@s.whatsapp.net", "+1111111111"},
		{1, "1112@s.whatsapp.net", "+1111111112"},
		{2, "2221@s.whatsapp.net", "+2221111111"},
	} {
		_, err := superPool.Exec(ctx,
			`INSERT INTO account_devices (tenant_id, account_jid, phone_number) VALUES ($1,$2,$3)`,
			row.tenantID, row.jid, row.phone)
		if err != nil {
			t.Fatalf("seed tenant %d jid %s: %v", row.tenantID, row.jid, err)
		}
	}

	// Build app_tenant pool (role subject to RLS).
	tenantDSN := buildAppTenantDSN(t, superDSN)
	tenantPool, err := pgxpool.New(ctx, tenantDSN)
	if err != nil {
		t.Fatalf("app_tenant pool: %v", err)
	}
	t.Cleanup(tenantPool.Close)

	t.Run("tenant1_sees_only_own_rows", func(t *testing.T) {
		tx, err := tenantPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck

		if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant_id', '1', true)`); err != nil {
			t.Fatalf("set_config: %v", err)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_devices`).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != 2 {
			t.Errorf("tenant1 count = %d, want 2", count)
		}
	})

	t.Run("tenant2_sees_only_own_rows", func(t *testing.T) {
		tx, err := tenantPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck

		if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant_id', '2', true)`); err != nil {
			t.Fatalf("set_config: %v", err)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_devices`).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != 1 {
			t.Errorf("tenant2 count = %d, want 1", count)
		}
	})

	t.Run("no_tenant_setting_sees_zero_rows", func(t *testing.T) {
		tx, err := tenantPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck

		// No set_config call — current_setting('app.current_tenant_id', true) returns ''
		// and ::bigint cast raises error OR returns 0 rows via USING policy.
		var count int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM account_devices`).Scan(&count)
		// Accept either: error (RLS cast failure) or 0 rows (policy blocks all).
		if err != nil {
			t.Logf("no-tenant-setting: query error (RLS blocked): %v", err)
			return
		}
		if count != 0 {
			t.Errorf("no-tenant count = %d, want 0", count)
		}
	})

	t.Run("with_check_rejects_cross_tenant_insert", func(t *testing.T) {
		tx, err := tenantPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck

		// Set current tenant to 1, then try to INSERT a row for tenant 2.
		if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant_id', '1', true)`); err != nil {
			t.Fatalf("set_config: %v", err)
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO account_devices (tenant_id, account_jid, phone_number) VALUES ($1,$2,$3)`,
			int64(2), "cross@s.whatsapp.net", "+9999999999")
		if err == nil {
			t.Error("cross-tenant insert should have been rejected by WITH CHECK, but succeeded")
		} else {
			t.Logf("WITH CHECK correctly rejected cross-tenant insert: %v", err)
		}
	})

	// Verify that Manager.WithTenant works correctly.
	t.Run("manager_WithTenant_isolation", func(t *testing.T) {
		// Construct a Manager with the pools already built.
		m := &Manager{
			tenantPool: tenantPool,
			systemPool: superPool,
			bizPool:    superPool,
		}

		// WithTenant for tenant 1 — should only see 2 rows.
		tx, err := m.WithTenant(ctx, 1)
		if err != nil {
			t.Fatalf("WithTenant(1): %v", err)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_devices`).Scan(&count); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("count via WithTenant tx: %v", err)
		}
		_ = tx.Rollback(ctx)
		if count != 2 {
			t.Errorf("WithTenant(1) count = %d, want 2", count)
		}

		// WithTenant for tenant 2 — should only see 1 row.
		tx2, err := m.WithTenant(ctx, 2)
		if err != nil {
			t.Fatalf("WithTenant(2): %v", err)
		}
		if err := tx2.QueryRow(ctx, `SELECT count(*) FROM account_devices`).Scan(&count); err != nil {
			_ = tx2.Rollback(ctx)
			t.Fatalf("count via WithTenant(2) tx: %v", err)
		}
		_ = tx2.Rollback(ctx)
		if count != 1 {
			t.Errorf("WithTenant(2) count = %d, want 1", count)
		}
	})
}
