// internal/console/csrf.go
package console

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
)

const (
	csrfCookieName = "wadist_csrf"
	csrfFormField  = "csrf"
	csrfHeader     = "X-CSRF-Token"
)

// issueCSRFToken returns the request's CSRF token, minting and setting the
// cookie if the request doesn't already carry one. Safe to call on any GET that
// renders a form. The token is a non-secret double-submit value: defense rests
// on same-origin policy preventing a cross-site page from reading/forging it.
func (s *Server) issueCSRFToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	return tok
}

// requireCSRF rejects a POST whose submitted token (form field or header) does
// not match the cookie. Wrap every state-mutating POST handler with this.
func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(csrfCookieName)
		if err != nil || c.Value == "" {
			http.Error(w, "missing CSRF cookie", http.StatusForbidden)
			return
		}
		sent := r.FormValue(csrfFormField)
		if sent == "" {
			sent = r.Header.Get(csrfHeader)
		}
		if sent == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(c.Value)) != 1 {
			http.Error(w, "bad CSRF token", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
