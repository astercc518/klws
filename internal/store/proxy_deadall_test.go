// internal/store/proxy_deadall_test.go
package store

import (
	"testing"
)

func TestListDeadProxyAccountsAll_CrossTenant(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)

	// 两个租户各一活跃账号，绑同一代理。
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://u:p@h:1080", "US", 5)
	seedAccount(t, ctx, m.BizPool(), 1, "t1@s.whatsapp.net", "15550000001")
	seedAccount(t, ctx, m.BizPool(), 2, "t2@s.whatsapp.net", "15550000002")
	if _, err := m.BindProxy(ctx, "t1@s.whatsapp.net", "US"); err != nil {
		t.Fatalf("bind t1: %v", err)
	}
	if _, err := m.BindProxy(ctx, "t2@s.whatsapp.net", "US"); err != nil {
		t.Fatalf("bind t2: %v", err)
	}

	// 代理仍活着 → 不应返回任何账号。
	got, err := m.ListDeadProxyAccountsAll(ctx, 100)
	if err != nil {
		t.Fatalf("list (alive): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("alive proxy: got %v want none", got)
	}

	// 打死代理。
	if _, err := m.BizPool().Exec(ctx, `UPDATE proxy_pool SET is_alive=FALSE WHERE id=$1`, pid); err != nil {
		t.Fatalf("kill proxy: %v", err)
	}

	got, err = m.ListDeadProxyAccountsAll(ctx, 100)
	if err != nil {
		t.Fatalf("list (dead): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("dead proxy: got %d accounts want 2 (cross-tenant): %+v", len(got), got)
	}
	for _, a := range got {
		if a.CountryCode != "US" {
			t.Fatalf("cc=%q want US for %+v", a.CountryCode, a)
		}
	}
}
