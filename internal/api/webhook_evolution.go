package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	BindInstanceJID(ctx context.Context, instanceName, jid string) error
	SetInstanceState(ctx context.Context, instanceName, state string) error
}

// healthSink feeds account health signals (satisfied by *sendgate.SendGate).
// Optional: nil skips the health path (E3 wires nil; E4 supplies sendgate).
type healthSink interface {
	ApplyHealthSignal(ctx context.Context, jid, signal string, cooloff time.Duration) error
}

// EvolutionWebhook receives Evolution API callbacks: HMAC-verifies, then feeds
// receipts (messages.update) and instance state (connection.update). It never
// mutates the whatsmeow path.
type EvolutionWebhook struct {
	secret string
	rec    receiptSink
	inst   instanceStore
	health healthSink
}

func NewEvolutionWebhook(secret string, rec receiptSink, inst instanceStore, health healthSink) *EvolutionWebhook {
	return &EvolutionWebhook{secret: secret, rec: rec, inst: inst, health: health}
}

func (h *EvolutionWebhook) Register(r gin.IRouter) {
	r.POST("/webhook/evolution", h.handle)
}

func (h *EvolutionWebhook) handle(c *gin.Context) {
	body, _ := c.GetRawData()
	if !h.verify(c.GetHeader("X-Evolution-Signature"), body) {
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
		if w.Data.RemoteJID != "" {
			_ = h.inst.BindInstanceJID(ctx, w.Instance, w.Data.RemoteJID)
		}
		if w.Data.State != "" {
			_ = h.inst.SetInstanceState(ctx, w.Instance, w.Data.State)
		}
		if w.Data.State == "close" && h.health != nil {
			if jid := h.resolveJID(ctx, w); jid != "" {
				_ = h.health.ApplyHealthSignal(ctx, jid, "conn_churn", 5*time.Minute)
			}
		}
	case "messages.update":
		kind, ok := translateAck(w.Data.Status, w.Data.Ack)
		if !ok || w.Data.Key == nil || !w.Data.Key.FromMe {
			break
		}
		jid, ok, err := h.inst.JIDForInstance(ctx, w.Instance)
		if err != nil || !ok {
			break
		}
		_ = h.rec.Record(ctx, receipt.Event{
			MessageIDs: []string{w.Data.Key.ID},
			SenderJID:  jid,
			Kind:       kind,
			At:         time.Now(),
		})
	}
	c.Status(http.StatusOK)
}

// resolveJID prefers the payload's remoteJid, else looks it up by instance.
func (h *EvolutionWebhook) resolveJID(ctx context.Context, w evoWebhook) string {
	if w.Data.RemoteJID != "" {
		return w.Data.RemoteJID
	}
	if jid, ok, err := h.inst.JIDForInstance(ctx, w.Instance); err == nil && ok {
		return jid
	}
	return ""
}

func (h *EvolutionWebhook) verify(sig string, body []byte) bool {
	if h.secret == "" { // dev only; prod must set WADIST_EVOLUTION_WEBHOOK_SECRET
		return true
	}
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(body)
	return hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil))))
}
