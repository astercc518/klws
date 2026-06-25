// cmd/console/main_smoke_test.go
package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestRun_BootsLogin requires a live PG + Redis (WADIST_POSTGRES_DSN set) and
// skips otherwise, matching cmd/wadist's smoke test convention.
// Note: since the RLS guard was added, booting also requires WADIST_APP_TENANT_DSN
// and WADIST_APP_SYSTEM_DSN to be set; include them when running this test live.
func TestRun_BootsLogin(t *testing.T) {
	if testing.Short() || os.Getenv("WADIST_POSTGRES_DSN") == "" {
		t.Skip("integration: set WADIST_POSTGRES_DSN (+ WADIST_REDIS_ADDR + WADIST_APP_TENANT_DSN + WADIST_APP_SYSTEM_DSN)")
	}
	t.Setenv("WADIST_CONSOLE_ADDR", "127.0.0.1:0")
	t.Setenv("WADIST_CONSOLE_SESSION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=") // 32 bytes b64
	t.Setenv("WADIST_CONSOLE_COOKIE_SECURE", "false")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, stop, err := run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer stop()

	resp, err := http.Get("http://" + srv.Addr() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `action="/login"`) {
		t.Fatalf("login not served: %d\n%s", resp.StatusCode, b)
	}
}
