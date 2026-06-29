// internal/api/server.go
package api

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Start binds the listener synchronously (so Addr() is known immediately, which
// the smoke test relies on) then serves in the background. Mirrors
// console.Server.Start for a drop-in lifecycle.
func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s.Router(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	s.ready.Store(true)
	return nil
}

// Addr returns the bound address (with the OS-chosen port resolved when :0).
func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return ""
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
