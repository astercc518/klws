package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRun_BootsMetrics requires a live PG + Redis reachable via environment
// variables. When WADIST_POSTGRES_DSN is not set the test skips gracefully;
// the critical guarantee is that run() compiles and the /metrics endpoint is
// reachable when a real DB is available.
func TestRun_BootsMetrics(t *testing.T) {
	if testing.Short() || os.Getenv("WADIST_POSTGRES_DSN") == "" {
		t.Skip("integration: set WADIST_POSTGRES_DSN")
	}
	// Keep stop() fast: default PreStopDelay is 5s, which slows the smoke test.
	// 1ms is a legal positive value so the <=0→5s fallback doesn't apply.
	t.Setenv("WADIST_PRESTOP_DELAY", "1ms")
	cfg := loadForTest(t) // config.Load() with MetricsAddr set to 127.0.0.1:0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, stop, err := run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer stop()

	resp, err := http.Get("http://" + srv.Addr() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "wadist_") {
		t.Fatalf("metrics not served: %d\nbody: %s", resp.StatusCode, b)
	}
}

// TestRun_PumpMode_BootsAndStopsCleanly exercises the pump send-path wiring
// (reclaim-at-boot, PumpEnqueuer, Pump, and the filler loop) — pump is now
// the sole send driver, wired unconditionally in run(). Like
// TestRun_BootsMetrics it needs a live PG + Redis, so it skips gracefully
// without WADIST_POSTGRES_DSN. The full send-path behavior (a payload
// actually flowing PumpEnqueuer -> Pump -> ProcessSend) is Task 6's
// integration test; this smoke test's job is narrower: prove run() boots
// and that stop() — which drives the filler's lctx.Done() -> pe.Close() ->
// pump.Run() drain — returns cleanly within the test's own deadline (no
// goroutine leak/hang).
func TestRun_PumpMode_BootsAndStopsCleanly(t *testing.T) {
	if testing.Short() || os.Getenv("WADIST_POSTGRES_DSN") == "" {
		t.Skip("integration: set WADIST_POSTGRES_DSN")
	}
	t.Setenv("WADIST_PRESTOP_DELAY", "1ms")
	cfg := loadForTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, stop, err := run(ctx, cfg)
	if err != nil {
		t.Fatalf("run (pump mode): %v", err)
	}

	resp, err := http.Get("http://" + srv.Addr() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "wadist_") {
		t.Fatalf("metrics not served in pump mode: %d\nbody: %s", resp.StatusCode, b)
	}

	// stop() must return promptly: it drives StopIntake (asynqSrv.Shutdown,
	// takeover only) then cancels the filler + pump.Run loops and waits for
	// both — the filler closes pe on its own lctx.Done(), pump.Run drains the
	// (empty, in this smoke test) channel and returns. A hang here would mean
	// the shutdown-ordering contract is broken (e.g. a second/concurrent
	// pe.Close(), or pump.Run never observing the close).
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stop() did not return within 10s in pump mode — possible shutdown deadlock")
	}
}
