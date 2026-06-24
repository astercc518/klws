package crypto

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// KeyRepo manages per-tenant DEKs wrapped by the master KEK. The wrapped key is
// itself an AES-256-GCM blob (nonce||ct) so the KEK never touches plaintext DEKs
// at rest. Shred deletes a tenant's keys, making its ciphertext unrecoverable.
type KeyRepo struct {
	pool *pgxpool.Pool
	kek  []byte // 32 bytes
}

func NewKeyRepo(pool *pgxpool.Pool, masterKey []byte) (*KeyRepo, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("crypto: master key must be 32 bytes, got %d", len(masterKey))
	}
	return &KeyRepo{pool: pool, kek: masterKey}, nil
}

func (k *KeyRepo) wrap(dek []byte) ([]byte, error) {
	gcm, err := newGCM(k.kek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, dek, nil), nil
}

func (k *KeyRepo) unwrap(blob []byte) ([]byte, error) {
	gcm, err := newGCM(k.kek)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(blob) < ns {
		return nil, fmt.Errorf("crypto: wrapped key too short")
	}
	return gcm.Open(nil, blob[:ns], blob[ns:], nil)
}

func (k *KeyRepo) CreateTenantKey(ctx context.Context, tenantID int64) (int32, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return 0, fmt.Errorf("dek: %w", err)
	}
	wrapped, err := k.wrap(dek)
	if err != nil {
		return 0, err
	}
	var version int32
	err = k.pool.QueryRow(ctx, `
INSERT INTO tenant_keys (tenant_id, version, key_enc)
VALUES ($1, COALESCE((SELECT max(version) FROM tenant_keys WHERE tenant_id=$1), 0) + 1, $2)
RETURNING version`, tenantID, wrapped).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("create tenant key: %w", err)
	}
	return version, nil
}

func (k *KeyRepo) GetCurrentDEK(ctx context.Context, tenantID int64) ([]byte, int32, error) {
	var wrapped []byte
	var version int32
	err := k.pool.QueryRow(ctx, `
SELECT key_enc, version FROM tenant_keys
 WHERE tenant_id=$1 ORDER BY version DESC LIMIT 1`, tenantID).Scan(&wrapped, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, ErrKeyNotFound
	}
	if err != nil {
		return nil, 0, fmt.Errorf("get current dek: %w", err)
	}
	dek, err := k.unwrap(wrapped)
	if err != nil {
		return nil, 0, fmt.Errorf("unwrap dek: %w", err)
	}
	return dek, version, nil
}

func (k *KeyRepo) GetDEK(ctx context.Context, tenantID int64, version int32) ([]byte, error) {
	var wrapped []byte
	err := k.pool.QueryRow(ctx,
		`SELECT key_enc FROM tenant_keys WHERE tenant_id=$1 AND version=$2`, tenantID, version).Scan(&wrapped)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get dek v%d: %w", version, err)
	}
	return k.unwrap(wrapped)
}

func (k *KeyRepo) Shred(ctx context.Context, tenantID int64) error {
	_, err := k.pool.Exec(ctx, `DELETE FROM tenant_keys WHERE tenant_id=$1`, tenantID)
	if err != nil {
		return fmt.Errorf("shred tenant keys: %w", err)
	}
	return nil
}
