package api

import (
	"strings"
	"testing"
)

func TestClampPage(t *testing.T) {
	cases := []struct{ n, def, max, want int }{
		{0, 20, 200, 20}, {-5, 20, 200, 20}, {50, 20, 200, 50}, {999, 20, 200, 200}, {200, 20, 200, 200},
	}
	for _, c := range cases {
		if got := clampPage(c.n, c.def, c.max); got != c.want {
			t.Errorf("clampPage(%d,%d,%d)=%d want %d", c.n, c.def, c.max, got, c.want)
		}
	}
}

func TestBuildDeviceWhere(t *testing.T) {
	owned := []string{"a0@wa", "a1@wa"}
	// empty
	if w, a := buildDeviceWhere(deviceFilter{}, owned); w != "" || len(a) != 0 {
		t.Errorf("empty: w=%q a=%v", w, a)
	}
	// q hits three columns with one param
	w, a := buildDeviceWhere(deviceFilter{Q: "abc"}, owned)
	if !strings.Contains(w, "account_jid ILIKE $1") || !strings.Contains(w, "phone_number ILIKE $1") || !strings.Contains(w, "array_to_string(tags,' ') ILIKE $1") {
		t.Errorf("q where = %q", w)
	}
	if len(a) != 1 || a[0] != "%abc%" {
		t.Errorf("q args = %v", a)
	}
	// combined: q + ban_status + online=true; online adds the redis-derived jid set as one arg
	on := true
	w, a = buildDeviceWhere(deviceFilter{Q: "x", BanStatus: "banned", Online: &on}, owned)
	if !strings.Contains(w, "ban_status::text = $2") || !strings.Contains(w, "account_jid = ANY($3)") {
		t.Errorf("combined where = %q", w)
	}
	if len(a) != 3 || a[1] != "banned" {
		t.Errorf("combined args = %v", a)
	}
	jids, ok := a[2].([]string)
	if !ok || len(jids) != 2 {
		t.Errorf("combined ownedJIDs arg = %v", a[2])
	}
	// online=false → NOT (... = ANY(...))
	off := false
	w, a = buildDeviceWhere(deviceFilter{Online: &off}, owned)
	if !strings.Contains(w, "NOT (account_jid = ANY($1))") {
		t.Errorf("offline where = %q", w)
	}
	// nil ownedJIDs (nothing currently owned) must not become a SQL NULL array —
	// NULL would make ANY(NULL)/NOT(...) evaluate to NULL and match nothing.
	w, a = buildDeviceWhere(deviceFilter{Online: &off}, nil)
	if jids, ok := a[0].([]string); !ok || jids == nil {
		t.Errorf("nil ownedJIDs must be normalized to non-nil empty slice, got %v", a[0])
	}
	_ = w
}

func TestBuildProxyWhere(t *testing.T) {
	if w, a := buildProxyWhere(proxyFilter{}); w != "" || len(a) != 0 {
		t.Errorf("empty: w=%q a=%v", w, a)
	}
	alive := true
	w, a := buildProxyWhere(proxyFilter{Q: "us", Alive: &alive, ProxyType: "socks5"})
	if !strings.Contains(w, "p.proxy_url ILIKE $1") || !strings.Contains(w, "p.is_alive = $2") || !strings.Contains(w, "p.proxy_type::text = $3") {
		t.Errorf("where = %q", w)
	}
	if len(a) != 3 || a[0] != "%us%" || a[1] != true || a[2] != "socks5" {
		t.Errorf("args = %v", a)
	}
}
