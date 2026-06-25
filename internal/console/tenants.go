// internal/console/tenants.go
package console

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantRow is a tenant registry row for admin/sales listing.
type TenantRow struct {
	ID           int64
	Name         string
	Status       string
	SalesOwnerID *int64
}

// SalesUser is a console_users row with role='sales'.
type SalesUser struct {
	ID    int64
	Email string
}

// TenantRepo reads the tenants table via the SystemPool (BYPASSRLS).
type TenantRepo struct{ pool *pgxpool.Pool }

func NewTenantRepo(pool *pgxpool.Pool) *TenantRepo { return &TenantRepo{pool: pool} }

var ErrTenantNotFound = errors.New("console: tenant not found")

func (r *TenantRepo) List(ctx context.Context) ([]TenantRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, name, status, sales_owner_id FROM tenants ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()
	var out []TenantRow
	for rows.Next() {
		var t TenantRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Status, &t.SalesOwnerID); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *TenantRepo) Get(ctx context.Context, id int64) (*TenantRow, error) {
	var t TenantRow
	err := r.pool.QueryRow(ctx, `SELECT id, name, status, sales_owner_id FROM tenants WHERE id=$1`, id).
		Scan(&t.ID, &t.Name, &t.Status, &t.SalesOwnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get tenant: %w", err)
	}
	return &t, nil
}

// ListSalesUsers returns all console_users with role='sales', ordered by email.
func (r *TenantRepo) ListSalesUsers(ctx context.Context) ([]SalesUser, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, email FROM console_users WHERE role='sales' ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("list sales users: %w", err)
	}
	defer rows.Close()
	var out []SalesUser
	for rows.Next() {
		var u SalesUser
		if err := rows.Scan(&u.ID, &u.Email); err != nil {
			return nil, fmt.Errorf("scan sales user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetSalesOwner assigns a tenant's sales owner; the target must be a sales user.
func (r *TenantRepo) SetSalesOwner(ctx context.Context, tenantID, salesUserID int64) error {
	var role string
	err := r.pool.QueryRow(ctx, `SELECT role FROM console_users WHERE id=$1`, salesUserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("sales owner: user %d not found", salesUserID)
	}
	if err != nil {
		return fmt.Errorf("sales owner: lookup user: %w", err)
	}
	if role != string(RoleSales) {
		return fmt.Errorf("sales owner: user %d is not a sales user (role=%s)", salesUserID, role)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE tenants SET sales_owner_id=$1 WHERE id=$2`, salesUserID, tenantID); err != nil {
		return fmt.Errorf("set sales owner: %w", err)
	}
	return nil
}
