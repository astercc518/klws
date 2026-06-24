package metrics

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server hosts /metrics (scoped to the injected registry) and /healthz.
type Server struct {
	addr string
	reg  *prometheus.Registry
	srv  *http.Server
	ln   net.Listener
}

func NewServer(addr string, reg *prometheus.Registry) *Server {
	return &Server{addr: addr, reg: reg}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// Start binds the listener synchronously (so Addr() is valid on return) and
// serves in a background goroutine.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.addr
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
