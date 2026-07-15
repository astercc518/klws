package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/warmup"
)

// TestLogoutDemotesWarmup pins P0-4 Task 17: a connection.update webhook
// carrying a down state (close/refused) for an instance bound to a MATURE
// warmup account must demote it back to WARMING — a churned/logged-out
// account should re-earn its graduated status, not keep coasting on stale
// signals. Uses the real-Manager test server (newWarmupReplyTestServer, Task
// 14) so h.resolveJID can fall back to JIDForInstance(w.Instance) — the down
// payload carries no wuid, only the instance name seedWarmupAccount bound.
func TestLogoutDemotesWarmup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, ctx := newWarmupReplyTestServer(t)
	seedWarmupAccount(t, ctx, s, "gone@s.whatsapp.net") // binds instance "igone"
	if err := s.deps.Warmup.ForcePromote(ctx, "gone@s.whatsapp.net"); err != nil {
		t.Fatalf("force promote: %v", err)
	}

	wh := NewEvolutionWebhook("", s.deps.Receipt, s.deps.Mgr, nil, s.deps.QRCache).WithWarmup(s.deps.Warmup)
	r := gin.New()
	wh.Register(r)

	body := `{"event":"connection.update","instance":"igone","data":{"state":"close"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}

	p, err := s.deps.Warmup.Store().Get(ctx, "gone@s.whatsapp.net")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if p.Stage != warmup.StageWarming {
		t.Fatalf("logout should demote MATURE->WARMING, got %s", p.Stage)
	}
}

// TestLogoutOnNonMatureIsNoop pins the ErrInvalidTransition swallow: a
// WARMING account (never promoted) receiving a down-state signal must not
// fail the webhook and must stay WARMING — Demote is an illegal transition
// for WARMING/NEW per warmup/state.go, and that error is expected/benign
// here (no demotion needed for an account that never graduated).
func TestLogoutOnNonMatureIsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, ctx := newWarmupReplyTestServer(t)
	seedWarmupAccount(t, ctx, s, "stillwarming@s.whatsapp.net") // binds instance "istillwarming"

	wh := NewEvolutionWebhook("", s.deps.Receipt, s.deps.Mgr, nil, s.deps.QRCache).WithWarmup(s.deps.Warmup)
	r := gin.New()
	wh.Register(r)

	body := `{"event":"connection.update","instance":"istillwarming","data":{"state":"close"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}

	p, err := s.deps.Warmup.Store().Get(ctx, "stillwarming@s.whatsapp.net")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if p.Stage != warmup.StageWarming {
		t.Fatalf("want stage unchanged WARMING, got %s", p.Stage)
	}
}
