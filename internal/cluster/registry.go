package cluster

import (
	"context"
	"sync"
)

// Registry is the thread-safe set of active account sessions on this node.
// It satisfies metrics.RegistrySnapshotProvider via ActiveSessions().
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*Session)}
}

// Add registers a session. If a session for the same JID already exists it is
// closed first (defensive: never keep two live connections for one account).
func (r *Registry) Add(s *Session) {
	r.mu.Lock()
	old, ok := r.sessions[s.jid]
	r.sessions[s.jid] = s
	r.mu.Unlock()
	if ok && old != s {
		old.Close(context.Background())
	}
}

func (r *Registry) Get(jid string) (*Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[jid]
	return s, ok
}

func (r *Registry) Remove(jid string) (*Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[jid]
	if ok {
		delete(r.sessions, jid)
	}
	return s, ok
}

func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

// ActiveSessions satisfies metrics.RegistrySnapshotProvider.
func (r *Registry) ActiveSessions() int { return r.Len() }

func (r *Registry) Snapshot() []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out
}

// CloseAll detaches every session and empties the registry. Used by the
// Supervisor during graceful shutdown, after asynq intake has stopped.
func (r *Registry) CloseAll(ctx context.Context) {
	r.mu.Lock()
	sessions := r.sessions
	r.sessions = make(map[string]*Session)
	r.mu.Unlock()
	for _, s := range sessions {
		s.Close(ctx)
	}
}
