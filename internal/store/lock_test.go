// internal/store/lock_test.go
package store

import (
	"context"
	"errors"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestAcquireDeviceLock_MutualExclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, err := newManager(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	defer m.Close()

	const jid = "1234567890.0:0@s.whatsapp.net"

	l1, err := m.AcquireDeviceLock(ctx, jid)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !l1.Healthy(ctx) {
		t.Fatal("lock should be healthy while held")
	}

	// 第二次抢同一 jid:不同连接 → 抢不到
	if _, err := m.AcquireDeviceLock(ctx, jid); !errors.Is(err, ErrDeviceLocked) {
		t.Fatalf("second acquire err = %v, want ErrDeviceLocked", err)
	}

	// 释放后可再抢
	l1.Release(ctx)
	l2, err := m.AcquireDeviceLock(ctx, jid)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	l2.Release(ctx)
}
