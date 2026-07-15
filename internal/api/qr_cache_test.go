package api

import (
	"sync"
	"testing"
	"time"
)

// TestQRCache_SetGetRoundTrip: a freshly Set QR is returned verbatim by Get,
// including the data:image/png;base64, prefix (stored as-is, per design).
func TestQRCache_SetGetRoundTrip(t *testing.T) {
	q := newQRCache()
	q.Set("wa_1", "data:image/png;base64,ABC123")
	if got := q.Get("wa_1"); got != "data:image/png;base64,ABC123" {
		t.Fatalf("Get = %q, want data:image/png;base64,ABC123", got)
	}
}

// TestQRCache_UnknownInstanceReturnsEmpty: an instance never Set returns "".
func TestQRCache_UnknownInstanceReturnsEmpty(t *testing.T) {
	q := newQRCache()
	if got := q.Get("ghost"); got != "" {
		t.Fatalf("Get(unknown) = %q, want empty", got)
	}
}

// TestQRCache_ExpiredEntryReturnsEmpty: an entry older than qrTTL is treated
// as stale — Get returns "" so the frontend keeps polling rather than
// rendering a dead code. Backdates the entry directly instead of sleeping.
func TestQRCache_ExpiredEntryReturnsEmpty(t *testing.T) {
	q := newQRCache()
	q.Set("wa_1", "data:image/png;base64,STALE")
	q.mu.Lock()
	e := q.m["wa_1"]
	e.at = time.Now().Add(-qrTTL - time.Second)
	q.m["wa_1"] = e
	q.mu.Unlock()

	if got := q.Get("wa_1"); got != "" {
		t.Fatalf("Get(expired) = %q, want empty", got)
	}
}

// TestQRCache_FreshEntryJustUnderTTLStillReturned pins the boundary: an entry
// recorded just inside qrTTL must still be served (Evolution's own ~40s
// re-push cadence means a too-tight TTL would flap the UI empty between
// pushes).
func TestQRCache_FreshEntryJustUnderTTLStillReturned(t *testing.T) {
	q := newQRCache()
	q.Set("wa_1", "data:image/png;base64,FRESH")
	q.mu.Lock()
	e := q.m["wa_1"]
	e.at = time.Now().Add(-qrTTL + 5*time.Second)
	q.m["wa_1"] = e
	q.mu.Unlock()

	if got := q.Get("wa_1"); got != "data:image/png;base64,FRESH" {
		t.Fatalf("Get(fresh) = %q, want FRESH payload", got)
	}
}

// TestQRCache_SetOverwritesPreviousEntry: the webhook re-pushes every ~40s;
// the newest value must win and refresh the timestamp.
func TestQRCache_SetOverwritesPreviousEntry(t *testing.T) {
	q := newQRCache()
	q.Set("wa_1", "data:image/png;base64,OLD")
	q.Set("wa_1", "data:image/png;base64,NEW")
	if got := q.Get("wa_1"); got != "data:image/png;base64,NEW" {
		t.Fatalf("Get after overwrite = %q, want NEW payload", got)
	}
}

// TestQRCache_NilSafe: Set/Get on a nil *qrCache must not panic — callers
// (tests, and any Server{} built without NewServer) get graceful "always
// empty" behavior instead of a crash.
func TestQRCache_NilSafe(t *testing.T) {
	var q *qrCache
	q.Set("wa_1", "data:image/png;base64,X") // must not panic
	if got := q.Get("wa_1"); got != "" {
		t.Fatalf("Get on nil cache = %q, want empty", got)
	}
}

// TestQRCache_ConcurrentAccess races Set/Get across goroutines under -race.
func TestQRCache_ConcurrentAccess(t *testing.T) {
	q := newQRCache()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			q.Set("wa_1", "data:image/png;base64,X")
		}()
		go func() {
			defer wg.Done()
			_ = q.Get("wa_1")
		}()
	}
	wg.Wait()
}
