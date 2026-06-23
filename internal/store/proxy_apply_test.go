// internal/store/proxy_apply_test.go
package store

import (
	"testing"

	"go.mau.fi/whatsmeow"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestApplyProxy_EmptyBinding(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	client := whatsmeow.NewClient(m.NewDeviceStore(ctx), waLog.Noop)
	if err := ApplyProxy(client, nil); err == nil {
		t.Fatal("expected error for nil binding")
	}
	if err := ApplyProxy(client, &ProxyBinding{ProxyURL: ""}); err == nil {
		t.Fatal("expected error for empty proxy url")
	}
}

func TestApplyProxy_ValidSocks5(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	client := whatsmeow.NewClient(m.NewDeviceStore(ctx), waLog.Noop)
	b := &ProxyBinding{ProxyID: 1, ProxyURL: "socks5://user:pass@127.0.0.1:1080", ProxyType: "socks5", Country: "US"}
	if err := ApplyProxy(client, b); err != nil {
		t.Fatalf("apply valid socks5: %v", err)
	}
}
