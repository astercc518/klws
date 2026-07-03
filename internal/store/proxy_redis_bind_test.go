// internal/store/proxy_redis_bind_test.go
package store

import (
	"testing"
	"time"
)

func TestRedisProxy_BindWritesPGAndBinding(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	rdb := newTestRedis(t)
	a := newRedisProxyAllocator(rdb, 60*time.Second)

	seedAccount(t, ctx, m.BizPool(), 1, "acc1", "15550000001")
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://p", "US", 1)

	if _, err := a.rebuildFromPG(ctx, m.BizPool(), 1000); err != nil {
		t.Fatal(err)
	}

	b, err := a.bind(ctx, m.BizPool(), "acc1", "US", 1000)
	if err != nil || b == nil || b.Country != "US" || b.ProxyURL != "socks5://p" || b.ProxyID != pid {
		t.Fatalf("bind = %+v,%v", b, err)
	}

	// PG durable: account bound + counter incremented.
	var boundID *int64
	var cur, usage int
	if err := m.BizPool().QueryRow(ctx,
		`SELECT a.proxy_id, p.current_bindings, p.usage_count
		   FROM account_devices a JOIN proxy_pool p ON p.id = a.proxy_id
		  WHERE a.account_jid='acc1'`).Scan(&boundID, &cur, &usage); err != nil {
		t.Fatalf("query pg state: %v", err)
	}
	if boundID == nil || *boundID != pid || cur != 1 || usage != 1 {
		t.Fatalf("PG not durably updated: pid=%v cur=%d usage=%d", boundID, cur, usage)
	}
}

func TestRedisProxy_BindNoCapacityReturnsErrNoProxyAvailable(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	rdb := newTestRedis(t)
	a := newRedisProxyAllocator(rdb, 60*time.Second)

	seedAccount(t, ctx, m.BizPool(), 1, "acc1", "15550000001")
	// no proxies seeded / rebuilt -> hot index empty for US.
	if _, err := a.rebuildFromPG(ctx, m.BizPool(), 1000); err != nil {
		t.Fatal(err)
	}

	if _, err := a.bind(ctx, m.BizPool(), "acc1", "US", 1000); err != ErrNoProxyAvailable {
		t.Fatalf("bind err = %v; want ErrNoProxyAvailable", err)
	}
}

func TestRedisProxy_BindAccountMissingReleasesRedisPick(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	rdb := newTestRedis(t)
	a := newRedisProxyAllocator(rdb, 60*time.Second)

	// proxy exists, but no account_devices row for "ghost".
	seedProxy(t, ctx, m.BizPool(), "socks5://p", "US", 1)
	if _, err := a.rebuildFromPG(ctx, m.BizPool(), 1000); err != nil {
		t.Fatal(err)
	}

	if _, err := a.bind(ctx, m.BizPool(), "ghost", "US", 1000); err != ErrAccountMissing {
		t.Fatalf("bind err = %v; want ErrAccountMissing", err)
	}

	// PG must not have durably bumped the counter (tx rolled back).
	var cur int
	if err := m.BizPool().QueryRow(ctx, `SELECT current_bindings FROM proxy_pool WHERE country_code='US'`).Scan(&cur); err != nil {
		t.Fatalf("query current_bindings: %v", err)
	}
	if cur != 0 {
		t.Fatalf("current_bindings = %d; want 0 (tx rolled back)", cur)
	}

	// the Redis pick must have been released (capacity not leaked): a fresh
	// pick after the cooldown window should succeed again.
	if _, _, err := a.pick(ctx, "US", 1000+(60*time.Second).Milliseconds()+1); err != nil {
		t.Fatalf("pick after failed bind = %v; want proxy still available (released)", err)
	}
}

func TestRedisProxy_ReleaseBindingDecrementsAndClears(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	rdb := newTestRedis(t)
	a := newRedisProxyAllocator(rdb, 60*time.Second)

	seedAccount(t, ctx, m.BizPool(), 1, "acc1", "15550000001")
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://p", "US", 1)
	if _, err := a.rebuildFromPG(ctx, m.BizPool(), 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := a.bind(ctx, m.BizPool(), "acc1", "US", 1000); err != nil {
		t.Fatalf("bind: %v", err)
	}

	if err := a.releaseBinding(ctx, m.BizPool(), "acc1", 2000); err != nil {
		t.Fatalf("releaseBinding: %v", err)
	}

	var boundID *int64
	var cur int
	if err := m.BizPool().QueryRow(ctx,
		`SELECT proxy_id FROM account_devices WHERE account_jid='acc1'`).Scan(&boundID); err != nil {
		t.Fatalf("query account: %v", err)
	}
	if boundID != nil {
		t.Fatalf("account still bound after release: %v", *boundID)
	}
	if err := m.BizPool().QueryRow(ctx, `SELECT current_bindings FROM proxy_pool WHERE id=$1`, pid).Scan(&cur); err != nil {
		t.Fatalf("query proxy: %v", err)
	}
	if cur != 0 {
		t.Fatalf("current_bindings = %d; want 0", cur)
	}

	// idempotent: calling again on an already-unbound account is a no-op, not an error.
	if err := a.releaseBinding(ctx, m.BizPool(), "acc1", 3000); err != nil {
		t.Fatalf("second releaseBinding (idempotent) = %v; want nil", err)
	}
}
