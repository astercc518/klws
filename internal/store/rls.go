// internal/store/rls.go
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantTxBeginner opens a transaction pre-configured for a specific tenant's
// RLS context. The caller must Commit or Rollback the returned tx.
type TenantTxBeginner interface {
	WithTenant(ctx context.Context, tenantID int64) (pgx.Tx, error)
}

// WithTenant begins a transaction on the RLS-constrained tenant pool and sets
// the tenant id for the RLS policy (set_config is transaction-scoped when
// is_local=true). The caller MUST Commit or Rollback the returned tx. All
// queries on this tx see only rows where tenant_id matches.
func (m *Manager) WithTenant(ctx context.Context, tenantID int64) (pgx.Tx, error) {
	tx, err := m.tenantPool.Begin(ctx)
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

// TenantPool returns the RLS-constrained pool (role app_tenant, or bizPool
// when AppTenantDSN is empty / single-DSN dev mode).
func (m *Manager) TenantPool() *pgxpool.Pool { return m.tenantPool }

// SystemPool returns the BYPASSRLS pool (role app_system, or bizPool when
// AppSystemDSN is empty / single-DSN dev mode).
func (m *Manager) SystemPool() *pgxpool.Pool { return m.systemPool }

// TODO phased-retrofit: the following subsystems still run on the superuser
// bizPool and need per-tenant RLS migration in later milestones:
//   - dispatch (internal/dispatch, ~13 files) — per-account send path
//   - sendgate (internal/sendgate, ~3 files) — outbound gateway
//   - store-node (internal/store node.go/proxy.go, ~12 files) — cross-tenant
//     admin queries should use SystemPool; per-tenant queries → WithTenant
//   - metrics-dbcollector (~6 files) — cross-tenant aggregation, use SystemPool
// Current milestone (M10 Task 2): only billing Hold/Settle/RequestRefund are
// wired to the tenant path via TenantTxBeginner.
