package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acme/wadist/internal/store"
)

type fakeEvoAPI struct {
	created, connected bool
	presence           *bool
	typingCalls        int
	proxySeen          *store.ProxyBinding
	webhookSeen        string
	setProxyErr        error
	// order records the call sequence so tests can assert create->proxy->connect.
	order []string
}

func (f *fakeEvoAPI) CreateInstance(_ context.Context, _ string, w string) error {
	f.created = true
	f.webhookSeen = w
	f.order = append(f.order, "create")
	return nil
}
func (f *fakeEvoAPI) SetProxy(_ context.Context, _ string, p *store.ProxyBinding) error {
	f.proxySeen = p
	f.order = append(f.order, "setproxy")
	return f.setProxyErr
}
func (f *fakeEvoAPI) ConnectInstance(_ context.Context, _ string) (string, error) {
	f.connected = true
	f.order = append(f.order, "connect")
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

func TestEvoInstance_ConnectSequencesCreateProxyConnect(t *testing.T) {
	f := &fakeEvoAPI{}
	b := &store.ProxyBinding{ProxyURL: "socks5://u:p@1.2.3.4:1080"}
	e := NewEvoInstance(f, "wa_1", "https://cp/wh", b)
	if err := e.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.proxySeen != b || f.webhookSeen != "https://cp/wh" {
		t.Fatal("proxy/webhook not threaded")
	}
	// Proxy MUST be applied before the socket dials (create -> setproxy -> connect).
	if got := strings.Join(f.order, ","); got != "create,setproxy,connect" {
		t.Fatalf("call order = %q, want create,setproxy,connect", got)
	}
}

func TestEvoInstance_ConnectNoProxySkipsSetProxy(t *testing.T) {
	f := &fakeEvoAPI{}
	e := NewEvoInstance(f, "wa_1", "", nil)
	if err := e.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.order, ","); got != "create,connect" {
		t.Fatalf("call order = %q, want create,connect", got)
	}
}

// Fail-closed: if the proxy cannot be applied, the instance must NOT connect
// proxyless — Connect errors and ConnectInstance is never called.
func TestEvoInstance_ConnectFailsClosedOnProxyError(t *testing.T) {
	f := &fakeEvoAPI{setProxyErr: errors.New("invalid proxy")}
	b := &store.ProxyBinding{ProxyURL: "socks5://u:p@1.2.3.4:1080"}
	e := NewEvoInstance(f, "wa_1", "", b)
	if err := e.Connect(context.Background()); err == nil {
		t.Fatal("expected error when proxy set fails")
	}
	if f.connected {
		t.Fatal("must NOT connect a proxyless instance when SetProxy failed")
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
