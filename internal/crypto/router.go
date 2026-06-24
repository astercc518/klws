package crypto

import (
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Router maps a data-residency region to its database pool. For M10 it is a stub
// over a single default pool (single-region deployment); real per-region DSNs are
// wired in M11. PoolFor falls back to the default for unknown/empty regions.
type Router struct {
	mu    sync.RWMutex
	def   *pgxpool.Pool
	pools map[string]*pgxpool.Pool
}

// NewRouter creates a Router with the given pool as the default.
func NewRouter(def *pgxpool.Pool) *Router {
	return &Router{def: def, pools: map[string]*pgxpool.Pool{}}
}

// Register associates region with pool. Subsequent calls to PoolFor(region) will
// return this pool instead of the default.
func (r *Router) Register(region string, pool *pgxpool.Pool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pools[region] = pool
}

// PoolFor returns the pool registered for region, or the default pool if region
// is empty or unknown.
func (r *Router) PoolFor(region string) *pgxpool.Pool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.pools[region]; ok {
		return p
	}
	return r.def
}
