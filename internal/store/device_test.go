// internal/store/device_test.go
package store

import (
	"context"
	"errors"
	"testing"
)

func TestGetDeviceStore_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)

	_, err := m.GetDeviceStore(ctx, "1234567890.0:0@s.whatsapp.net")
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("err = %v, want ErrDeviceNotFound", err)
	}
}

func TestGetDeviceStore_BadJID(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)

	if _, err := m.GetDeviceStore(ctx, "not-a-jid"); err == nil {
		t.Fatal("expected parse error for bad jid")
	}
}

func TestNewDeviceStore_NonNil(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)

	if d := m.NewDeviceStore(ctx); d == nil {
		t.Fatal("NewDeviceStore returned nil")
	}
}
