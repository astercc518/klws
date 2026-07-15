package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/receipt"
	"github.com/acme/wadist/internal/warmup"
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
	// EnrollDeviceForInstance folds a scanned-in account into account_devices
	// (the send pool) once its jid is known — see store.Manager's doc comment
	// for the proxy-key-reuse rationale (FIX-3).
	EnrollDeviceForInstance(ctx context.Context, instanceName, jid string) error
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

// warmupEnroller folds a freshly-connected account into the warmup pool and
// feeds it inbound reply signals. Optional: nil skips the warmup path
// (dev/tests without warmup wiring).
type warmupEnroller interface {
	Enroll(ctx context.Context, jid string, tenantID int64, lane warmup.Lane) error
	// RecordReply accumulates an inbound-reply signal for jid. No-op (nil
	// error) when jid has no warmup profile (not pool-enrolled) — see
	// warmup.Service.RecordReply's doc comment.
	RecordReply(ctx context.Context, jid string) error
	// Demote retreats a MATURE account back to WARMING on a churn/logout
	// signal (reason is log/audit-only). Returns warmup.ErrInvalidTransition
	// for WARMING/NEW jids (no-op stages, not an error condition for
	// callers) — see warmup.Service.Demote's doc comment.
	Demote(ctx context.Context, jid, reason string) error
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
	warmup warmupEnroller
}

func NewEvolutionWebhook(secret string, rec receiptSink, inst instanceStore, health healthSink, qr qrSink) *EvolutionWebhook {
	return &EvolutionWebhook{secret: secret, rec: rec, inst: inst, health: health, qr: qr}
}

// WithWarmup wires the warmup enroller (called post-EnrollDeviceForInstance on
// connection.update). Separate from the constructor to keep NewEvolutionWebhook's
// signature stable for existing callers/tests. Takes the CONCRETE *warmup.Service
// (not the warmupEnroller interface) and nil-guards: assigning a nil
// *warmup.Service to an interface field would yield a non-nil interface holding
// a nil pointer, making h.warmup != nil true and panicking on Enroll.
func (h *EvolutionWebhook) WithWarmup(w *warmup.Service) *EvolutionWebhook {
	if w != nil {
		h.warmup = w
	}
	return h
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
			// Best-effort: fold the account into account_devices (send pool) now
			// that its jid is known. Never blocks the webhook's 200 — a failure
			// here just leaves the account unsendable until the next
			// connection.update retries it, it does not corrupt routing state.
			_ = h.inst.EnrollDeviceForInstance(ctx, w.Instance, jid)
			// 折进养号池(best-effort,和 device 入池同理由:失败不阻塞 200,
			// 下次 connection.update 重试)。lane 默认 STANDARD;租户从路由行解析,
			// 解析不到用 0(仅影响展示列,不影响养号逻辑)。
			if h.warmup != nil {
				tenantID := h.tenantForInstance(ctx, w.Instance)
				_ = h.warmup.Enroll(ctx, jid, tenantID, warmup.LaneStandard)
			}
		}
		if w.Data.State != "" {
			_ = h.inst.SetInstanceState(ctx, w.Instance, w.Data.State)
		}
		if isDownState(w.Data.State) && h.health != nil {
			if jid := h.resolveJID(ctx, w); jid != "" {
				_ = h.health.ApplyHealthSignal(ctx, jid, "conn_churn", 5*time.Minute)
			}
		}
		// 封号信号自动降级(P0-4 Task 17):MATURE 号掉线/登出退回 WARMING,
		// 重新计时养号。WARMING/NEW 号收到同一信号会命中 ErrInvalidTransition
		// (它们本就没有 DEMOTE 迁移),吞掉即可——无需降级。
		if isDownState(w.Data.State) && h.warmup != nil {
			if jid := h.resolveJID(ctx, w); jid != "" {
				if err := h.warmup.Demote(ctx, jid, "conn_down"); err != nil && !errors.Is(err, warmup.ErrInvalidTransition) {
					log.Printf("warmup demote %s: %v", jid, err)
				}
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
	case "messages.upsert":
		// 入站养号消息喂回复信号。messages.upsert 是 NESTED(data.key.{id,fromMe,
		// remoteJid});fromMe=true 是自己发的,不计。收信方=本实例 jid。
		if h.warmup == nil || w.Data.fromMe() {
			break
		}
		jid, ok, err := h.inst.JIDForInstance(ctx, w.Instance)
		if err != nil || !ok || jid == "" {
			break
		}
		// RecordReply 内部对非池内 jid 静默跳过(spec §5:只有池内号计信号)。
		_ = h.warmup.RecordReply(ctx, jid)
	case "qrcode.updated":
		if b64 := w.Data.qrBase64(); b64 != "" && h.qr != nil {
			h.qr.Set(w.Instance, b64)
		}
	}
	c.Status(http.StatusOK)
}

// tenantForInstance best-effort 解析实例所属租户;取不到返回 0。instanceStore
// 当前不暴露 tenant 解析;保留 0,展示列由 admin 列表 join account_instances 时
// 补全。避免为此扩接口。
func (h *EvolutionWebhook) tenantForInstance(ctx context.Context, instance string) int64 {
	return 0
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
