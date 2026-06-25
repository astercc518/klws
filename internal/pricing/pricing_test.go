// internal/pricing/pricing_test.go
package pricing

import (
	"context"
	"errors"
	"testing"
)

func TestSetGetPriceAndFallback(t *testing.T) {
	ctx := context.Background()
	mgr := newTestPool(t)
	repo := NewRepo(mgr.SystemPool())
	tid := seedTenant(t, ctx, mgr, "Acme")

	// no price yet → GetPrice ErrNoPrice; PriceFor returns fallback.
	if _, err := repo.GetPrice(ctx, tid, "US"); !errors.Is(err, ErrNoPrice) {
		t.Fatalf("want ErrNoPrice, got %v", err)
	}
	if got := repo.PriceFor(ctx, tid, "US", 3); got != 3 {
		t.Fatalf("fallback: want 3, got %d", got)
	}

	// set then read.
	if err := repo.SetPrice(ctx, tid, "US", 5); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got, err := repo.GetPrice(ctx, tid, "US"); err != nil || got != 5 {
		t.Fatalf("get: want 5, got %d err=%v", got, err)
	}
	if got := repo.PriceFor(ctx, tid, "US", 3); got != 5 {
		t.Fatalf("PriceFor after set: want 5, got %d", got)
	}

	// upsert (change price).
	if err := repo.SetPrice(ctx, tid, "US", 8); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, _ := repo.GetPrice(ctx, tid, "US"); got != 8 {
		t.Fatalf("after upsert want 8, got %d", got)
	}

	// reject non-positive.
	if err := repo.SetPrice(ctx, tid, "US", 0); err == nil {
		t.Fatalf("expected error for non-positive price")
	}

	// list.
	_ = repo.SetPrice(ctx, tid, "GB", 4)
	list, err := repo.ListForTenant(ctx, tid)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: want 2 entries, got %d err=%v", len(list), err)
	}
}
