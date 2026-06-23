// internal/store/device_test.go
package store

import (
	"context"
	"errors"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestGetDeviceStore_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, err := newManager(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	defer m.Close()

	_, err = m.GetDeviceStore(ctx, "1234567890.0:0@s.whatsapp.net")
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("err = %v, want ErrDeviceNotFound", err)
	}
}

func TestGetDeviceStore_BadJID(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, _ := newManager(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	defer m.Close()

	if _, err := m.GetDeviceStore(ctx, "not-a-jid"); err == nil {
		t.Fatal("expected parse error for bad jid")
	}
}

func TestNewDeviceStore_NonNil(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, _ := newManager(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	defer m.Close()

	if d := m.NewDeviceStore(ctx); d == nil {
		t.Fatal("NewDeviceStore returned nil")
	}
}
