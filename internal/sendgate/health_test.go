// internal/sendgate/health_test.go
package sendgate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func healthOf(t *testing.T, ctx context.Context, g *SendGate, jid string) (int, *time.Time) {
	t.Helper()
	var h int
	var q *time.Time
	if err := g.pool.QueryRow(ctx, `SELECT health_score, quarantined_until FROM account_devices WHERE account_jid=$1`, jid).Scan(&h, &q); err != nil {
		t.Fatalf("read health: %v", err)
	}
	return h, q
}

func TestApplyHealthSignal_DecrementsAndClamps(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	g := NewSendGate(pool, nil, 0)
	seedAccount(t, ctx, pool, "acc1", "active", time.Now().Add(-30*24*time.Hour), 100)

	if err := g.ApplyHealthSignal(ctx, "acc1", "undelivered", time.Hour); err != nil { // -2
		t.Fatalf("signal: %v", err)
	}
	if h, _ := healthOf(t, ctx, g, "acc1"); h != 98 {
		t.Fatalf("health = %d, want 98", h)
	}
	if err := g.ApplyHealthSignal(ctx, "acc1", "delivered", 0); err != nil { // +1
		t.Fatalf("signal: %v", err)
	}
	if h, _ := healthOf(t, ctx, g, "acc1"); h != 99 {
		t.Fatalf("health = %d, want 99", h)
	}
}

func TestApplyHealthSignal_QuarantinesBelowThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	g := NewSendGate(pool, nil, 0)
	// start at health 45; a wa_warning (-30) drops to 15 < 40 → quarantine
	seedAccount(t, ctx, pool, "acc1", "active", time.Now().Add(-30*24*time.Hour), 45)
	if err := g.ApplyHealthSignal(ctx, "acc1", "wa_warning", 6*time.Hour); err != nil {
		t.Fatalf("signal: %v", err)
	}
	h, q := healthOf(t, ctx, g, "acc1")
	if h != 15 {
		t.Fatalf("health = %d, want 15", h)
	}
	if q == nil || !q.After(time.Now()) {
		t.Fatalf("expected quarantine in the future, got %v", q)
	}
}

func TestApplyHealthSignal_UnknownSignal(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	g := NewSendGate(pool, nil, 0)
	seedAccount(t, ctx, pool, "acc1", "active", time.Now(), 100)
	if err := g.ApplyHealthSignal(ctx, "acc1", "bogus", 0); err == nil {
		t.Fatal("expected error for unknown signal")
	}
}

func TestHeal_RecoversAndUnquarantines(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	g := NewSendGate(pool, nil, 0)
	seedAccount(t, ctx, pool, "acc1", "active", time.Now().Add(-30*24*time.Hour), 50)
	// expired quarantine should be cleared; health should rise by 5
	if _, err := pool.Exec(ctx, `UPDATE account_devices SET quarantined_until = now() - interval '1 hour' WHERE account_jid='acc1'`); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := g.Heal(ctx); err != nil {
		t.Fatalf("heal: %v", err)
	}
	h, q := healthOf(t, ctx, g, "acc1")
	if h != 55 {
		t.Fatalf("health = %d, want 55", h)
	}
	if q != nil {
		t.Fatalf("expired quarantine should be cleared, got %v", q)
	}
}

var _ = errors.New // keep errors import used
