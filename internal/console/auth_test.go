// internal/console/auth_test.go
package console

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLoginLogoutFlow(t *testing.T) {
	mgr := newTestManager(t)
	repo := NewUserRepo(mgr.SystemPool())
	hash, _ := HashPassword("pw12345")
	if _, err := repo.Create(t.Context(), "admin@acme.test", hash, RoleAdmin, nil); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	srv, _ := NewServer(testConfig(), repo, NewSessionStore(newTestRedis(t), time.Hour), mgr)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	jar, _ := newJar()
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	// Unauthenticated dashboard → 302 to /login.
	resp, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/login" {
		t.Fatalf("unauth dashboard: status=%d loc=%s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp.Body.Close()

	// Bad credentials → 200 with error, no cookie.
	tok := csrfFor(t, client, ts, "/login")
	resp, err = client.PostForm(ts.URL+"/login", url.Values{"email": {"admin@acme.test"}, "password": {"wrong"}, csrfFormField: {tok}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bad login status: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// CSRF cookie is set, but no session cookie must exist yet.
	u, _ := url.Parse(ts.URL)
	for _, c := range jar.Cookies(u) {
		if c.Name == "wadist_session" {
			t.Fatalf("bad login must not set a session cookie, got %v", jar.Cookies(u))
		}
	}

	// Good credentials → 302 to / and a session cookie set.
	tok2 := csrfFor(t, client, ts, "/login")
	resp, err = client.PostForm(ts.URL+"/login", url.Values{"email": {"admin@acme.test"}, "password": {"pw12345"}, csrfFormField: {tok2}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("good login: status=%d loc=%s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp.Body.Close()

	// Authenticated dashboard → 200 showing role.
	resp, err = client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "admin") {
		t.Fatalf("auth dashboard: status=%d body=%s", resp.StatusCode, body)
	}

	// Logout → 302 to /login; dashboard again unauthenticated.
	logoutTok := csrfFor(t, client, ts, "/")
	resp, err = client.PostForm(ts.URL+"/logout", url.Values{csrfFormField: {logoutTok}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("after logout dashboard should redirect, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRequireRoleForbidsWrongRole(t *testing.T) {
	srv, _ := NewServer(testConfig(), nil, nil, nil)
	h := srv.requireRole(RoleAdmin)(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// inject a customer session directly
	req := httptest.NewRequest("GET", "/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), sessionCtxKey, &SessionData{UserID: 1, Role: RoleCustomer, TenantID: ptrInt64(9)}))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("customer hitting admin-only route: want 403, got %d", rec.Code)
	}
}

func TestRequireRoleForbidsMissingSession(t *testing.T) {
	srv, _ := NewServer(testConfig(), nil, nil, nil)
	h := srv.requireRole(RoleAdmin)(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest("GET", "/x", nil) // no session in context
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing session on role-gated route: want 403, got %d", rec.Code)
	}
}

func newJar() (*cookiejar.Jar, error) { return cookiejar.New(nil) }

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
