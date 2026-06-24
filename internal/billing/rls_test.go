// internal/billing/rls_test.go
package billing

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// buildAppTenantDSNBilling swaps user/password in the superuser DSN for app_tenant.
func buildAppTenantDSNBilling(t *testing.T, superDSN string) string {
	t.Helper()
	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.User = url.UserPassword("app_tenant", "app_tenant_pw")
	return u.String()
}

// poolTxBeginner implements TenantTxBeginner using a pgxpool, mirroring
// store.Manager.WithTenant logic without importing the store package.
type poolTxBeginner struct{ pool *pgxpool.Pool }

func (p *poolTxBeginner) WithTenant(ctx context.Context, tenantID int64) (pgx.Tx, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tenant tx: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.current_tenant_id', $1, true)`,
		fmt.Sprint(tenantID)); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("set tenant: %w", err)
	}
	return tx, nil
}

func TestBillingRLS_HoldAndIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()

	// Superuser pool for setup.
	superDSN := testDSN(t)
	superPool, err := pgxpool.New(ctx, superDSN)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	t.Cleanup(superPool.Close)
	applyMigrations(t, ctx, superPool)

	// Seed wallets for tenant 1 and tenant 2 via superuser.
	seedWallet(t, ctx, superPool, 1, 5000)
	seedWallet(t, ctx, superPool, 2, 3000)

	// Build app_tenant pool.
	tenantDSN := buildAppTenantDSNBilling(t, superDSN)
	tenantPool, err := pgxpool.New(ctx, tenantDSN)
	if err != nil {
		t.Fatalf("app_tenant pool: %v", err)
	}
	t.Cleanup(tenantPool.Close)

	// Repo with RLS injected.
	txb := &poolTxBeginner{pool: tenantPool}
	r := NewRepo(superPool).UseTenantRLS(txb)

	t.Run("hold_under_rls_tenant1_succeeds", func(t *testing.T) {
		c, err := r.Hold(ctx, HoldRequest{
			TenantID:    1,
			AccountJID:  "1001@s.whatsapp.net",
			MessageID:   "rls-m1",
			CountryCode: "US",
			Amount:      100,
		})
		if err != nil {
			t.Fatalf("Hold tenant1: %v", err)
		}
		if c.State != "held" {
			t.Errorf("state = %q, want held", c.State)
		}
	})

	t.Run("rls_tenant1_cannot_see_tenant2_wallet", func(t *testing.T) {
		// Under tenant=1 RLS context, querying tenant_wallets should only return tenant1.
		tx, err := tenantPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck

		if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant_id', '1', true)`); err != nil {
			t.Fatalf("set_config: %v", err)
		}

		// Count wallets visible under tenant=1: should be 1 (own wallet only).
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tenant_wallets`).Scan(&count); err != nil {
			t.Fatalf("count wallets: %v", err)
		}
		if count != 1 {
			t.Errorf("tenant1 sees %d wallets, want 1 (own only)", count)
		}

		// Count billing_charges visible under tenant=1 (should be 1 from prior subtest).
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM billing_charges`).Scan(&count); err != nil {
			t.Fatalf("count charges: %v", err)
		}
		if count != 1 {
			t.Errorf("tenant1 sees %d charges, want 1", count)
		}
	})

	t.Run("existing_hold_nil_txb_still_works", func(t *testing.T) {
		// Repo without RLS injection — old code path, must stay green.
		rNoRLS := NewRepo(superPool)
		seedWallet(t, ctx, superPool, 99, 1000)
		c, err := rNoRLS.Hold(ctx, HoldRequest{
			TenantID:    99,
			AccountJID:  "9900@s.whatsapp.net",
			MessageID:   "norls-m1",
			CountryCode: "US",
			Amount:      50,
		})
		if err != nil {
			t.Fatalf("Hold (no RLS): %v", err)
		}
		if c.State != "held" {
			t.Errorf("state = %q, want held", c.State)
		}
	})
}
