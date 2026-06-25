// internal/console/server.go
package console

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/acme/wadist/internal/store"
)

// Server is the console HTTP server. It mirrors metrics.Server's lifecycle
// (synchronous bind, background serve, SetReady drain) for consistency.
type Server struct {
	cfg      Config
	users    *UserRepo
	sessions *SessionStore
	mgr      *store.Manager
	srv      *http.Server
	ln       net.Listener
	ready    atomic.Bool
}

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
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("GET /{$}", s.requireAuth(s.handleDashboard))
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
		_ = render(w, t, map[string]any{"Error": "邮箱或密码错误"})
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
	t, err := parsePage("dashboard.html")
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"Role": string(data.Role)})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, _ *http.Request) {
	t, err := parsePage("login.html")
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"Error": ""})
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
