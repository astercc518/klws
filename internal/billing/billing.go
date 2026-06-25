// Package billing implements transactional pre-deduction, settlement, and
// admin-reviewed refunds over a frozen-funds wallet model.
//
// TODO phased-retrofit: dispatch/sendgate/store-node/metrics-dbcollector still
// run on the superuser pool and need RLS migration in later milestones.
// Current milestone (M10 Task 2): Hold/Settle/RequestRefund support optional
// TenantTxBeginner injection via UseTenantRLS; nil → falls back to r.pool.
package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/audit"
)

var (
	ErrInsufficientFunds = errors.New("billing: insufficient balance")
	ErrChargeConflict    = errors.New("billing: invalid charge state transition")
	ErrRefundConflict    = errors.New("billing: refund already reviewed")
	ErrNotFound          = errors.New("billing: record not found")
	ErrWalletLocked      = errors.New("billing: wallet is locked (reconciliation drift)")
)

// TenantTxBeginner opens a per-tenant RLS-scoped transaction. Satisfied by
// *store.Manager; nil disables RLS (existing tests, dev mode).
type TenantTxBeginner interface {
	WithTenant(ctx context.Context, tenantID int64) (pgx.Tx, error)
}

type Repo struct {
	pool *pgxpool.Pool
	txb  TenantTxBeginner  // nil → use r.pool.Begin (backward-compat)
	aw   *audit.AuditWriter // nil → skip audit (zero regression for existing callers)
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// UseTenantRLS injects a TenantTxBeginner so Hold/Settle/RequestRefund acquire
// RLS-scoped transactions. Returns r for chaining. Existing callers that never
// call this have txb=nil and behave identically to before.
func (r *Repo) UseTenantRLS(b TenantTxBeginner) *Repo { r.txb = b; return r }

// UseAudit injects an AuditWriter. When set, ApproveRefund/RejectRefund write
// an audit row inside the same transaction as the business update. nil → no-op.
func (r *Repo) UseAudit(aw *audit.AuditWriter) *Repo { r.aw = aw; return r }

// beginTenantTx picks the correct tx source: RLS-scoped if txb is set, else superuser.
func (r *Repo) beginTenantTx(ctx context.Context, tenantID int64) (pgx.Tx, error) {
	if r.txb != nil {
		return r.txb.WithTenant(ctx, tenantID)
	}
	return r.pool.Begin(ctx)
}

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
	tx, err := r.beginTenantTx(ctx, h.TenantID)
	if err != nil {
		return nil, fmt.Errorf("hold begin tx: %w", err)
	}
	var out Charge
	err = func() error {
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
		var locked bool
		if err := tx.QueryRow(ctx,
			`SELECT balance, frozen, locked FROM tenant_wallets WHERE tenant_id=$1 FOR UPDATE`,
			h.TenantID).Scan(&bal, &frz, &locked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock wallet: %w", err)
		}
		if locked {
			return ErrWalletLocked // drift-locked wallet rejects new deductions
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
	}()
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("hold commit: %w", err)
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
	tx, err := r.beginTenantTx(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("settle begin tx: %w", err)
	}
	err = func() error {
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
	}()
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// RequestRefund moves a held charge to refund_pending and enqueues an admin
// review. Funds STAY frozen — never returned here. Idempotent: charge_id UNIQUE
// on refund_requests + the state guard prevent duplicates.
func (r *Repo) RequestRefund(ctx context.Context, tenantID int64, messageID, reason string) error {
	tx, err := r.beginTenantTx(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("requestrefund begin tx: %w", err)
	}
	err = func() error {
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
	}()
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// ApproveRefund: admin approves; frozen→balance (funds returned).
func (r *Repo) ApproveRefund(ctx context.Context, refundID, adminID int64, note string) error {
	return r.reviewRefund(ctx, refundID, adminID, note, true)
}

// RejectRefund: admin rejects; frozen is consumed as revenue (no return).
func (r *Repo) RejectRefund(ctx context.Context, refundID, adminID int64, note string) error {
	return r.reviewRefund(ctx, refundID, adminID, note, false)
}

// Topup credits a tenant's balance and writes a 'topup' ledger row (charge_id
// NULL) so the reconciliation invariant balance=Σledger.delta_balance holds.
// Idempotent on ref (the payment reference): a replayed topup credits nothing.
func (r *Repo) Topup(ctx context.Context, tenantID, amount int64, ref string) error {
	if amount <= 0 {
		return fmt.Errorf("billing: topup amount must be positive, got %d", amount)
	}
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tenant_wallets (tenant_id) VALUES ($1) ON CONFLICT (tenant_id) DO NOTHING`,
			tenantID); err != nil {
			return fmt.Errorf("ensure wallet: %w", err)
		}
		var bal, frz int64
		if err := tx.QueryRow(ctx,
			`SELECT balance, frozen FROM tenant_wallets WHERE tenant_id=$1 FOR UPDATE`,
			tenantID).Scan(&bal, &frz); err != nil {
			return fmt.Errorf("lock wallet: %w", err)
		}
		// idempotency: this ref already applied?
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM wallet_ledger WHERE idem_key=$1)`,
			"topup:"+ref).Scan(&exists); err != nil {
			return fmt.Errorf("check topup idem: %w", err)
		}
		if exists {
			return nil // already credited
		}
		newBal := bal + amount
		if _, err := tx.Exec(ctx,
			`UPDATE tenant_wallets SET balance=$2, version=version+1 WHERE tenant_id=$1`,
			tenantID, newBal); err != nil {
			return fmt.Errorf("credit wallet: %w", err)
		}
		_, err := tx.Exec(ctx, `
INSERT INTO wallet_ledger
  (tenant_id, charge_id, kind, delta_balance, delta_frozen, balance_after, frozen_after, idem_key)
VALUES ($1, NULL, 'topup', $2, 0, $3, $4, $5)
ON CONFLICT (idem_key) DO NOTHING`,
			tenantID, amount, newBal, frz, "topup:"+ref)
		if err != nil {
			return fmt.Errorf("insert topup ledger: %w", err)
		}
		return nil
	})
}

// Balance returns the tenant's available and frozen balances (minor units).
// A missing wallet row reads as (0, 0, nil) — a tenant with no activity yet.
func (r *Repo) Balance(ctx context.Context, tenantID int64) (balance, frozen int64, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT balance, frozen FROM tenant_wallets WHERE tenant_id=$1`, tenantID).
		Scan(&balance, &frozen)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("read balance: %w", err)
	}
	return balance, frozen, nil
}

// LedgerEntry is one immutable wallet ledger row.
type LedgerEntry struct {
	ID           int64
	ChargeID     *int64
	Kind         string
	DeltaBalance int64
	DeltaFrozen  int64
	BalanceAfter int64
	FrozenAfter  int64
	CreatedAt    time.Time
}

// Ledger returns a tenant's most-recent ledger entries (newest first). limit<=0 → 100.
func (r *Repo) Ledger(ctx context.Context, tenantID int64, limit int) ([]LedgerEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id, charge_id, kind, delta_balance, delta_frozen, balance_after, frozen_after, created_at
		   FROM wallet_ledger WHERE tenant_id=$1 ORDER BY id DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("query ledger: %w", err)
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.ChargeID, &e.Kind, &e.DeltaBalance, &e.DeltaFrozen,
			&e.BalanceAfter, &e.FrozenAfter, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ledger: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *Repo) reviewRefund(ctx context.Context, refundID, adminID int64, note string, approve bool) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// only a pending refund can be adjudicated; lock it against concurrent reviewers
		var chargeID, tenantID, amount int64
		err := tx.QueryRow(ctx,
			`SELECT charge_id, tenant_id, amount FROM refund_requests
			  WHERE id=$1 AND state='pending' FOR UPDATE`, refundID).
			Scan(&chargeID, &tenantID, &amount)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRefundConflict
		}
		if err != nil {
			return fmt.Errorf("lock refund: %w", err)
		}

		newState, chargeState, kind, idem := "rejected", "rejected", "reject", "reject:"
		deltaBal, deltaFrz := int64(0), -amount // reject: consume frozen only
		if approve {
			newState, chargeState, kind, idem = "approved", "refunded", "refund", "refund:"
			deltaBal, deltaFrz = amount, -amount // approve: frozen returns to balance
		}

		if _, err := tx.Exec(ctx, `
UPDATE refund_requests SET state=$2, reviewed_by=$3, review_note=$4, reviewed_at=now()
 WHERE id=$1`, refundID, newState, adminID, note); err != nil {
			return fmt.Errorf("update refund: %w", err)
		}

		ct, err := tx.Exec(ctx,
			`UPDATE billing_charges SET state=$2 WHERE id=$1 AND state='refund_pending'`,
			chargeID, chargeState)
		if err != nil {
			return fmt.Errorf("settle charge state: %w", err)
		}
		if ct.RowsAffected() == 0 {
			return ErrChargeConflict
		}

		newBal, newFrz, err := moveWallet(ctx, tx, tenantID, deltaBal, deltaFrz)
		if err != nil {
			return err
		}
		if err := insertLedger(ctx, tx, tenantID, chargeID, kind,
			deltaBal, deltaFrz, newBal, newFrz, idem+fmt.Sprint(chargeID)); err != nil {
			return err
		}
		if r.aw != nil {
			action := "refund.reject"
			if approve {
				action = "refund.approve"
			}
			if err := r.aw.RecordTx(ctx, tx, audit.AuditEntry{
				TenantID:     tenantID,
				ActorID:      adminID,
				Action:       action,
				ResourceType: "refund_request",
				ResourceID:   refundID,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
