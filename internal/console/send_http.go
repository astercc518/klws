// internal/console/send_http.go
package console

import (
	"errors"
	"html"
	"math/rand"
	"net/http"
	"strings"

	"github.com/acme/wadist/internal/spintax"
)

func (s *Server) handleSendPage(w http.ResponseWriter, r *http.Request) {
	t, err := parsePage("customer_send.html")
	if err != nil {
		http.Error(w, "template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"CSRF": s.issueCSRFToken(w, r), "Error": ""})
}

func (s *Server) handleSendPreview(w http.ResponseWriter, r *http.Request) {
	body := r.FormValue("body")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// up to 5 sample expansions (deterministic seeds for stable display)
	for i := 0; i < 5; i++ {
		sample := spintax.Expand(body, rand.New(rand.NewSource(int64(i+1))))
		_, _ = w.Write([]byte("<li>" + html.EscapeString(sample) + "</li>"))
	}
}

func isValidCountryCode(c string) bool {
	if len(c) != 2 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func (s *Server) handleSendSubmit(w http.ResponseWriter, r *http.Request) {
	country := strings.ToUpper(strings.TrimSpace(r.FormValue("country")))
	if !isValidCountryCode(country) {
		s.renderSendError(w, r, "请填写有效的2位国家代码")
		return
	}
	body := r.FormValue("body")
	parsed := parseNumbers(r.FormValue("numbers"))
	kept, _, err := s.filterSuppressed(r.Context(), parsed.Valid)
	if err != nil {
		s.renderSendError(w, r, "send not configured")
		return
	}
	if len(kept) == 0 {
		s.renderSendError(w, r, "没有可发送的有效号码")
		return
	}
	if _, err := s.createCampaign(r.Context(), country, body, kept); err != nil {
		if errors.Is(err, ErrInsufficientBalance) {
			s.renderSendError(w, r, "余额不足,请先充值")
			return
		}
		if errors.Is(err, ErrNoPriceConfigured) {
			s.renderSendError(w, r, "该国家尚未配置单价,请联系销售")
			return
		}
		http.Error(w, "create campaign: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) renderSendError(w http.ResponseWriter, r *http.Request, msg string) {
	t, err := parsePage("customer_send.html")
	if err != nil {
		http.Error(w, "template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = render(w, t, map[string]any{"CSRF": s.issueCSRFToken(w, r), "Error": msg})
}
