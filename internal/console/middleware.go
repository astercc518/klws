// internal/console/middleware.go
package console

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type ctxKey int

const sessionCtxKey ctxKey = 0

func sessionFrom(ctx context.Context) (*SessionData, bool) {
	d, ok := ctx.Value(sessionCtxKey).(*SessionData)
	return d, ok
}

// requireAuth loads the session from the signed cookie into the request context,
// or redirects to /login.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.cfg.CookieName)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		sid, ok := VerifyCookie(s.cfg.SessionKey, c.Value)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		data, err := s.sessions.Get(r.Context(), sid)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), sessionCtxKey, data)
		next(w, r.WithContext(ctx))
	}
}

// requireRole returns middleware that 403s unless the session role is allowed.
// It must wrap a handler already behind requireAuth.
func (s *Server) requireRole(roles ...Role) func(http.HandlerFunc) http.HandlerFunc {
	allowed := make(map[Role]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			data, ok := sessionFrom(r.Context())
			if !ok || !allowed[data.Role] {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next(w, r)
		}
	}
}

// withTenantTx begins an RLS-scoped transaction for the current customer session.
// Callers (Module E handlers) use the returned tx for all tenant-scoped queries;
// it sees only rows where tenant_id matches the session tenant. Rollback/commit
// is the caller's responsibility.
func (s *Server) withTenantTx(ctx context.Context) (pgx.Tx, error) {
	data, ok := sessionFrom(ctx)
	if !ok || data.Role != RoleCustomer || data.TenantID == nil {
		return nil, errors.New("console: withTenantTx requires a customer session with a tenant")
	}
	return s.mgr.WithTenant(ctx, *data.TenantID)
}
