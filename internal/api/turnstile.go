// internal/api/turnstile.go — Cloudflare Turnstile server-side verification.
package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// verifyTurnstile validates a Turnstile token against Cloudflare's siteverify.
//
// Posture:
//   - TURNSTILE_SECRET_KEY unset  → skip (fail-open) so the app is never locked
//     out when the captcha is not configured.
//   - secret set, token empty     → reject.
//   - secret set, CF says success → accept; CF says failure → reject.
//   - secret set, CF unreachable/parse error → fail-open (logged) to avoid an
//     auth outage if Cloudflare is briefly unreachable.
func verifyTurnstile(ctx context.Context, token, remoteIP string) bool {
	secret := strings.TrimSpace(os.Getenv("TURNSTILE_SECRET_KEY"))
	if secret == "" {
		return true // not configured
	}
	if strings.TrimSpace(token) == "" {
		return false
	}

	form := url.Values{"secret": {secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, turnstileVerifyURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		log.Printf("[turnstile] build request: %v (failing open)", err)
		return true
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[turnstile] siteverify unreachable: %v (failing open)", err)
		return true
	}
	defer resp.Body.Close()

	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Printf("[turnstile] decode response: %v (failing open)", err)
		return true
	}
	return out.Success
}
