// Package billing implements transactional pre-deduction, settlement, and
// admin-reviewed refunds over a frozen-funds wallet model.
package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInsufficientFunds = errors.New("billing: insufficient balance")
	ErrChargeConflict    = errors.New("billing: invalid charge state transition")
	ErrRefundConflict    = errors.New("billing: refund already reviewed")
	ErrNotFound          = errors.New("billing: record not found")
)

type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

type HoldRequest struct {
	TenantID    int64
	AccountJID  string
	MessageID   string
	CountryCode string
	Amount      int64 // minor units, must be > 0
}

type Charge struct {
	ID    int64
	State string
}

// Hold atomically pre-deducts: balance→frozen, charge=held, ledger=hold.
// Idempotent on (TenantID, MessageID): a re-hold returns the existing charge
// and moves no money.
func (r *Repo) Hold(ctx context.Context, h HoldRequest) (*Charge, error) {
	var out Charge
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var chargeID int64
		err := tx.QueryRow(ctx, `
INSERT INTO billing_charges (tenant_id, account_jid, message_id, country_code, amount, state)
VALUES ($1,$2,$3,$4,$5,'held')
ON CONFLICT (tenant_id, message_id) DO NOTHING
RETURNING id`,
			h.TenantID, h.AccountJID, h.MessageID, h.CountryCode, h.Amount).Scan(&chargeID)
		if errors.Is(err, pgx.ErrNoRows) {
			// already charged → return existing, move no money
			return tx.QueryRow(ctx,
				`SELECT id, state::text FROM billing_charges WHERE tenant_id=$1 AND message_id=$2`,
				h.TenantID, h.MessageID).Scan(&out.ID, &out.State)
		}
		if err != nil {
			return fmt.Errorf("insert charge: %w", err)
		}

		var bal, frz int64
		if err := tx.QueryRow(ctx,
			`SELECT balance, frozen FROM tenant_wallets WHERE tenant_id=$1 FOR UPDATE`,
			h.TenantID).Scan(&bal, &frz); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock wallet: %w", err)
		}
		if bal < h.Amount {
			return ErrInsufficientFunds // rollback also drops the charge insert
		}
		newBal, newFrz := bal-h.Amount, frz+h.Amount
		if _, err := tx.Exec(ctx,
			`UPDATE tenant_wallets SET balance=$2, frozen=$3, version=version+1 WHERE tenant_id=$1`,
			h.TenantID, newBal, newFrz); err != nil {
			return fmt.Errorf("update wallet: %w", err)
		}
		if err := insertLedger(ctx, tx, h.TenantID, chargeID, "hold",
			-h.Amount, h.Amount, newBal, newFrz, "hold:"+h.MessageID); err != nil {
			return err
		}
		out = Charge{ID: chargeID, State: "held"}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// moveWallet locks the wallet row and applies signed deltas, returning new values.
func moveWallet(ctx context.Context, tx pgx.Tx, tenantID, dBal, dFrz int64) (newBal, newFrz int64, err error) {
	if err = tx.QueryRow(ctx,
		`SELECT balance, frozen FROM tenant_wallets WHERE tenant_id=$1 FOR UPDATE`,
		tenantID).Scan(&newBal, &newFrz); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, ErrNotFound
		}
		return 0, 0, fmt.Errorf("lock wallet: %w", err)
	}
	newBal += dBal
	newFrz += dFrz
	if newBal < 0 || newFrz < 0 {
		return 0, 0, fmt.Errorf("billing: wallet would go negative (bal=%d frz=%d)", newBal, newFrz)
	}
	if _, err = tx.Exec(ctx,
		`UPDATE tenant_wallets SET balance=$2, frozen=$3, version=version+1 WHERE tenant_id=$1`,
		tenantID, newBal, newFrz); err != nil {
		return 0, 0, fmt.Errorf("update wallet: %w", err)
	}
	return newBal, newFrz, nil
}

// insertLedger appends an immutable ledger row; idem_key UNIQUE prevents double-posting.
func insertLedger(ctx context.Context, tx pgx.Tx, tenantID, chargeID int64, kind string,
	dBal, dFrz, balAfter, frzAfter int64, idemKey string) error {
	_, err := tx.Exec(ctx, `
INSERT INTO wallet_ledger
  (tenant_id, charge_id, kind, delta_balance, delta_frozen, balance_after, frozen_after, idem_key)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (idem_key) DO NOTHING`,
		tenantID, chargeID, kind, dBal, dFrz, balAfter, frzAfter, idemKey)
	if err != nil {
		return fmt.Errorf("insert ledger: %w", err)
	}
	return nil
}

// Settle marks a held charge settled and consumes the frozen amount (platform
// revenue). Idempotent: a non-held charge is a silent no-op.
func (r *Repo) Settle(ctx context.Context, tenantID int64, messageID string) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var chargeID, amount int64
		err := tx.QueryRow(ctx, `
UPDATE billing_charges SET state='settled'
 WHERE tenant_id=$1 AND message_id=$2 AND state='held'
RETURNING id, amount`, tenantID, messageID).Scan(&chargeID, &amount)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already settled or not held → idempotent pass
		}
		if err != nil {
			return fmt.Errorf("settle charge: %w", err)
		}
		newBal, newFrz, err := moveWallet(ctx, tx, tenantID, 0, -amount)
		if err != nil {
			return err
		}
		return insertLedger(ctx, tx, tenantID, chargeID, "settle",
			0, -amount, newBal, newFrz, "settle:"+messageID)
	})
}

// RequestRefund moves a held charge to refund_pending and enqueues an admin
// review. Funds STAY frozen — never returned here. Idempotent: charge_id UNIQUE
// on refund_requests + the state guard prevent duplicates.
func (r *Repo) RequestRefund(ctx context.Context, tenantID int64, messageID, reason string) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var chargeID, amount int64
		err := tx.QueryRow(ctx, `
UPDATE billing_charges SET state='refund_pending'
 WHERE tenant_id=$1 AND message_id=$2 AND state='held'
RETURNING id, amount`, tenantID, messageID).Scan(&chargeID, &amount)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already processed → idempotent pass
		}
		if err != nil {
			return fmt.Errorf("mark refund_pending: %w", err)
		}
		_, err = tx.Exec(ctx, `
INSERT INTO refund_requests (charge_id, tenant_id, amount, reason, state)
VALUES ($1,$2,$3,$4,'pending')
ON CONFLICT (charge_id) DO NOTHING`,
			chargeID, tenantID, amount, reason)
		if err != nil {
			return fmt.Errorf("enqueue refund: %w", err)
		}
		return nil // money stays frozen, awaiting admin review
	})
}
