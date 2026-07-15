package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/receipt"
)

// receiptSink records delivery/read milestones (satisfied by *receipt.Recorder).
type receiptSink interface {
	Record(ctx context.Context, ev receipt.Event) error
}

// instanceStore resolves + mutates account_instances routing rows
// (satisfied by *store.Manager).
type instanceStore interface {
	JIDForInstance(ctx context.Context, instanceName string) (string, bool, error)
	BindInstanceJIDIfUnset(ctx context.Context, instanceName, jid string) error
	SetInstanceState(ctx context.Context, instanceName, state string) error
}

// healthSink feeds account health signals (satisfied by *sendgate.SendGate).
// Optional: nil skips the health path (E3 wires nil; E4 supplies sendgate).
type healthSink interface {
	ApplyHealthSignal(ctx context.Context, jid, signal string, cooloff time.Duration) error
}

// qrSink caches the latest QR pairing code per instance (satisfied by
// *qrCache). This is the ONLY place a QR's base64 image ever arrives — see
// evoWebhookData's qrcode.updated doc comment — so the webhook is the sole
// writer; handleAdminInstanceQR (instances_api.go) reads it back.
type qrSink interface {
	Set(instance, base64 string)
}

// EvolutionWebhook receives Evolution API callbacks: authenticates via the
// Authorization header Evolution echoes back (configured as webhook.headers.
// authorization at instance-create time — Evolution v2 does NOT sign payloads),
// then feeds receipts (messages.update), instance state (connection.update),
// and the pairing QR cache (qrcode.updated).
type EvolutionWebhook struct {
	secret string
	rec    receiptSink
	inst   instanceStore
	health healthSink
	qr     qrSink
}

func NewEvolutionWebhook(secret string, rec receiptSink, inst instanceStore, health healthSink, qr qrSink) *EvolutionWebhook {
	return &EvolutionWebhook{secret: secret, rec: rec, inst: inst, health: health, qr: qr}
}

func (h *EvolutionWebhook) Register(r gin.IRouter) {
	r.POST("/webhook/evolution", h.handle)
}

func (h *EvolutionWebhook) handle(c *gin.Context) {
	body, _ := c.GetRawData()
	if !h.verify(c.GetHeader("Authorization")) {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	var w evoWebhook
	if err := json.Unmarshal(body, &w); err != nil {
		c.Status(http.StatusOK) // stop Evolution retrying unparseable payloads
		return
	}
	ctx := c.Request.Context()
	switch normalizeEvent(w.Event) {
	case "connection.update":
		if jid := w.Data.ownJID(); jid != "" {
			_ = h.inst.BindInstanceJIDIfUnset(ctx, w.Instance, jid)
		}
		if w.Data.State != "" {
			_ = h.inst.SetInstanceState(ctx, w.Instance, w.Data.State)
		}
		if isDownState(w.Data.State) && h.health != nil {
			if jid := h.resolveJID(ctx, w); jid != "" {
				_ = h.health.ApplyHealthSignal(ctx, jid, "conn_churn", 5*time.Minute)
			}
		}
	case "messages.update":
		kind, ok := translateAck(w.Data.Status, w.Data.Ack)
		id := w.Data.msgID()
		if !ok || id == "" || !w.Data.fromMe() {
			break
		}
		jid, ok, err := h.inst.JIDForInstance(ctx, w.Instance)
		if err != nil || !ok {
			break
		}
		if err := h.rec.Record(ctx, receipt.Event{
			MessageIDs: []string{id},
			SenderJID:  jid,
			Kind:       kind,
			At:         time.Now(),
		}); err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	case "qrcode.updated":
		if b64 := w.Data.qrBase64(); b64 != "" && h.qr != nil {
			h.qr.Set(w.Instance, b64)
		}
	}
	c.Status(http.StatusOK)
}

// isDownState reports whether a connection.update state means the socket dropped
// (Evolution v2 emits both "close" and "refused" on a lost/rejected session).
func isDownState(state string) bool {
	return state == "close" || state == "refused"
}

// resolveJID prefers the payload's own JID, else looks it up by instance.
func (h *EvolutionWebhook) resolveJID(ctx context.Context, w evoWebhook) string {
	if jid := w.Data.ownJID(); jid != "" {
		return jid
	}
	if jid, ok, err := h.inst.JIDForInstance(ctx, w.Instance); err == nil && ok {
		return jid
	}
	return ""
}

// verify checks the Authorization header Evolution echoes from the webhook's
// configured headers.authorization. Empty secret = dev (accept unauthenticated);
// prod must set WADIST_EVOLUTION_WEBHOOK_SECRET on BOTH the receiver and the
// instance-creating worker so the values match.
func (h *EvolutionWebhook) verify(auth string) bool {
	if h.secret == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(auth), []byte(h.secret)) == 1
}
