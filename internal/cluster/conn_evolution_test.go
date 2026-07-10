package cluster

import (
	"context"
	"testing"

	"github.com/acme/wadist/internal/store"
)

type fakeEvoAPI struct {
	created, connected bool
	presence           *bool
	typingCalls        int
	proxySeen          *store.ProxyBinding
	webhookSeen        string
}

func (f *fakeEvoAPI) CreateInstance(_ context.Context, _ string, p *store.ProxyBinding, w string) error {
	f.created = true
	f.proxySeen = p
	f.webhookSeen = w
	return nil
}
func (f *fakeEvoAPI) ConnectInstance(_ context.Context, _ string) (string, error) {
	f.connected = true
	return "QR", nil
}
func (f *fakeEvoAPI) SetPresence(_ context.Context, _ string, a bool) error {
	f.presence = &a
	return nil
}
func (f *fakeEvoAPI) SendTyping(_ context.Context, _, _ string, _ bool) error {
	f.typingCalls++
	return nil
}

func TestEvoInstance_ConnectCreatesThenConnects(t *testing.T) {
	f := &fakeEvoAPI{}
	b := &store.ProxyBinding{ProxyURL: "socks5://u:p@1.2.3.4:1080"}
	e := NewEvoInstance(f, "wa_1", "https://cp/wh", b)
	if err := e.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.created || !f.connected {
		t.Fatalf("created=%v connected=%v", f.created, f.connected)
	}
	if f.proxySeen != b || f.webhookSeen != "https://cp/wh" {
		t.Fatal("proxy/webhook not threaded to CreateInstance")
	}
}

func TestEvoInstance_LivenessReflectsState(t *testing.T) {
	e := NewEvoInstance(&fakeEvoAPI{}, "wa_1", "", nil)
	if lv := e.Liveness(); lv.Alive || lv.Permanent {
		t.Fatalf("fresh instance should be not-alive not-permanent: %+v", lv)
	}
	e.UpdateState(StateConnected)
	if lv := e.Liveness(); !lv.Alive || lv.Permanent {
		t.Fatalf("connected: %+v", lv)
	}
	e.UpdateState(StateLoggedOut)
	if lv := e.Liveness(); lv.Alive || !lv.Permanent {
		t.Fatalf("loggedOut: %+v", lv)
	}
}

func TestEvoInstance_DisconnectDoesNotLogout(t *testing.T) {
	f := &fakeEvoAPI{}
	e := NewEvoInstance(f, "wa_1", "", nil)
	e.UpdateState(StateConnected)
	e.Disconnect()
	if lv := e.Liveness(); lv.Alive {
		t.Fatal("after Disconnect, not alive")
	}
	if lv := e.Liveness(); lv.Permanent {
		t.Fatal("Disconnect must NOT mark permanent (no logout)")
	}
}

func TestEvoInstance_PresenceDelegates(t *testing.T) {
	f := &fakeEvoAPI{}
	e := NewEvoInstance(f, "wa_1", "", nil)
	_ = e.SetPresence(context.Background(), false)
	_ = e.SendTyping(context.Background(), "15551234", true)
	if f.presence == nil || *f.presence != false || f.typingCalls != 1 {
		t.Fatalf("presence=%v typing=%d", f.presence, f.typingCalls)
	}
}

func TestEvoInstance_ImplementsCapabilities(t *testing.T) {
	var _ Conn = (*evoInstance)(nil)
	var _ PresenceConn = (*evoInstance)(nil)
	var _ LivenessConn = (*evoInstance)(nil)
}
