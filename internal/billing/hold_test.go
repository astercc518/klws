// internal/billing/hold_test.go
package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newRepoWithSchema spins a PG, applies migrations, returns a Repo + ctx + pool.
func newRepoWithSchema(t *testing.T) (*Repo, context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyMigrations(t, ctx, pool)
	return NewRepo(pool), ctx, pool
}

func walletState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID int64) (bal, frz int64) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT balance, frozen FROM tenant_wallets WHERE tenant_id=$1`, tenantID).Scan(&bal, &frz); err != nil {
		t.Fatalf("wallet state: %v", err)
	}
	return
}

func hReq(tenantID int64, msgID string, amount int64) HoldRequest {
	return HoldRequest{TenantID: tenantID, AccountJID: "111@s.whatsapp.net", MessageID: msgID, CountryCode: "US", Amount: amount}
}

func TestHold_DeductsAndFreezes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := newRepoWithSchema(t)
	seedWallet(t, ctx, pool, 1, 1000)

	c, err := r.Hold(ctx, hReq(1, "m1", 300))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if c.State != "held" {
		t.Fatalf("state = %q, want held", c.State)
	}
	bal, frz := walletState(t, ctx, pool, 1)
	if bal != 700 || frz != 300 {
		t.Fatalf("wallet bal=%d frz=%d, want 700/300", bal, frz)
	}
	// ledger entry recorded
	var dBal, dFrz int64
	var kind string
	pool.QueryRow(ctx, `SELECT kind::text, delta_balance, delta_frozen FROM wallet_ledger WHERE idem_key='hold:m1'`).Scan(&kind, &dBal, &dFrz)
	if kind != "hold" || dBal != -300 || dFrz != 300 {
		t.Fatalf("ledger kind=%s dBal=%d dFrz=%d, want hold/-300/300", kind, dBal, dFrz)
	}
}

func TestHold_IdempotentNoDoubleDeduct(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := newRepoWithSchema(t)
	seedWallet(t, ctx, pool, 1, 1000)

	if _, err := r.Hold(ctx, hReq(1, "m1", 300)); err != nil {
		t.Fatalf("hold 1: %v", err)
	}
	c2, err := r.Hold(ctx, hReq(1, "m1", 300)) // same message_id
	if err != nil {
		t.Fatalf("hold 2: %v", err)
	}
	if c2.State != "held" {
		t.Fatalf("re-hold state = %q", c2.State)
	}
	bal, frz := walletState(t, ctx, pool, 1)
	if bal != 700 || frz != 300 {
		t.Fatalf("after re-hold bal=%d frz=%d, want 700/300 (no double deduct)", bal, frz)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger WHERE idem_key='hold:m1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("ledger rows for hold:m1 = %d, want 1", n)
	}
}

func TestHold_InsufficientFunds(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := newRepoWithSchema(t)
	seedWallet(t, ctx, pool, 1, 100)
	if _, err := r.Hold(ctx, hReq(1, "m1", 300)); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("err = %v, want ErrInsufficientFunds", err)
	}
	bal, frz := walletState(t, ctx, pool, 1)
	if bal != 100 || frz != 0 {
		t.Fatalf("after failed hold bal=%d frz=%d, want 100/0 (unchanged)", bal, frz)
	}
}
