package billing

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Report is a single-tenant reconciliation result. Healthy iff all three drifts are zero.
type Report struct {
	TenantID      int64
	WalletBalance int64
	WalletFrozen  int64
	LedgerBalance int64 // Σ delta_balance
	LedgerFrozen  int64 // Σ delta_frozen
	ChargesFrozen int64 // Σ amount of charges in {held, refund_pending}
	DriftBalance  int64 // wallet - ledger
	DriftFrozen   int64
	DriftCharges  int64 // frozen - charges_frozen
}

func (r Report) Healthy() bool {
	return r.DriftBalance == 0 && r.DriftFrozen == 0 && r.DriftCharges == 0
}

// single statement → single MVCC snapshot → the three aggregates are consistent.
const reconcileOneSQL = `
SELECT
    w.balance, w.frozen,
    COALESCE(lb.sum_bal, 0), COALESCE(lb.sum_frz, 0),
    COALESCE(oc.frozen_charges, 0)
FROM tenant_wallets w
LEFT JOIN (
    SELECT tenant_id, SUM(delta_balance) AS sum_bal, SUM(delta_frozen) AS sum_frz
      FROM wallet_ledger WHERE tenant_id=$1 GROUP BY tenant_id
) lb ON lb.tenant_id = w.tenant_id
LEFT JOIN (
    SELECT tenant_id, SUM(amount) AS frozen_charges
      FROM billing_charges
     WHERE tenant_id=$1 AND state IN ('held','refund_pending') GROUP BY tenant_id
) oc ON oc.tenant_id = w.tenant_id
WHERE w.tenant_id=$1;`

// ReconcileTenant recomputes a tenant's wallet from the ledger + open charges,
// records an audit row, and returns the report. Read-only on money (never fixes).
func (r *Repo) ReconcileTenant(ctx context.Context, tenantID int64) (*Report, error) {
	rep := Report{TenantID: tenantID}
	err := r.pool.QueryRow(ctx, reconcileOneSQL, tenantID).Scan(
		&rep.WalletBalance, &rep.WalletFrozen,
		&rep.LedgerBalance, &rep.LedgerFrozen, &rep.ChargesFrozen)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("reconcile query: %w", err)
	}
	rep.DriftBalance = rep.WalletBalance - rep.LedgerBalance
	rep.DriftFrozen = rep.WalletFrozen - rep.LedgerFrozen
	rep.DriftCharges = rep.WalletFrozen - rep.ChargesFrozen

	if err := persistRun(ctx, r, rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

func persistRun(ctx context.Context, r *Repo, rep Report) error {
	_, err := r.pool.Exec(ctx, `
INSERT INTO reconciliation_runs
  (tenant_id, wallet_balance, wallet_frozen, ledger_balance, ledger_frozen,
   charges_frozen, drift_balance, drift_frozen, drift_charges, healthy)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		rep.TenantID, rep.WalletBalance, rep.WalletFrozen, rep.LedgerBalance, rep.LedgerFrozen,
		rep.ChargesFrozen, rep.DriftBalance, rep.DriftFrozen, rep.DriftCharges, rep.Healthy())
	if err != nil {
		return fmt.Errorf("persist recon run: %w", err)
	}
	return nil
}
