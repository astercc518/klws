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
	// empty
	if w, a := buildDeviceWhere(deviceFilter{}); w != "" || len(a) != 0 {
		t.Errorf("empty: w=%q a=%v", w, a)
	}
	// q hits three columns with one param
	w, a := buildDeviceWhere(deviceFilter{Q: "abc"})
	if !strings.Contains(w, "account_jid ILIKE $1") || !strings.Contains(w, "phone_number ILIKE $1") || !strings.Contains(w, "array_to_string(tags,' ') ILIKE $1") {
		t.Errorf("q where = %q", w)
	}
	if len(a) != 1 || a[0] != "%abc%" {
		t.Errorf("q args = %v", a)
	}
	// combined: q + ban_status + online=true; online adds no arg
	on := true
	w, a = buildDeviceWhere(deviceFilter{Q: "x", BanStatus: "banned", Online: &on})
	if !strings.Contains(w, "ban_status::text = $2") || !strings.Contains(w, "owner_node IS NOT NULL") {
		t.Errorf("combined where = %q", w)
	}
	if len(a) != 2 || a[1] != "banned" {
		t.Errorf("combined args = %v", a)
	}
	// online=false → IS NULL
	off := false
	w, _ = buildDeviceWhere(deviceFilter{Online: &off})
	if !strings.Contains(w, "owner_node IS NULL") {
		t.Errorf("offline where = %q", w)
	}
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
