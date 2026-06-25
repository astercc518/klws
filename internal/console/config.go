// internal/console/config.go
package console

import (
	"encoding/base64"
	"fmt"
	"os"
	"time"
)

// Config is the console web-tier configuration (store/redis config comes from
// internal/config separately and is shared with the node).
type Config struct {
	Addr         string
	SessionKey   []byte
	SessionTTL   time.Duration
	CookieName   string
	CookieSecure bool
}

// LoadConfig reads WADIST_CONSOLE_* env vars. SessionKey is required (base64-std,
// >=32 bytes) so sessions survive restarts and cannot be forged.
func LoadConfig() (Config, error) {
	cfg := Config{
		Addr:         getenv("WADIST_CONSOLE_ADDR", ":8080"),
		SessionTTL:   24 * time.Hour,
		CookieName:   getenv("WADIST_CONSOLE_COOKIE_NAME", "wadist_session"),
		CookieSecure: getenv("WADIST_CONSOLE_COOKIE_SECURE", "true") != "false",
	}
	raw := os.Getenv("WADIST_CONSOLE_SESSION_KEY")
	if raw == "" {
		return Config{}, fmt.Errorf("console: WADIST_CONSOLE_SESSION_KEY is required")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return Config{}, fmt.Errorf("console: WADIST_CONSOLE_SESSION_KEY base64: %w", err)
	}
	if len(key) < 32 {
		return Config{}, fmt.Errorf("console: WADIST_CONSOLE_SESSION_KEY must be >=32 bytes, got %d", len(key))
	}
	cfg.SessionKey = key
	if v := os.Getenv("WADIST_CONSOLE_SESSION_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.SessionTTL = d
		}
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
