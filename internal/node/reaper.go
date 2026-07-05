package node

import (
	"context"
	"log"
	"time"

	"github.com/acme/wadist/internal/cluster"
	"golang.org/x/sync/errgroup"
)

// GhostCandidate is one session's liveness view for a reaper tick.
type GhostCandidate struct {
	JID       string
	Alive     bool
	Permanent bool
}

// GhostCandidatesFrom maps live sessions to candidates via the LivenessConn
// capability. A session whose conn lacks the capability is reported Alive=true
// (assume-healthy: never reap what we cannot probe).
func GhostCandidatesFrom(sessions []*cluster.Session) []GhostCandidate {
	out := make([]GhostCandidate, 0, len(sessions))
	for _, s := range sessions {
		lv, ok := s.Liveness()
		if !ok {
			out = append(out, GhostCandidate{JID: s.JID(), Alive: true})
			continue
		}
		out = append(out, GhostCandidate{JID: s.JID(), Alive: lv.Alive, Permanent: lv.Permanent})
	}
	return out
}

// GhostReaperMetrics is the optional metrics sink (nil-safe at call site).
type GhostReaperMetrics interface {
	IncGhostReaped(kind string) // "transient" | "permanent"
	SetGhostInFlight(n int)
}

// GhostReaperDeps injects all side effects so the reaper is pure/testable.
type GhostReaperDeps struct {
	Now           func() time.Time
	Snapshot      func() []GhostCandidate
	Reap          func(ctx context.Context, jid string) error // reg.Remove + sess.Close
	RequestWarm   func(ctx context.Context, jid string) error // transient → re-warm
	MarkLoggedOut func(ctx context.Context, jid string) error // permanent → PG logged_out
	Metrics       GhostReaperMetrics                          // may be nil
}

type GhostReaperConfig struct {
	TTL time.Duration // dwell before a silent not-alive session is a ghost
	Max int           // max concurrent reaps per tick
}

// GhostReaper force-reclaims ghost connections under hard caps.
type GhostReaper struct {
	cfg   GhostReaperConfig
	deps  GhostReaperDeps
	dwell map[string]time.Time // jid → first observed not-alive time
}

func NewGhostReaper(cfg GhostReaperConfig, deps GhostReaperDeps) *GhostReaper {
	if cfg.TTL <= 0 {
		cfg.TTL = 20 * time.Second
	}
	if cfg.Max <= 0 {
		cfg.Max = 128
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &GhostReaper{cfg: cfg, deps: deps, dwell: make(map[string]time.Time)}
}

type ghost struct {
	jid       string
	permanent bool
}

// Tick classifies the current snapshot, then reaps up to Max ghosts concurrently.
// Returns the number reaped this tick.
func (r *GhostReaper) Tick(ctx context.Context) (int, error) {
	snap := r.deps.Snapshot()
	now := r.deps.Now()

	present := make(map[string]struct{}, len(snap))
	var ghosts []ghost
	for _, c := range snap {
		present[c.JID] = struct{}{}
		switch {
		case c.Permanent:
			delete(r.dwell, c.JID)
			ghosts = append(ghosts, ghost{jid: c.JID, permanent: true})
		case c.Alive:
			delete(r.dwell, c.JID)
		default: // not alive, not permanent → dwell
			first, ok := r.dwell[c.JID]
			if !ok {
				r.dwell[c.JID] = now
				first = now
			}
			if now.Sub(first) >= r.cfg.TTL {
				ghosts = append(ghosts, ghost{jid: c.JID, permanent: false})
			}
		}
	}
	// Drop dwell entries for sessions no longer present.
	for jid := range r.dwell {
		if _, ok := present[jid]; !ok {
			delete(r.dwell, jid)
		}
	}

	// Cap this tick's reaps; overflow waits for the next tick.
	if len(ghosts) > r.cfg.Max {
		ghosts = ghosts[:r.cfg.Max]
	}
	if len(ghosts) == 0 {
		if r.deps.Metrics != nil {
			r.deps.Metrics.SetGhostInFlight(0)
		}
		return 0, nil
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(r.cfg.Max)
	for _, gh := range ghosts {
		gh := gh
		g.Go(func() error {
			if err := r.deps.Reap(gctx, gh.jid); err != nil {
				log.Printf("node: ghost reap %s: %v", gh.jid, err)
				return nil // one failure never aborts the batch
			}
			if gh.permanent {
				if err := r.deps.MarkLoggedOut(gctx, gh.jid); err != nil {
					log.Printf("node: ghost mark-logged-out %s: %v", gh.jid, err)
				}
				if r.deps.Metrics != nil {
					r.deps.Metrics.IncGhostReaped("permanent")
				}
			} else {
				if err := r.deps.RequestWarm(gctx, gh.jid); err != nil {
					log.Printf("node: ghost re-warm %s: %v", gh.jid, err)
				}
				if r.deps.Metrics != nil {
					r.deps.Metrics.IncGhostReaped("transient")
				}
			}
			return nil
		})
	}
	_ = g.Wait()

	// Reaped sessions leave the registry; drop their dwell entries.
	for _, gh := range ghosts {
		delete(r.dwell, gh.jid)
	}
	if r.deps.Metrics != nil {
		r.deps.Metrics.SetGhostInFlight(len(ghosts))
	}
	return len(ghosts), nil
}

// Run ticks every interval; interval<=0 disables (blocks until ctx done).
func (r *GhostReaper) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if n, err := r.Tick(ctx); err != nil {
				log.Printf("node: ghost reaper tick error: %v", err)
			} else if n > 0 {
				log.Printf("node: ghost reaper reclaimed %d ghost(s)", n)
			}
		}
	}
}
