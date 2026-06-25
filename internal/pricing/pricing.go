// internal/pricing/pricing.go
// Package pricing stores and resolves per-tenant×country unit prices (minor units).
package pricing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNoPrice = errors.New("pricing: no price set for tenant/country")

// Price is one tenant×country unit price.
type Price struct {
	Country   string
	UnitPrice int64
}

// Repo reads/writes tenant_pricing. Construct with the BYPASSRLS SystemPool —
// pricing is platform metadata, not tenant-row-scoped.
type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// SetPrice upserts the unit price for a tenant×country (minor units, must be > 0).
func (r *Repo) SetPrice(ctx context.Context, tenantID int64, country string, unitPrice int64) error {
	if unitPrice <= 0 {
		return fmt.Errorf("pricing: unit price must be positive, got %d", unitPrice)
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO tenant_pricing (tenant_id, country_code, unit_price)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id, country_code)
		 DO UPDATE SET unit_price = EXCLUDED.unit_price, updated_at = now()`,
		tenantID, country, unitPrice)
	if err != nil {
		return fmt.Errorf("set price: %w", err)
	}
	return nil
}

// GetPrice returns the configured unit price or ErrNoPrice.
func (r *Repo) GetPrice(ctx context.Context, tenantID int64, country string) (int64, error) {
	var p int64
	err := r.pool.QueryRow(ctx,
		`SELECT unit_price FROM tenant_pricing WHERE tenant_id=$1 AND country_code=$2`,
		tenantID, country).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNoPrice
	}
	if err != nil {
		return 0, fmt.Errorf("get price: %w", err)
	}
	return p, nil
}

// PriceFor is the hot-path lookup: the configured price, or `fallback` on miss/error.
// It never returns an error so a transient DB blip cannot block billing — but a real
// outage will surface elsewhere (the Hold itself runs against the same DB).
func (r *Repo) PriceFor(ctx context.Context, tenantID int64, country string, fallback int64) int64 {
	p, err := r.GetPrice(ctx, tenantID, country)
	if err != nil {
		return fallback
	}
	return p
}

// ListForTenant returns all country prices set for a tenant, ordered by country.
func (r *Repo) ListForTenant(ctx context.Context, tenantID int64) ([]Price, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT country_code, unit_price FROM tenant_pricing WHERE tenant_id=$1 ORDER BY country_code`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("list prices: %w", err)
	}
	defer rows.Close()
	var out []Price
	for rows.Next() {
		var p Price
		if err := rows.Scan(&p.Country, &p.UnitPrice); err != nil {
			return nil, fmt.Errorf("scan price: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
