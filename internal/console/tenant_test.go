// internal/console/tenant_test.go
package console

import (
	"context"
	"testing"

	"github.com/acme/wadist/internal/store"
)

func TestWithTenantTxIsolatesRows(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)

	// Two tenants, one account_devices row each (RLS-enforced table).
	var tA, tB int64
	if err := mgr.SystemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('A') RETURNING id`).Scan(&tA); err != nil {
		t.Fatalf("tenant A: %v", err)
	}
	if err := mgr.SystemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('B') RETURNING id`).Scan(&tB); err != nil {
		t.Fatalf("tenant B: %v", err)
	}
	seedDevice(t, ctx, mgr, tA, "jidA@s.whatsapp.net")
	seedDevice(t, ctx, mgr, tB, "jidB@s.whatsapp.net")

	srv, _ := NewServer(testConfig(), nil, nil, mgr)

	// Customer of tenant A should see exactly one device (its own).
	cctx := context.WithValue(ctx, sessionCtxKey, &SessionData{UserID: 1, Role: RoleCustomer, TenantID: &tA})
	tx, err := srv.withTenantTx(cctx)
	if err != nil {
		t.Fatalf("withTenantTx: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_devices`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("RLS leak: customer A saw %d devices, want 1", count)
	}

	// A non-customer session must be rejected (no tenant scope).
	actx := context.WithValue(ctx, sessionCtxKey, &SessionData{UserID: 2, Role: RoleAdmin})
	if _, err := srv.withTenantTx(actx); err == nil {
		t.Fatalf("admin session must not get a tenant tx")
	}
}

// TestAppTenantCannotReadConsoleUsers verifies that the 0010 migration's
// REVOKE ALL ON tenants, console_users FROM app_tenant actually denies access.
func TestAppTenantCannotReadConsoleUsers(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)

	tx, err := mgr.TenantPool().Begin(ctx)
	if err != nil {
		t.Fatalf("begin tenant tx: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Set a tenant so the RLS policy doesn't interfere before we hit the REVOKE.
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant_id', '1', true)`); err != nil {
		t.Fatalf("set_config: %v", err)
	}

	var n int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM console_users`).Scan(&n)
	if err == nil {
		t.Fatalf("app_tenant role must NOT be able to SELECT from console_users (got %d rows)", n)
	}
	t.Logf("app_tenant correctly denied access to console_users: %v", err)
}

func seedDevice(t *testing.T, ctx context.Context, mgr *store.Manager, tenantID int64, jid string) {
	t.Helper()
	_, err := mgr.SystemPool().Exec(ctx,
		`INSERT INTO account_devices (tenant_id, account_jid, phone_number) VALUES ($1, $2, $3)`,
		tenantID, jid, "+1000000000")
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
}
