// internal/console/customer.go
package console

import (
	"context"
	"errors"
	"fmt"

	"github.com/acme/wadist/internal/billing"
	"github.com/jackc/pgx/v5"
)

// CampaignRow is a summary of a single campaign for the customer dashboard.
type CampaignRow struct {
	ID            int64
	State         string
	Total, Sent, Failed int
}

// customerBalance reads the session tenant's wallet balance and frozen amount
// via an RLS-scoped transaction. If the tenant has no wallet row, it returns 0,0,nil.
func (s *Server) customerBalance(ctx context.Context) (balance, frozen int64, err error) {
	tx, err := s.withTenantTx(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	err = tx.QueryRow(ctx, `SELECT balance, frozen FROM tenant_wallets`).Scan(&balance, &frozen)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("customer balance: %w", err)
	}
	return balance, frozen, nil
}

// customerLedger returns up to limit wallet ledger entries for the session tenant,
// ordered newest-first. RLS auto-scopes results to the session tenant. limit<=0 → 100.
func (s *Server) customerLedger(ctx context.Context, limit int) ([]billing.LedgerEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	tx, err := s.withTenantTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `
SELECT id, charge_id, kind, delta_balance, delta_frozen, balance_after, frozen_after, created_at
  FROM wallet_ledger ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("customer ledger: %w", err)
	}
	defer rows.Close()
	var out []billing.LedgerEntry
	for rows.Next() {
		var e billing.LedgerEntry
		if err := rows.Scan(&e.ID, &e.ChargeID, &e.Kind, &e.DeltaBalance, &e.DeltaFrozen, &e.BalanceAfter, &e.FrozenAfter, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ledger: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// customerCampaigns returns up to limit campaigns for the session tenant,
// ordered by id DESC. RLS auto-scopes results to the session tenant.
func (s *Server) customerCampaigns(ctx context.Context, limit int) ([]CampaignRow, error) {
	if limit <= 0 {
		limit = 50
	}
	tx, err := s.withTenantTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `SELECT id, state::text, total, sent, failed FROM campaigns ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("customer campaigns: %w", err)
	}
	defer rows.Close()
	var out []CampaignRow
	for rows.Next() {
		var c CampaignRow
		if err := rows.Scan(&c.ID, &c.State, &c.Total, &c.Sent, &c.Failed); err != nil {
			return nil, fmt.Errorf("scan campaign: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
