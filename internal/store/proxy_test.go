// internal/store/proxy_test.go
package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	wlog "github.com/acme/wadist/internal/log"
)

// newManagerWithSchema builds a schema-applied Manager wired to a real Redis
// (the sole proxy allocation backend). Migrations are applied BEFORE
// constructing the Manager: newManager's boot-time rebuildFromPG queries
// proxy_pool immediately, and that table must already exist.
func newManagerWithSchema(t *testing.T) (*Manager, context.Context) {
	t.Helper()
	ctx := context.Background()
	dsn := testDSN(t)

	migPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("migration pool: %v", err)
	}
	applyMigrations(t, ctx, migPool)
	migPool.Close()

	rdb := newTestRedis(t)
	m, err := newManager(ctx, Config{DSN: dsn, Redis: rdb, BadgerDir: t.TempDir()}, wlog.Noop)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(m.Close)
	return m, ctx
}

func TestBindProxy_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://u:p@h:1080", "US", 1)
	seedAccount(t, ctx, m.BizPool(), 1, "111@s.whatsapp.net", "15550000001")
	if _, err := m.proxyAlloc.rebuildFromPG(ctx, m.BizPool(), nowMsForTest()); err != nil {
		t.Fatalf("rebuildFromPG: %v", err)
	}

	b, err := m.BindProxy(ctx, "111@s.whatsapp.net", "US")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if b.ProxyID != pid || b.ProxyURL != "socks5://u:p@h:1080" || b.Country != "US" {
		t.Fatalf("binding wrong: %+v", b)
	}

	var cur, usage int
	var boundID int64
	m.BizPool().QueryRow(ctx, `SELECT current_bindings, usage_count FROM proxy_pool WHERE id=$1`, pid).Scan(&cur, &usage)
	if cur != 1 || usage != 1 {
		t.Fatalf("counts: current=%d usage=%d, want 1/1", cur, usage)
	}
	m.BizPool().QueryRow(ctx, `SELECT proxy_id FROM account_devices WHERE account_jid='111@s.whatsapp.net'`).Scan(&boundID)
	if boundID != pid {
		t.Fatalf("account proxy_id = %d, want %d", boundID, pid)
	}
}

func TestBindProxy_NoCapacity(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	seedAccount(t, ctx, m.BizPool(), 1, "111@s.whatsapp.net", "15550000001")
	// no proxies seeded for US
	if _, err := m.BindProxy(ctx, "111@s.whatsapp.net", "US"); !errors.Is(err, ErrNoProxyAvailable) {
		t.Fatalf("err = %v, want ErrNoProxyAvailable", err)
	}
}

func TestBindProxy_NoOversellUnderConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	// 3 proxies, each capacity 1 → at most 3 accounts can bind
	for i := 0; i < 3; i++ {
		seedProxy(t, ctx, m.BizPool(), "socks5://h:"+string(rune('a'+i)), "US", 1)
	}
	if _, err := m.proxyAlloc.rebuildFromPG(ctx, m.BizPool(), nowMsForTest()); err != nil {
		t.Fatalf("rebuildFromPG: %v", err)
	}
	const n = 10
	jids := make([]string, n)
	for i := 0; i < n; i++ {
		jids[i] = "acc" + string(rune('A'+i)) + "@s.whatsapp.net"
		seedAccount(t, ctx, m.BizPool(), 1, jids[i], "1555000000"+string(rune('0'+i)))
	}

	var success int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(jid string) {
			defer wg.Done()
			if _, err := m.BindProxy(ctx, jid, "US"); err == nil {
				atomic.AddInt64(&success, 1)
			}
		}(jids[i])
	}
	wg.Wait()

	if success != 3 {
		t.Fatalf("successful binds = %d, want exactly 3 (capacity)", success)
	}
	// no proxy oversold
	var maxCur int
	m.BizPool().QueryRow(ctx, `SELECT COALESCE(max(current_bindings),0) FROM proxy_pool`).Scan(&maxCur)
	if maxCur > 1 {
		t.Fatalf("oversell detected: max current_bindings = %d", maxCur)
	}
}
