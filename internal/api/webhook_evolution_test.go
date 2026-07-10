package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestEvolutionWebhook_HMAC(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewEvolutionWebhook("s3cr3t").Register(r)
	body := []byte(`{"event":"connection.update","instance":"wa_1","data":{"state":"open"}}`)

	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewReader(body))
	req.Header.Set("X-Evolution-Signature", sign("s3cr3t", body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("valid sig -> %d", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewReader(body))
	req2.Header.Set("X-Evolution-Signature", "deadbeef")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("bad sig -> %d", w2.Code)
	}
}
