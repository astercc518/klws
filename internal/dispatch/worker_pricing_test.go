// internal/dispatch/worker_pricing_test.go
package dispatch

import (
	"context"
	"testing"
)

// TestWithPricingSetsAmount verifies the price hook is invoked with the payload's
// tenant+country and that WithPricing is chainable. (Unit-level: we exercise the
// hook directly rather than a full send, which the integration tests already cover.)
func TestWithPricingSetsAmount(t *testing.T) {
	var gotTenant int64
	var gotCountry string
	w := (&SendWorker{}).WithPricing(func(_ context.Context, tenantID int64, country string) int64 {
		gotTenant, gotCountry = tenantID, country
		return 7
	})
	if w.priceFor == nil {
		t.Fatal("WithPricing did not set the hook")
	}
	if got := w.priceFor(context.Background(), 42, "US"); got != 7 {
		t.Fatalf("price hook: want 7, got %d", got)
	}
	if gotTenant != 42 || gotCountry != "US" {
		t.Fatalf("hook args: got tenant=%d country=%s", gotTenant, gotCountry)
	}
}

// TestAmountForUsesFallbackWhenUnset verifies the default unit price is 1 when no
// pricing hook is configured (preserves pre-pricing behavior).
func TestAmountForUsesFallbackWhenUnset(t *testing.T) {
	w := &SendWorker{}
	if got := w.amountFor(context.Background(), 1, "US"); got != 1 {
		t.Fatalf("default amount: want 1, got %d", got)
	}
}
