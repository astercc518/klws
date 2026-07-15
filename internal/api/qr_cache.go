package api

import (
	"sync"
	"time"
)

// qrTTL bounds how long a cached QR is considered fresh. Evolution v2 re-pushes
// a qrcode.updated webhook roughly every ~40s while an instance is pairing, so
// anything older than this is presumed stale — Get returns "" past qrTTL so the
// frontend keeps polling instead of rendering a dead/expired code.
const qrTTL = 90 * time.Second

// qrEntry is one cached QR payload plus the time it was recorded.
type qrEntry struct {
	base64 string
	at     time.Time
}

// qrCache holds the most recent QR code base64 payload per Evolution instance
// name. It exists because Evolution v2.3.7's synchronous
// GET /instance/connect/{name} response carries NO base64 (verified against a
// live instance — it returns only {"count":N,"pairingCode":null}); the actual
// QR image arrives asynchronously via the qrcode.updated webhook event
// (data.qrcode.base64). The webhook handler (webhook_evolution.go) writes
// here via Set on every qrcode.updated; handleAdminInstanceQR
// (instances_api.go) reads via Get to answer the admin UI's poll.
//
// Safe for concurrent use. Safe to call on a nil *qrCache (Get returns "",
// Set is a no-op) so callers/tests that construct a bare Server{} without
// wiring a cache degrade gracefully instead of panicking.
type qrCache struct {
	mu sync.Mutex
	m  map[string]qrEntry
}

// newQRCache returns an empty, ready-to-use cache.
func newQRCache() *qrCache {
	return &qrCache{m: make(map[string]qrEntry)}
}

// NewQRCache is the exported constructor for wiring a single shared instance
// from cmd/console/main.go into both the webhook writer and the API reader.
func NewQRCache() *qrCache {
	return newQRCache()
}

// Set records the latest QR payload for instance, timestamped now. Called by
// the webhook handler on every qrcode.updated event (and, as a best-effort
// fallback, by handleAdminInstanceQR if Evolution's connect response ever
// does carry a non-empty base64).
func (q *qrCache) Set(instance, base64 string) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.m[instance] = qrEntry{base64: base64, at: time.Now()}
}

// Get returns the cached QR for instance, or "" if none is cached or the
// cached entry is older than qrTTL.
func (q *qrCache) Get(instance string) string {
	if q == nil {
		return ""
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.m[instance]
	if !ok || time.Since(e.at) > qrTTL {
		return ""
	}
	return e.base64
}
