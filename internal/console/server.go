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
	return mux
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
