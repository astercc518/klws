// internal/console/password.go
// Package console implements the wadist management web platform: login,
// sessions, RBAC, tenant-context middleware, and server-rendered pages.
package console

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemoryKiB = 64 * 1024 // 64 MiB
	argonTime      = 1
	argonThreads   = 4
	argonKeyLen    = 32
	argonSaltLen   = 16
)

// HashPassword returns a PHC-style argon2id encoding of plain.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("argon2: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether plain matches the PHC-encoded argon2id hash.
func VerifyPassword(encoded, plain string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=65536,t=1,p=4", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("argon2: malformed hash")
	}
	var version, mem, iterations, threads int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("argon2: bad version: %w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("argon2: unsupported version %d", version)
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iterations, &threads); err != nil {
		return false, fmt.Errorf("argon2: bad params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("argon2: bad salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("argon2: bad hash: %w", err)
	}
	got := argon2.IDKey([]byte(plain), salt, uint32(iterations), uint32(mem), uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
