// internal/sendgate/admit_test.go
package sendgate

import (
	"context"
	"testing"
	"time"
)

func newGate(t *testing.T) (*SendGate, context.Context) {
	t.Helper()
	pool, ctx := pgPool(t)
	return NewSendGate(pool, NewAdmission(redisClient(t), BackoffParams{}), 1*time.Second), ctx
}

func TestAdmit_AllowsActiveHealthyWithinQuota(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	g, ctx := newGate(t)
	seedAccount(t, ctx, g.pool, "acc1", "active", time.Now().Add(-30*24*time.Hour), 100)
	dec, err := g.Admit(ctx, "acc1", "US", time.Now())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !dec.Allow || dec.Ticket == nil {
		t.Fatalf("decision = %+v, want Allow with Ticket", dec)
	}
}

func TestAdmit_DeniesBanned(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	g, ctx := newGate(t)
	seedAccount(t, ctx, g.pool, "acc1", "banned", time.Now().Add(-30*24*time.Hour), 100)
	dec, err := g.Admit(ctx, "acc1", "US", time.Now())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if dec.Allow || dec.Reason != "not_active" {
		t.Fatalf("decision = %+v, want deny not_active", dec)
	}
}

func TestAdmit_DeniesQuarantined(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	g, ctx := newGate(t)
	seedAccount(t, ctx, g.pool, "acc1", "active", time.Now().Add(-30*24*time.Hour), 100)
	now := time.Now()
	if _, err := g.pool.Exec(ctx, `UPDATE account_devices SET quarantined_until=$1 WHERE account_jid='acc1'`, now.Add(time.Hour)); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	dec, err := g.Admit(ctx, "acc1", "US", now)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if dec.Allow || dec.Reason != "quarantined" {
		t.Fatalf("decision = %+v, want deny quarantined", dec)
	}
}

func TestAdmit_DeniesQuotaExhausted(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	g, ctx := newGate(t)
	// brand-new account (age 0) → quota 20; exhaust it, next is denied
	seedAccount(t, ctx, g.pool, "acc1", "active", time.Now(), 100)
	now := time.Now()
	for i := 0; i < 20; i++ {
		dec, err := g.Admit(ctx, "acc1", "US", now.Add(time.Duration(i)*10*time.Second))
		if err != nil || !dec.Allow {
			t.Fatalf("admit %d: dec=%+v err=%v", i, dec, err)
		}
	}
	dec, err := g.Admit(ctx, "acc1", "US", now.Add(2000*time.Second))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if dec.Allow || dec.Reason != "daily_quota" {
		t.Fatalf("decision = %+v, want deny daily_quota", dec)
	}
}
