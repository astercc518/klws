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
	ID     int64
	Name   string
	Status string
}

// TenantRepo reads the tenants table via the SystemPool (BYPASSRLS).
type TenantRepo struct{ pool *pgxpool.Pool }

func NewTenantRepo(pool *pgxpool.Pool) *TenantRepo { return &TenantRepo{pool: pool} }

var ErrTenantNotFound = errors.New("console: tenant not found")

func (r *TenantRepo) List(ctx context.Context) ([]TenantRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, name, status FROM tenants ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()
	var out []TenantRow
	for rows.Next() {
		var t TenantRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Status); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *TenantRepo) Get(ctx context.Context, id int64) (*TenantRow, error) {
	var t TenantRow
	err := r.pool.QueryRow(ctx, `SELECT id, name, status FROM tenants WHERE id=$1`, id).
		Scan(&t.ID, &t.Name, &t.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get tenant: %w", err)
	}
	return &t, nil
}
