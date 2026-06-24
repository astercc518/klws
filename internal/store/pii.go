// internal/store/pii.go
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/acme/wadist/internal/crypto"
)

// AddRecipientPII inserts a campaign_recipients row with dual-write: plaintext
// phone is retained alongside phone_enc (AES-256-GCM envelope) and phone_bidx
// (HMAC-SHA256 blind index). The unique index on (campaign_id, phone_bidx)
// deduplicates by encrypted phone; on conflict the existing row id is returned.
//
// c must be non-nil and blindKey must be exactly 32 bytes.
func (m *Manager) AddRecipientPII(
	ctx context.Context,
	c *crypto.Cipher,
	blindKey []byte,
	tenantID, campaignID int64,
	phone, country string,
	vars []byte,
) (recipientID int64, err error) {
	if c == nil {
		return 0, errors.New("store: Cipher must not be nil for PII operations")
	}
	if len(blindKey) != 32 {
		return 0, fmt.Errorf("store: blindKey must be 32 bytes, got %d", len(blindKey))
	}
	if vars == nil {
		vars = []byte(`{}`)
	}

	phoneEnc, err := c.Encrypt(ctx, tenantID, []byte(phone))
	if err != nil {
		return 0, fmt.Errorf("store: encrypt phone: %w", err)
	}

	phoneBidx := crypto.BlindIndex(blindKey, phone)

	const q = `
INSERT INTO campaign_recipients
  (tenant_id, campaign_id, phone, country_code, vars, phone_enc, phone_bidx)
VALUES
  ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (campaign_id, phone_bidx) WHERE phone_bidx IS NOT NULL
  DO NOTHING
RETURNING id`

	err = m.bizPool.QueryRow(ctx, q,
		tenantID, campaignID, phone, country, vars, phoneEnc, phoneBidx,
	).Scan(&recipientID)

	if err == nil {
		// Successful INSERT.
		return recipientID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("store: insert recipient: %w", err)
	}

	// Conflict: DO NOTHING returned no row. Fetch the existing id.
	const sel = `
SELECT id FROM campaign_recipients
 WHERE campaign_id = $1 AND phone_bidx = $2`
	err = m.bizPool.QueryRow(ctx, sel, campaignID, phoneBidx).Scan(&recipientID)
	if err != nil {
		return 0, fmt.Errorf("store: fetch existing recipient after conflict: %w", err)
	}
	return recipientID, nil
}

// SetDevicePhonePII encrypts phone and writes it to account_devices.phone_number_enc
// for the given accountJID. The plaintext phone_number column is left unchanged.
//
// c must be non-nil.
func (m *Manager) SetDevicePhonePII(
	ctx context.Context,
	c *crypto.Cipher,
	tenantID int64,
	accountJID, phone string,
) error {
	if c == nil {
		return errors.New("store: Cipher must not be nil for PII operations")
	}

	phoneEnc, err := c.Encrypt(ctx, tenantID, []byte(phone))
	if err != nil {
		return fmt.Errorf("store: encrypt device phone: %w", err)
	}

	const q = `UPDATE account_devices SET phone_number_enc = $1 WHERE account_jid = $2`
	_, err = m.bizPool.Exec(ctx, q, phoneEnc, accountJID)
	if err != nil {
		return fmt.Errorf("store: set device phone_number_enc: %w", err)
	}
	return nil
}
