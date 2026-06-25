// internal/console/server_test.go
package console

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Addr:         "127.0.0.1:0",
		SessionKey:   []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL:   time.Hour,
		CookieName:   "wadist_session",
		CookieSecure: false,
	}
}

func TestServerLoginPageAndHealth(t *testing.T) {
	mgr := newTestManager(t)
	srv, err := NewServer(testConfig(), NewUserRepo(mgr.SystemPool()), NewSessionStore(newTestRedis(t), time.Hour), mgr)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// /healthz
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// /login renders the form
	resp, err = http.Get(ts.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `action="/login"`) {
		t.Fatalf("login page not rendered: %d\n%s", resp.StatusCode, b)
	}

	// static asset served
	resp, err = http.Get(ts.URL + "/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("static css: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
