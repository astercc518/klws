// internal/console/dashboard_nav_test.go
package console

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The staff landing page (GET /) must give each role a navigation entry to its
// feature area, otherwise login lands on a dead-end with no way in.
func TestDashboardShowsRoleNav(t *testing.T) {
	mgr := newTestManager(t)
	repo := NewUserRepo(mgr.SystemPool())
	hash, _ := HashPassword("pw123456")
	if _, err := repo.Create(t.Context(), "admin@acme.test", hash, RoleAdmin, nil); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if _, err := repo.Create(t.Context(), "sales@acme.test", hash, RoleSales, nil); err != nil {
		t.Fatalf("seed sales: %v", err)
	}
	srv, _ := NewServer(testConfig(), repo, NewSessionStore(newTestRedis(t), time.Hour), mgr)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	adminBody := loginAndGetRoot(t, ts, "admin@acme.test", "pw123456")
	if !strings.Contains(adminBody, `href="/admin"`) {
		t.Errorf("admin dashboard missing nav link to /admin; body=%s", adminBody)
	}

	salesBody := loginAndGetRoot(t, ts, "sales@acme.test", "pw123456")
	if !strings.Contains(salesBody, `href="/sales"`) {
		t.Errorf("sales dashboard missing nav link to /sales; body=%s", salesBody)
	}
}

// loginAndGetRoot logs a user in with a fresh cookie jar and returns the body of
// the authenticated landing page (GET /).
func loginAndGetRoot(t *testing.T, ts *httptest.Server, email, pw string) string {
	t.Helper()
	jar, _ := newJar()
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	tok := csrfFor(t, client, ts, "/login")
	resp, err := client.PostForm(ts.URL+"/login", url.Values{"email": {email}, "password": {pw}, csrfFormField: {tok}})
	if err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("login %s: status=%d loc=%s", email, resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, err = client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("get / as %s: %v", email, err)
	}
	return readAll(t, resp)
}
