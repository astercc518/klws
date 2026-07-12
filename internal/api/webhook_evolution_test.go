package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/receipt"
)

// TestEvolutionWebhook_Authorization pins the v2 auth model: Evolution echoes the
// configured webhook.headers.authorization back as an Authorization header (it
// does NOT sign the body). Matching token -> 200; wrong/missing -> 401.
func TestEvolutionWebhook_Authorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewEvolutionWebhook("s3cr3t", &fakeReceipt{}, newFakeInst(), nil).Register(r)
	body := []byte(`{"event":"connection.update","instance":"wa_1","data":{"state":"open"}}`)

	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewReader(body))
	req.Header.Set("Authorization", "s3cr3t")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("valid token -> %d", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewReader(body))
	req2.Header.Set("Authorization", "wrong")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("bad token -> %d", w2.Code)
	}
}

type fakeReceipt struct{ evs []receipt.Event }

func (f *fakeReceipt) Record(_ context.Context, ev receipt.Event) error {
	f.evs = append(f.evs, ev)
	return nil
}

type fakeInst struct {
	jidByInst map[string]string
	bound     map[string]string
	states    map[string]string
}

func newFakeInst() *fakeInst {
	return &fakeInst{jidByInst: map[string]string{}, bound: map[string]string{}, states: map[string]string{}}
}
func (f *fakeInst) JIDForInstance(_ context.Context, inst string) (string, bool, error) {
	j, ok := f.jidByInst[inst]
	return j, ok, nil
}
func (f *fakeInst) BindInstanceJIDIfUnset(_ context.Context, inst, jid string) error {
	f.bound[inst] = jid
	f.jidByInst[inst] = jid
	return nil
}
func (f *fakeInst) SetInstanceState(_ context.Context, inst, state string) error {
	f.states[inst] = state
	return nil
}

type errReceipt struct{}

func (errReceipt) Record(_ context.Context, _ receipt.Event) error {
	return errors.New("db down")
}

type fakeHealth struct{ calls []string }

func (f *fakeHealth) ApplyHealthSignal(_ context.Context, jid, signal string, _ time.Duration) error {
	f.calls = append(f.calls, jid+":"+signal)
	return nil
}

func postWebhook(t *testing.T, h *EvolutionWebhook, body []byte) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.Register(r)
	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// Evolution v2 connection.update carries the account's own JID in `wuid`.
func TestWebhook_ConnectionUpdate_BindsJIDAndState(t *testing.T) {
	inst := newFakeInst()
	h := NewEvolutionWebhook("", &fakeReceipt{}, inst, nil)
	body := []byte(`{"event":"connection.update","instance":"wa_1","data":{"state":"open","wuid":"123@s.whatsapp.net"}}`)
	if code := postWebhook(t, h, body); code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	if inst.bound["wa_1"] != "123@s.whatsapp.net" {
		t.Fatalf("jid not bound from wuid: %+v", inst.bound)
	}
	if inst.states["wa_1"] != "open" {
		t.Fatalf("state=%q", inst.states["wa_1"])
	}
}

func TestWebhook_ConnectionClose_FiresHealthSignal(t *testing.T) {
	inst := newFakeInst()
	inst.jidByInst["wa_1"] = "123@s.whatsapp.net"
	health := &fakeHealth{}
	h := NewEvolutionWebhook("", &fakeReceipt{}, inst, health)
	body := []byte(`{"event":"connection.update","instance":"wa_1","data":{"state":"close"}}`)
	postWebhook(t, h, body)
	if len(health.calls) != 1 || health.calls[0] != "123@s.whatsapp.net:conn_churn" {
		t.Fatalf("health calls=%v", health.calls)
	}
}

// Evolution v2 messages.update is FLAT: data.keyId / data.fromMe / data.status
// (no nested key, no numeric ack). This is the receipt-matching command point.
func TestWebhook_MessagesUpdate_RecordsReadReceipt(t *testing.T) {
	inst := newFakeInst()
	inst.jidByInst["wa_1"] = "123@s.whatsapp.net"
	rec := &fakeReceipt{}
	h := NewEvolutionWebhook("", rec, inst, nil)
	body := []byte(`{"event":"messages.update","instance":"wa_1","data":{"keyId":"WAMID7","fromMe":true,"status":"READ"}}`)
	postWebhook(t, h, body)
	if len(rec.evs) != 1 {
		t.Fatalf("recorded %d events", len(rec.evs))
	}
	ev := rec.evs[0]
	if ev.Kind != receipt.Read || ev.SenderJID != "123@s.whatsapp.net" ||
		len(ev.MessageIDs) != 1 || ev.MessageIDs[0] != "WAMID7" {
		t.Fatalf("event=%+v", ev)
	}
}

// DELIVERY_ACK status maps to a delivered receipt.
func TestWebhook_MessagesUpdate_DeliveryAck(t *testing.T) {
	inst := newFakeInst()
	inst.jidByInst["wa_1"] = "123@s.whatsapp.net"
	rec := &fakeReceipt{}
	h := NewEvolutionWebhook("", rec, inst, nil)
	body := []byte(`{"event":"messages.update","instance":"wa_1","data":{"keyId":"M8","fromMe":true,"status":"DELIVERY_ACK"}}`)
	postWebhook(t, h, body)
	if len(rec.evs) != 1 || rec.evs[0].Kind != receipt.Delivered {
		t.Fatalf("want one delivered receipt, got %+v", rec.evs)
	}
}

// SERVER_ACK (message reached the WA server = "sent") is recorded at send time,
// so the webhook must skip it — no duplicate receipt.
func TestWebhook_MessagesUpdate_ServerAckSkipped(t *testing.T) {
	inst := newFakeInst()
	inst.jidByInst["wa_1"] = "123@s.whatsapp.net"
	rec := &fakeReceipt{}
	h := NewEvolutionWebhook("", rec, inst, nil)
	body := []byte(`{"event":"messages.update","instance":"wa_1","data":{"keyId":"M9","fromMe":true,"status":"SERVER_ACK"}}`)
	postWebhook(t, h, body)
	if len(rec.evs) != 0 {
		t.Fatalf("SERVER_ACK must be skipped: %+v", rec.evs)
	}
}

func TestWebhook_MessagesUpdate_UnknownInstanceNoRecord(t *testing.T) {
	rec := &fakeReceipt{}
	h := NewEvolutionWebhook("", rec, newFakeInst(), nil)
	body := []byte(`{"event":"messages.update","instance":"ghost","data":{"keyId":"X","fromMe":true,"status":"READ"}}`)
	postWebhook(t, h, body)
	if len(rec.evs) != 0 {
		t.Fatalf("must not record for unknown instance: %+v", rec.evs)
	}
}

func TestWebhook_MessagesUpdate_NotFromMeSkipped(t *testing.T) {
	inst := newFakeInst()
	inst.jidByInst["wa_1"] = "j"
	rec := &fakeReceipt{}
	h := NewEvolutionWebhook("", rec, inst, nil)
	body := []byte(`{"event":"messages.update","instance":"wa_1","data":{"keyId":"X","fromMe":false,"status":"READ"}}`)
	postWebhook(t, h, body)
	if len(rec.evs) != 0 {
		t.Fatalf("inbound (not fromMe) must be skipped: %+v", rec.evs)
	}
}

func TestWebhook_MessagesUpdate_DBErrorReturns500(t *testing.T) {
	inst := newFakeInst()
	inst.jidByInst["wa_1"] = "123@s.whatsapp.net"
	rec := &errReceipt{} // Record returns an error
	h := NewEvolutionWebhook("", rec, inst, nil)
	body := []byte(`{"event":"messages.update","instance":"wa_1","data":{"keyId":"X","fromMe":true,"status":"READ"}}`)
	code := postWebhook(t, h, body)
	if code != http.StatusInternalServerError {
		t.Fatalf("db error must yield 500, got %d", code)
	}
}
