// internal/store/manager_test.go
package store

import (
	"context"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestManager_Init_UpgradesAndPings(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, err := Init(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer m.Close()

	// whatsmeow Upgrade 应已建出会话表
	var n int
	if err := m.BizPool().QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='whatsmeow_device'`).
		Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("whatsmeow_device table count = %d, want 1", n)
	}
}
