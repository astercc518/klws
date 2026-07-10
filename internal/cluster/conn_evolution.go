package cluster

import (
	"context"
	"sync"

	"github.com/acme/wadist/internal/store"
)

// InstanceState mirrors Evolution's connection lifecycle, kept fresh by the
// CONNECTION_UPDATE webhook (E3). It is the Go-side liveness truth.
type InstanceState string

const (
	StateCreated      InstanceState = "created"
	StateQR           InstanceState = "qr"
	StateConnected    InstanceState = "connected"
	StateDisconnected InstanceState = "disconnected"
	StateLoggedOut    InstanceState = "loggedOut"
)

// evoAPI is the slice of EvoClient that evoInstance needs (fake-testable).
type evoAPI interface {
	CreateInstance(ctx context.Context, instanceName string, proxy *store.ProxyBinding, webhookURL string) error
	ConnectInstance(ctx context.Context, instanceName string) (string, error)
	SetPresence(ctx context.Context, instanceName string, available bool) error
	SendTyping(ctx context.Context, instanceName, toPhone string, composing bool) error
}

// evoInstance is the Evolution-backed Conn: one account's live transport
// expressed as REST calls to an Evolution instance. Proxy is applied at create
// time (sticky). It implements PresenceConn (anthropomorphic dwell) and
// LivenessConn (from webhook-fed state). NOT wired into the live orchestrator
// in E1 — dormant behind WADIST_CONN until a later integration module.
type evoInstance struct {
	api          evoAPI
	instanceName string
	webhookURL   string
	proxy        *store.ProxyBinding

	mu    sync.Mutex
	state InstanceState
}

func NewEvoInstance(api evoAPI, instanceName, webhookURL string, proxy *store.ProxyBinding) *evoInstance {
	return &evoInstance{api: api, instanceName: instanceName, webhookURL: webhookURL, proxy: proxy, state: StateCreated}
}

var (
	_ Conn         = (*evoInstance)(nil)
	_ PresenceConn = (*evoInstance)(nil)
	_ LivenessConn = (*evoInstance)(nil)
)

// Connect provisions the instance (idempotent, sticky proxy + webhook) then
// triggers pairing. The QR (if unpaired) arrives via the QRCODE_UPDATED
// webhook; Connect propagates only errors.
func (e *evoInstance) Connect(ctx context.Context) error {
	if err := e.api.CreateInstance(ctx, e.instanceName, e.proxy, e.webhookURL); err != nil {
		return err
	}
	_, err := e.api.ConnectInstance(ctx, e.instanceName)
	return err
}

// Disconnect stops routing to this instance locally. It does NOT logout/delete
// (warm-residency; terminal recovery is the reaper/control-plane's call) — the
// same "Disconnect never Logout" contract as the whatsmeow Conn.
func (e *evoInstance) Disconnect() {
	e.UpdateState(StateDisconnected)
}

func (e *evoInstance) SetPresence(ctx context.Context, available bool) error {
	return e.api.SetPresence(ctx, e.instanceName, available)
}

func (e *evoInstance) SendTyping(ctx context.Context, chatPhone string, composing bool) error {
	return e.api.SendTyping(ctx, e.instanceName, chatPhone, composing)
}

// Liveness reports socket liveness from the webhook-fed state.
func (e *evoInstance) Liveness() Liveness {
	e.mu.Lock()
	s := e.state
	e.mu.Unlock()
	return Liveness{Alive: s == StateConnected, Permanent: s == StateLoggedOut}
}

// UpdateState is called by the webhook layer (E3) on CONNECTION_UPDATE.
func (e *evoInstance) UpdateState(s InstanceState) {
	e.mu.Lock()
	e.state = s
	e.mu.Unlock()
}
