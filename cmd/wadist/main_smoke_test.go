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
	_ = time.Second
}
