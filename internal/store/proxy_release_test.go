// internal/store/proxy_release_test.go
package store

import (
	"testing"
)

func TestReleaseProxy_DecrementsAndIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://h:1080", "US", 1)
	seedAccount(t, ctx, m.BizPool(), 1, "111@s.whatsapp.net", "15550000001")
	if _, err := m.BindProxy(ctx, "111@s.whatsapp.net", "US"); err != nil {
		t.Fatalf("bind: %v", err)
	}

	if err := m.ReleaseProxy(ctx, "111@s.whatsapp.net"); err != nil {
		t.Fatalf("release: %v", err)
	}
	assertReleased := func() {
		var cur int
		var pidNull *int64
		m.BizPool().QueryRow(ctx, `SELECT current_bindings FROM proxy_pool WHERE id=$1`, pid).Scan(&cur)
		m.BizPool().QueryRow(ctx, `SELECT proxy_id FROM account_devices WHERE account_jid='111@s.whatsapp.net'`).Scan(&pidNull)
		if cur != 0 {
			t.Fatalf("current_bindings = %d, want 0", cur)
		}
		if pidNull != nil {
			t.Fatalf("account proxy_id = %v, want NULL", *pidNull)
		}
	}
	assertReleased()

	// idempotent: second release is a no-op, must not error or go negative
	if err := m.ReleaseProxy(ctx, "111@s.whatsapp.net"); err != nil {
		t.Fatalf("second release: %v", err)
	}
	assertReleased()
}
