// internal/console/session.go
package console

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// SessionData is the server-side session payload stored in Redis.
type SessionData struct {
	UserID   int64  `json:"uid"`
	Role     Role   `json:"role"`
	TenantID *int64 `json:"tid,omitempty"`
}

var ErrNoSession = errors.New("console: no session")

const sessionKeyPrefix = "console:session:"

// SessionStore persists sessions in Redis with a TTL.
type SessionStore struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewSessionStore(rdb *redis.Client, ttl time.Duration) *SessionStore {
	return &SessionStore{rdb: rdb, ttl: ttl}
}

func newSID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *SessionStore) Create(ctx context.Context, d SessionData) (string, error) {
	sid, err := newSID()
	if err != nil {
		return "", fmt.Errorf("session id: %w", err)
	}
	payload, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("marshal session: %w", err)
	}
	if err := s.rdb.Set(ctx, sessionKeyPrefix+sid, payload, s.ttl).Err(); err != nil {
		return "", fmt.Errorf("redis set: %w", err)
	}
	return sid, nil
}

func (s *SessionStore) Get(ctx context.Context, sid string) (*SessionData, error) {
	raw, err := s.rdb.Get(ctx, sessionKeyPrefix+sid).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("redis get: %w", err)
	}
	var d SessionData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return &d, nil
}

func (s *SessionStore) Destroy(ctx context.Context, sid string) error {
	if err := s.rdb.Del(ctx, sessionKeyPrefix+sid).Err(); err != nil {
		return fmt.Errorf("redis del: %w", err)
	}
	return nil
}

// SignCookie returns "<sid>.<base64 hmac>" so a tampered sid is detectable.
func SignCookie(secret []byte, sid string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sid))
	return sid + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyCookie validates the HMAC and returns the embedded sid.
func VerifyCookie(secret []byte, value string) (string, bool) {
	i := strings.LastIndexByte(value, '.')
	if i <= 0 {
		return "", false
	}
	sid, sig := value[:i], value[i+1:]
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sid))
	if subtle.ConstantTimeCompare(want, mac.Sum(nil)) != 1 {
		return "", false
	}
	return sid, true
}
