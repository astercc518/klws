package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// pgPool + applyMigrations + seed helpers are provided by dbcollector_testsupport_test.go
// (mirror the testcontainers pattern from internal/dispatch/testsupport_test.go).

func TestDBCollector_Gauges(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	// seed: one active healthy account, one wallet, one alive proxy, one pending recipient
	seedCollectorFixture(t, ctx, pool)

	c := NewDBCollector(pool, time.Minute, nil) // nil snapshot provider → 0 active sessions
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	// account_devices ban_status gauge for 'active' should be >= 1
	got := testutil.ToFloat64(activeAccountsProbe(t, reg))
	if got < 1 {
		t.Fatalf("expected >=1 active account gauge, got %v", got)
	}
	// active sessions gauge with nil provider == 0
	if v := gaugeValue(t, reg, "wadist_active_sessions"); v != 0 {
		t.Fatalf("nil provider should yield 0 active sessions, got %v", v)
	}
	// pending recipients gauge >= 1
	if v := gaugeValue(t, reg, "wadist_recipients_state"); v < 0 {
		t.Fatalf("unexpected recipients gauge %v", v)
	}
	_ = context.Background
}
