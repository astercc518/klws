package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/warmup"
)

// fakeEnroller records every jid handed to Enroll (satisfies warmupEnroller).
type fakeEnroller struct{ calls []string }

func (f *fakeEnroller) Enroll(_ context.Context, jid string, _ int64, _ warmup.Lane) error {
	f.calls = append(f.calls, jid)
	return nil
}

// RecordReply satisfies warmupEnroller (Task 14 extended the interface); not
// exercised by this file's tests (those cover Enroll on connection.update).
func (f *fakeEnroller) RecordReply(_ context.Context, _ string) error {
	return nil
}

// TestConnectionUpdateAutoEnrollsWarmup pins P0-4 T6: a connection.update that
// resolves the account's own jid must also fold it into the warmup pool
// (h.warmup.Enroll), not just account_devices (EnrollDeviceForInstance) —
// scan-in should auto-enroll new accounts into warmup with no manual step.
func TestConnectionUpdateAutoEnrollsWarmup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	enr := &fakeEnroller{}
	wh := NewEvolutionWebhook("", &fakeReceipt{}, newFakeInst(), nil, nil)
	wh.warmup = enr // package-internal test: assign directly (see WithWarmup for the nil-safe prod path)
	r := gin.New()
	wh.Register(r)

	body := `{"event":"connection.update","instance":"inst-1","data":{"state":"open","wuid":"55119@s.whatsapp.net"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	if len(enr.calls) != 1 || enr.calls[0] != "55119@s.whatsapp.net" {
		t.Fatalf("want enroll of jid, got %v", enr.calls)
	}
}

// TestWithWarmup_NilServiceIsNoop pins the nil-interface trap: assigning a nil
// *warmup.Service to an interface field yields a NON-nil interface holding a
// nil pointer, so h.warmup != nil would be true and any call would panic.
// WithWarmup must guard against this at the concrete-type boundary.
func TestWithWarmup_NilServiceIsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wh := NewEvolutionWebhook("", &fakeReceipt{}, newFakeInst(), nil, nil)
	var svc *warmup.Service // nil
	wh.WithWarmup(svc)
	if wh.warmup != nil {
		t.Fatalf("WithWarmup(nil) must leave h.warmup nil, got %#v", wh.warmup)
	}

	r := gin.New()
	wh.Register(r)
	body := `{"event":"connection.update","instance":"inst-1","data":{"state":"open","wuid":"55119@s.whatsapp.net"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req) // must not panic
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
}
