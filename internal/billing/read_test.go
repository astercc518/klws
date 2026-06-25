// internal/billing/read_test.go
package billing

import (
	"context"
	"testing"
)

func TestBalanceAndLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	repo, _, pool := newRepoWithSchema(t) // spins PG, applies migrations, returns Repo+ctx+pool
	const tid = int64(42)
	seedWallet(t, ctx, pool, tid, 100) // creates wallet with balance=100, frozen=0

	bal, frozen, err := repo.Balance(ctx, tid)
	if err != nil || bal != 100 || frozen != 0 {
		t.Fatalf("balance: want 100/0, got %d/%d err=%v", bal, frozen, err)
	}

	// unknown tenant → zero, no error.
	if b, f, err := repo.Balance(ctx, 999999); err != nil || b != 0 || f != 0 {
		t.Fatalf("unknown wallet: want 0/0/nil, got %d/%d/%v", b, f, err)
	}

	// a topup writes a ledger row → Ledger returns it.
	if err := repo.Topup(ctx, tid, 50, "ref-1"); err != nil {
		t.Fatalf("topup: %v", err)
	}
	entries, err := repo.Ledger(ctx, tid, 10)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one ledger entry after topup")
	}
	if entries[0].Kind != "topup" || entries[0].DeltaBalance != 50 {
		t.Fatalf("latest entry: got kind=%s delta=%d", entries[0].Kind, entries[0].DeltaBalance)
	}
}
