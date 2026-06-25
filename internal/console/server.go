// internal/console/server.go
package console

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/pricing"
	"github.com/acme/wadist/internal/store"
)

// Server is the console HTTP server. It mirrors metrics.Server's lifecycle
// (synchronous bind, background serve, SetReady drain) for consistency.
type Server struct {
	cfg      Config
	users    *UserRepo
	sessions *SessionStore
	mgr      *store.Manager
	billing  *billing.Repo
	tenants  *TenantRepo
	pricing  *pricing.Repo
	blindKey []byte
	srv      *http.Server
	ln       net.Listener
	ready    atomic.Bool
}

// WithBilling wires a billing.Repo into the server (for admin handlers). Chainable.
func (s *Server) WithBilling(b *billing.Repo) *Server { s.billing = b; return s }

// WithTenants wires a TenantRepo into the server (for admin handlers). Chainable.
func (s *Server) WithTenants(t *TenantRepo) *Server { s.tenants = t; return s }

// WithPricing wires a pricing.Repo into the server (for admin handlers). Chainable.
func (s *Server) WithPricing(p *pricing.Repo) *Server { s.pricing = p; return s }

func NewServer(cfg Config, users *UserRepo, sessions *SessionStore, mgr *store.Manager) (*Server, error) {
	return &Server{cfg: cfg, users: users, sessions: sessions, mgr: mgr}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.ready.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("draining"))
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(mustStaticSub()))))
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.requireCSRF(s.handleLoginSubmit))
	mux.HandleFunc("POST /logout", s.requireAuth(s.requireCSRF(s.handleLogout)))
	mux.HandleFunc("GET /{$}", s.requireAuth(s.handleDashboard))
	custOnly := s.requireRole(RoleCustomer)
	mux.HandleFunc("GET /account", s.requireAuth(custOnly(s.handleCustomerAccount)))
	mux.HandleFunc("GET /send", s.requireAuth(custOnly(s.handleSendPage)))
	mux.HandleFunc("POST /send/preview", s.requireCSRF(s.requireAuth(custOnly(s.handleSendPreview))))
	mux.HandleFunc("POST /send", s.requireCSRF(s.requireAuth(custOnly(s.handleSendSubmit))))
	adminOnly := s.requireRole(RoleAdmin)
	mux.HandleFunc("GET /admin", s.requireAuth(adminOnly(s.handleAdminTenants)))
	mux.HandleFunc("GET /admin/tenant/{id}", s.requireAuth(adminOnly(s.handleAdminTenant)))
	mux.HandleFunc("POST /admin/tenant/{id}/recharge", s.requireAuth(adminOnly(s.requireCSRF(s.handleAdminRecharge))))
	mux.HandleFunc("POST /admin/tenant/{id}/pricing", s.requireAuth(adminOnly(s.requireCSRF(s.handleAdminSetPrice))))
	return mux
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	email := r.FormValue("email")
	password := r.FormValue("password")
	user, err := s.users.Authenticate(r.Context(), email, password)
	if err != nil {
		t, perr := parsePage("login.html")
		if perr != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = render(w, t, map[string]any{"Error": "邮箱或密码错误", "CSRF": s.issueCSRFToken(w, r)})
		return
	}
	sid, err := s.sessions.Create(r.Context(), SessionData{UserID: user.ID, Role: user.Role, TenantID: user.TenantID})
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    SignCookie(s.cfg.SessionKey, sid),
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cfg.CookieName); err == nil {
		if sid, ok := VerifyCookie(s.cfg.SessionKey, c.Value); ok {
			_ = s.sessions.Destroy(r.Context(), sid)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data, _ := sessionFrom(r.Context())
	if data.Role == RoleCustomer {
		bal, frozen, err := s.customerBalance(r.Context())
		if err != nil {
			http.Error(w, "load balance", http.StatusInternalServerError)
			return
		}
		camps, err := s.customerCampaigns(r.Context(), 50)
		if err != nil {
			http.Error(w, "load campaigns", http.StatusInternalServerError)
			return
		}
		t, err := parsePage("customer_dashboard.html")
		if err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = render(w, t, map[string]any{"Balance": bal, "Frozen": frozen, "Campaigns": camps, "CSRF": s.issueCSRFToken(w, r)})
		return
	}
	// staff: existing dashboard
	t, err := parsePage("dashboard.html")
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"Role": string(data.Role), "CSRF": s.issueCSRFToken(w, r)})
}

func (s *Server) handleCustomerAccount(w http.ResponseWriter, r *http.Request) {
	bal, frozen, err := s.customerBalance(r.Context())
	if err != nil {
		http.Error(w, "load balance", http.StatusInternalServerError)
		return
	}
	ledger, err := s.customerLedger(r.Context(), 100)
	if err != nil {
		http.Error(w, "load ledger", http.StatusInternalServerError)
		return
	}
	t, err := parsePage("customer_account.html")
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"Balance": bal, "Frozen": frozen, "Ledger": ledger})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	t, err := parsePage("login.html")
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"Error": "", "CSRF": s.issueCSRFToken(w, r)})
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	s.ready.Store(true)
	return nil
}

func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.cfg.Addr
}

func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
