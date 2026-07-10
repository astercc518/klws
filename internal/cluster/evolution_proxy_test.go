package cluster

import (
	"testing"

	"github.com/acme/wadist/internal/store"
)

func TestProxyFromBinding(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		wantOK bool
		host   string
		port   int
		proto  string
		user   string
		pass   string
	}{
		{"socks5 with auth", "socks5://u1:p1@1.2.3.4:1080", true, "1.2.3.4", 1080, "socks5", "u1", "p1"},
		{"http no auth", "http://10.0.0.1:8080", true, "10.0.0.1", 8080, "http", "", ""},
		{"empty url", "", false, "", 0, "", "", ""},
		{"garbage", "://nope", false, "", 0, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b *store.ProxyBinding
			if c.url != "" || c.name == "empty url" {
				b = &store.ProxyBinding{ProxyURL: c.url}
			}
			got, ok := proxyFromBinding(b)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if got.Host != c.host || got.Port != c.port || got.Protocol != c.proto ||
				got.Username != c.user || got.Password != c.pass {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestProxyFromBinding_Nil(t *testing.T) {
	if _, ok := proxyFromBinding(nil); ok {
		t.Fatal("nil binding must be ok=false")
	}
}
