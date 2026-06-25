// internal/console/csrf_test.go
package console

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCSRFBlocksPostWithoutToken(t *testing.T) {
	srv, _ := NewServer(testConfig(), nil, nil, nil)
	h := srv.requireCSRF(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// no cookie, no field → 403
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/x", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing token: want 403, got %d", rec.Code)
	}

	// matching cookie + field → pass
	form := url.Values{csrfFormField: {"tok-123"}}
	req := httptest.NewRequest("POST", "/x", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "tok-123"})
	rec2 := httptest.NewRecorder()
	h(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("matching token: want 200, got %d", rec2.Code)
	}

	// cookie present but field mismatched → 403
	req3 := httptest.NewRequest("POST", "/x", strings.NewReader(url.Values{csrfFormField: {"wrong"}}.Encode()))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req3.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "tok-123"})
	rec3 := httptest.NewRecorder()
	h(rec3, req3)
	if rec3.Code != http.StatusForbidden {
		t.Fatalf("mismatched token: want 403, got %d", rec3.Code)
	}
}

func TestIssueCSRFTokenSetsCookieOnce(t *testing.T) {
	srv, _ := NewServer(testConfig(), nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/login", nil)
	tok := srv.issueCSRFToken(rec, req)
	if tok == "" {
		t.Fatal("token must be non-empty")
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), csrfCookieName) {
		t.Fatalf("cookie not set: %q", rec.Header().Get("Set-Cookie"))
	}
}
