package dispatch

import (
	"errors"
	"testing"
	"time"
)

func TestSelectAccountWarmupGate_OnlyMatureEligible(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	mature := time.Now().Add(-30 * 24 * time.Hour) // quota headroom

	seedAccount(t, ctx, pool, "mat@s.whatsapp.net", "US", mature, 100, 0)
	seedAccount(t, ctx, pool, "warm@s.whatsapp.net", "US", mature, 100, 0)

	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_profiles (account_jid,tenant_id,lane,stage) VALUES ($1,1,'STANDARD',$2)`,
		"mat@s.whatsapp.net", "MATURE"); err != nil {
		t.Fatalf("seed warmup mat: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_profiles (account_jid,tenant_id,lane,stage) VALUES ($1,1,'STANDARD',$2)`,
		"warm@s.whatsapp.net", "WARMING"); err != nil {
		t.Fatalf("seed warmup warm: %v", err)
	}

	d := NewDispatcher(pool, nil, nil, nil, time.Second).WithWarmupGate(true)
	for i := 0; i < 8; i++ {
		jid, err := pickAccount(t, ctx, d, "US")
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if jid == "warm@s.whatsapp.net" {
			t.Fatalf("WARMING account must not be selected under gate")
		}
		if jid != "mat@s.whatsapp.net" {
			t.Fatalf("picked %s, want mat@s.whatsapp.net", jid)
		}
	}
}

func TestSelectAccountWarmupGate_WarmingOnlyPoolNoCapacity(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	mature := time.Now().Add(-30 * 24 * time.Hour)

	seedAccount(t, ctx, pool, "warm@s.whatsapp.net", "US", mature, 100, 0)
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_profiles (account_jid,tenant_id,lane,stage) VALUES ($1,1,'STANDARD',$2)`,
		"warm@s.whatsapp.net", "WARMING"); err != nil {
		t.Fatalf("seed warmup warm: %v", err)
	}

	d := NewDispatcher(pool, nil, nil, nil, time.Second).WithWarmupGate(true)
	if _, err := pickAccount(t, ctx, d, "US"); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("err=%v, want ErrNoCapacity", err)
	}
}

func TestSelectAccountWarmupGate_OffSelectsEitherAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	mature := time.Now().Add(-30 * 24 * time.Hour)

	seedAccount(t, ctx, pool, "mat@s.whatsapp.net", "US", mature, 100, 0)
	seedAccount(t, ctx, pool, "warm@s.whatsapp.net", "US", mature, 100, 0)
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_profiles (account_jid,tenant_id,lane,stage) VALUES ($1,1,'STANDARD',$2)`,
		"mat@s.whatsapp.net", "MATURE"); err != nil {
		t.Fatalf("seed warmup mat: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_profiles (account_jid,tenant_id,lane,stage) VALUES ($1,1,'STANDARD',$2)`,
		"warm@s.whatsapp.net", "WARMING"); err != nil {
		t.Fatalf("seed warmup warm: %v", err)
	}

	dOff := NewDispatcher(pool, nil, nil, nil, time.Second).WithWarmupGate(false)
	jid, err := pickAccount(t, ctx, dOff, "US")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if jid == "" {
		t.Fatalf("gate off should still select some account")
	}
}
