// internal/store/proxy_health_test.go
package store

import (
	"testing"
)

func TestReportProxyFailure_DisablesAtThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://h:1080", "US", 1)

	var dead bool
	var err error
	for i := 1; i < proxyFailureThreshold; i++ { // first threshold-1 failures: still alive
		if dead, err = m.ReportProxyFailure(ctx, pid); err != nil {
			t.Fatalf("failure %d: %v", i, err)
		}
		if dead {
			t.Fatalf("dead too early at failure %d", i)
		}
	}
	if dead, err = m.ReportProxyFailure(ctx, pid); err != nil || !dead { // threshold-th: dead
		t.Fatalf("at threshold dead=%v err=%v, want dead=true", dead, err)
	}
	var alive bool
	m.BizPool().QueryRow(ctx, `SELECT is_alive FROM proxy_pool WHERE id=$1`, pid).Scan(&alive)
	if alive {
		t.Fatal("proxy should be is_alive=false after threshold")
	}
}

func TestReportProxySuccess_ResetsAndRevives(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://h:1080", "US", 1)
	for i := 0; i < proxyFailureThreshold; i++ {
		m.ReportProxyFailure(ctx, pid)
	}
	if err := m.ReportProxySuccess(ctx, pid, 123); err != nil {
		t.Fatalf("success: %v", err)
	}
	var alive bool
	var fc, lat int
	m.BizPool().QueryRow(ctx, `SELECT is_alive, failure_count, latency_ms FROM proxy_pool WHERE id=$1`, pid).Scan(&alive, &fc, &lat)
	if !alive || fc != 0 || lat != 123 {
		t.Fatalf("after success: alive=%v fc=%d lat=%d, want true/0/123", alive, fc, lat)
	}
}

func TestListAccountsByDeadProxies(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://h:1080", "US", 1)
	seedAccount(t, ctx, m.BizPool(), 7, "live@s.whatsapp.net", "15550000001")
	if _, err := m.BindProxy(ctx, "live@s.whatsapp.net", "US"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	// kill the proxy
	for i := 0; i < proxyFailureThreshold; i++ {
		m.ReportProxyFailure(ctx, pid)
	}
	jids, err := m.ListAccountsByDeadProxies(ctx, 7, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jids) != 1 || jids[0] != "live@s.whatsapp.net" {
		t.Fatalf("dead-proxy accounts = %v, want [live@s.whatsapp.net]", jids)
	}
}
