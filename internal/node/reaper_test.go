package node

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recording deps for reaper tests.
type reapRec struct {
	mu        sync.Mutex
	reaped    []string
	warmed    []string
	loggedOut []string
}

func (r *reapRec) reap(_ context.Context, jid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reaped = append(r.reaped, jid)
	return nil
}
func (r *reapRec) warm(_ context.Context, jid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warmed = append(r.warmed, jid)
	return nil
}
func (r *reapRec) markOut(_ context.Context, jid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loggedOut = append(r.loggedOut, jid)
	return nil
}

func newReaper(t *testing.T, cfg GhostReaperConfig, snap func() []GhostCandidate, now func() time.Time) (*GhostReaper, *reapRec) {
	t.Helper()
	rec := &reapRec{}
	r := NewGhostReaper(cfg, GhostReaperDeps{
		Now: now, Snapshot: snap,
		Reap: rec.reap, RequestWarm: rec.warm, MarkLoggedOut: rec.markOut,
	})
	return r, rec
}

func TestReaper_TransientDwellBelowTTL_NoReap(t *testing.T) {
	base := time.Unix(1000, 0)
	nowT := base
	snap := func() []GhostCandidate { return []GhostCandidate{{JID: "a", Alive: false}} }
	r, rec := newReaper(t, GhostReaperConfig{TTL: 20 * time.Second, Max: 8},
		snap, func() time.Time { return nowT })

	n, _ := r.Tick(context.Background()) // records dwell at base
	if n != 0 {
		t.Fatalf("tick1 reaped %d; want 0 (dwell just started)", n)
	}
	nowT = base.Add(10 * time.Second) // < TTL
	n, _ = r.Tick(context.Background())
	if n != 0 || len(rec.reaped) != 0 {
		t.Fatalf("reaped %d before TTL; want 0", n)
	}
}

func TestReaper_TransientAtTTL_ReapAndRewarm(t *testing.T) {
	base := time.Unix(1000, 0)
	nowT := base
	snap := func() []GhostCandidate { return []GhostCandidate{{JID: "a", Alive: false}} }
	r, rec := newReaper(t, GhostReaperConfig{TTL: 20 * time.Second, Max: 8},
		snap, func() time.Time { return nowT })

	r.Tick(context.Background())      // dwell starts at base
	nowT = base.Add(20 * time.Second) // == TTL
	n, _ := r.Tick(context.Background())
	if n != 1 {
		t.Fatalf("reaped %d; want 1 at TTL", n)
	}
	if len(rec.reaped) != 1 || rec.reaped[0] != "a" {
		t.Fatalf("reaped=%v; want [a]", rec.reaped)
	}
	if len(rec.warmed) != 1 || rec.warmed[0] != "a" {
		t.Fatalf("warmed=%v; want [a] (transient re-warm)", rec.warmed)
	}
	if len(rec.loggedOut) != 0 {
		t.Fatalf("loggedOut=%v; want none for transient", rec.loggedOut)
	}
}

func TestReaper_Permanent_ImmediateReapMarkNoRewarm(t *testing.T) {
	snap := func() []GhostCandidate { return []GhostCandidate{{JID: "p", Alive: false, Permanent: true}} }
	r, rec := newReaper(t, GhostReaperConfig{TTL: 20 * time.Second, Max: 8},
		snap, func() time.Time { return time.Unix(1000, 0) })

	n, _ := r.Tick(context.Background()) // no dwell wait for permanent
	if n != 1 {
		t.Fatalf("reaped %d; want 1 (permanent immediate)", n)
	}
	if len(rec.loggedOut) != 1 || rec.loggedOut[0] != "p" {
		t.Fatalf("loggedOut=%v; want [p]", rec.loggedOut)
	}
	if len(rec.warmed) != 0 {
		t.Fatalf("warmed=%v; want none for permanent (no re-warm)", rec.warmed)
	}
}

func TestReaper_AliveClearsDwell(t *testing.T) {
	base := time.Unix(1000, 0)
	nowT := base
	alive := false
	snap := func() []GhostCandidate { return []GhostCandidate{{JID: "a", Alive: alive}} }
	r, rec := newReaper(t, GhostReaperConfig{TTL: 20 * time.Second, Max: 8},
		snap, func() time.Time { return nowT })

	r.Tick(context.Background()) // dwell starts (not alive)
	alive = true                 // recovered before TTL
	nowT = base.Add(10 * time.Second)
	r.Tick(context.Background()) // should clear dwell
	alive = false                // dies again
	nowT = base.Add(15 * time.Second)
	r.Tick(context.Background())      // dwell restarts here, not from base
	nowT = base.Add(30 * time.Second) // 15s since restart < TTL
	n, _ := r.Tick(context.Background())
	if n != 0 || len(rec.reaped) != 0 {
		t.Fatalf("reaped %d; want 0 (dwell was reset by the alive tick)", n)
	}
}

func TestReaper_GhostMaxOverflowsToNextTick(t *testing.T) {
	// 5 permanent ghosts, Max=2 → 2 per tick; snapshot shrinks as reaped.
	var liveMu sync.Mutex
	live := map[string]bool{"a": true, "b": true, "c": true, "d": true, "e": true}
	snap := func() []GhostCandidate {
		liveMu.Lock()
		defer liveMu.Unlock()
		out := []GhostCandidate{}
		for _, j := range []string{"a", "b", "c", "d", "e"} {
			if live[j] {
				out = append(out, GhostCandidate{JID: j, Alive: false, Permanent: true})
			}
		}
		return out
	}
	rec := &reapRec{}
	r := NewGhostReaper(GhostReaperConfig{TTL: time.Second, Max: 2}, GhostReaperDeps{
		Now:      func() time.Time { return time.Unix(1000, 0) },
		Snapshot: snap,
		Reap: func(ctx context.Context, jid string) error {
			liveMu.Lock()
			live[jid] = false // reaped session leaves the registry
			liveMu.Unlock()
			return rec.reap(ctx, jid)
		},
		RequestWarm:   rec.warm,
		MarkLoggedOut: rec.markOut,
	})

	total := 0
	for i := 0; i < 3; i++ { // ceil(5/2)=3 ticks
		n, _ := r.Tick(context.Background())
		if n > 2 {
			t.Fatalf("tick %d reaped %d; want <=Max(2)", i, n)
		}
		total += n
	}
	if total != 5 {
		t.Fatalf("total reaped=%d; want 5 across ticks", total)
	}
}

func TestReaper_DropsDwellForGoneSessions(t *testing.T) {
	present := true
	snap := func() []GhostCandidate {
		if present {
			return []GhostCandidate{{JID: "a", Alive: false}}
		}
		return nil
	}
	r, _ := newReaper(t, GhostReaperConfig{TTL: 20 * time.Second, Max: 8},
		snap, func() time.Time { return time.Unix(1000, 0) })

	r.Tick(context.Background()) // dwell["a"] set
	if len(r.dwell) != 1 {
		t.Fatalf("dwell size=%d; want 1", len(r.dwell))
	}
	present = false
	r.Tick(context.Background()) // "a" gone → dwell dropped
	if len(r.dwell) != 0 {
		t.Fatalf("dwell size=%d; want 0 after session gone", len(r.dwell))
	}
}
