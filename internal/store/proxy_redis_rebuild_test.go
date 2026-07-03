// internal/store/proxy_redis_rebuild_test.go
package store

import (
	"testing"
)

func TestRedisProxy_RebuildFromPG(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	rdb := newTestRedis(t)
	a := newRedisProxyAllocator(rdb, 60_000)

	// two US proxies: one alive w/ free slot, one dead.
	_, err := m.BizPool().Exec(ctx, `INSERT INTO proxy_pool (proxy_url,proxy_type,country_code,is_alive,max_bindings,current_bindings) VALUES
		('socks5://alive','socks5','US',true,2,0), ('socks5://dead','socks5','US',false,1,0)`)
	if err != nil {
		t.Fatalf("seed proxies: %v", err)
	}

	n, err := a.rebuildFromPG(ctx, m.BizPool(), 5000)
	if err != nil || n != 1 {
		t.Fatalf("rebuild = %d,%v; want 1 (only alive)", n, err)
	}
	id, meta, err := a.pick(ctx, "US", 5001)
	if err != nil || meta == "" || id == 0 {
		t.Fatalf("pick after rebuild: %d,%q,%v", id, meta, err)
	}
}
