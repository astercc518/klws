package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestServer_Handler_MetricsAndHealthz(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.RecordSend("sent")
	s := NewServer(":0", reg)

	// /healthz
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthz: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// /metrics contains our metric
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "wadist_send_outcomes_total") {
		t.Fatalf("metrics: code=%d missing series", rec.Code)
	}
}

func TestServer_Readyz_ReflectsState(t *testing.T) {
	reg := prometheus.NewRegistry()
	New(reg)
	s := NewServer(":0", reg)

	// default NOT ready → 503
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 503 {
		t.Fatalf("default readyz want 503, got %d", rec.Code)
	}
	s.SetReady(true)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ready") {
		t.Fatalf("ready readyz want 200/ready, got %d %q", rec.Code, rec.Body.String())
	}
	s.SetReady(false)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 503 {
		t.Fatalf("draining readyz want 503, got %d", rec.Code)
	}
	// /healthz stays 200 regardless of readiness
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("healthz must stay 200, got %d", rec.Code)
	}
}

func TestServer_StartShutdown(t *testing.T) {
	reg := prometheus.NewRegistry()
	New(reg)
	s := NewServer("127.0.0.1:0", reg)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + s.Addr() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.TrimSpace(string(b)) != "ok" {
		t.Fatalf("live healthz: %d %q", resp.StatusCode, b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
