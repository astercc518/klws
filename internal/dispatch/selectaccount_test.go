package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func pickAccount(t *testing.T, ctx context.Context, d *Dispatcher, country string) (string, error) {
	t.Helper()
	var jid string
	var err error
	e := pgx.BeginTxFunc(ctx, d.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		jid, err = d.selectAccount(ctx, tx, 1, country)
		return err
	})
	if e != nil && err == nil { err = e }
	return jid, err
}

func TestSelectAccount_PicksMostRemainingHealthy(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	pool, ctx := pgPool(t)
	d := NewDispatcher(pool, nil, nil, nil, time.Second)
	mature := time.Now().Add(-30 * 24 * time.Hour) // quota 1000
	seedAccount(t, ctx, pool, "a@s.whatsapp.net", "US", mature, 100, 990) // remaining 10
	seedAccount(t, ctx, pool, "b@s.whatsapp.net", "US", mature, 100, 100) // remaining 900 ← pick
	jid, err := pickAccount(t, ctx, d, "US")
	if err != nil { t.Fatalf("select: %v", err) }
	if jid != "b@s.whatsapp.net" { t.Fatalf("picked %s, want b", jid) }
}

func TestSelectAccount_ExcludesIneligible(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	pool, ctx := pgPool(t)
	d := NewDispatcher(pool, nil, nil, nil, time.Second)
	mature := time.Now().Add(-30 * 24 * time.Hour)
	// only a banned US account + a healthy DE account → no US capacity
	seedAccount(t, ctx, pool, "ban@s.whatsapp.net", "US", mature, 100, 0)
	pool.Exec(ctx, `UPDATE account_devices SET ban_status='banned' WHERE account_jid='ban@s.whatsapp.net'`)
	seedAccount(t, ctx, pool, "de@s.whatsapp.net", "DE", mature, 100, 0)
	if _, err := pickAccount(t, ctx, d, "US"); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("err=%v, want ErrNoCapacity", err)
	}
}

func TestSelectAccount_ExcludesQuarantinedAndExhausted(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	pool, ctx := pgPool(t)
	d := NewDispatcher(pool, nil, nil, nil, time.Second)
	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, pool, "q@s.whatsapp.net", "US", mature, 100, 0)
	pool.Exec(ctx, `UPDATE account_devices SET quarantined_until=now()+interval '1 hour' WHERE account_jid='q@s.whatsapp.net'`)
	seedAccount(t, ctx, pool, "full@s.whatsapp.net", "US", mature, 100, 1000) // remaining 0
	if _, err := pickAccount(t, ctx, d, "US"); !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("err=%v, want ErrNoCapacity", err)
	}
}
