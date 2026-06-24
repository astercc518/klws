package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

var ErrKeyNotFound = errors.New("crypto: tenant key not found (shredded?)")

// Cipher does envelope AES-256-GCM: per-tenant DEKs (from KeyRepo) encrypt data;
// the blob layout is version(4B BE) || nonce(12B) || ciphertext+tag. Decrypt reads
// the version to fetch the matching DEK, so rotated ciphertext stays readable.
type Cipher struct{ kr *KeyRepo }

func NewCipher(kr *KeyRepo) *Cipher { return &Cipher{kr: kr} }

func (c *Cipher) Encrypt(ctx context.Context, tenantID int64, plaintext []byte) ([]byte, error) {
	dek, version, err := c.kr.GetCurrentDEK(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, uint32(version))
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

func (c *Cipher) Decrypt(ctx context.Context, tenantID int64, blob []byte) ([]byte, error) {
	if len(blob) < 4+12 {
		return nil, fmt.Errorf("crypto: ciphertext too short")
	}
	version := int32(binary.BigEndian.Uint32(blob[:4]))
	dek, err := c.kr.GetDEK(ctx, tenantID, version)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	nonce := blob[4 : 4+ns]
	ct := blob[4+ns:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: decrypt: %w", err)
	}
	return pt, nil
}

func (c *Cipher) Rotate(ctx context.Context, tenantID int64) (int32, error) {
	return c.kr.CreateTenantKey(ctx, tenantID)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key) // 32 bytes -> AES-256
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	return cipher.NewGCM(blk)
}
