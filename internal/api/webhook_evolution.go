package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/gin-gonic/gin"
)

// EvolutionWebhook receives Evolution API callbacks. E0 verifies the HMAC
// signature and returns 200/401 only — it performs NO database mutation.
// E3 upgrades this to feed receipt.Recorder + sendgate health signals.
type EvolutionWebhook struct {
	secret string
}

func NewEvolutionWebhook(secret string) *EvolutionWebhook {
	return &EvolutionWebhook{secret: secret}
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
	// E0: observe only. Never mutate campaign_recipients here yet (E3).
	c.Status(http.StatusOK)
}

func (h *EvolutionWebhook) verify(sig string, body []byte) bool {
	if h.secret == "" { // dev only; prod must set WADIST_EVOLUTION_WEBHOOK_SECRET
		return true
	}
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(body)
	return hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil))))
}
