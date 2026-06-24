// internal/store/suppression.go
package store

import (
	"context"
	"fmt"

	"github.com/acme/wadist/internal/crypto"
)

// AddSuppression inserts a (tenant_id, phone_bidx) row into suppression_list.
// blindKey must be exactly 32 bytes. On duplicate (tenant_id, phone_bidx) the
// row is silently kept (ON CONFLICT DO NOTHING — idempotent).
func (m *Manager) AddSuppression(ctx context.Context, blindKey []byte, tenantID int64, phone, reason string) error {
	if len(blindKey) != 32 {
		return fmt.Errorf("store: blindKey must be 32 bytes, got %d", len(blindKey))
	}
	bidx := crypto.BlindIndex(blindKey, phone)
	_, err := m.bizPool.Exec(ctx, `
INSERT INTO suppression_list (tenant_id, phone_bidx, reason)
VALUES ($1, $2, $3)
ON CONFLICT (tenant_id, phone_bidx) DO NOTHING`,
		tenantID, bidx, reason)
	if err != nil {
		return fmt.Errorf("store: add suppression: %w", err)
	}
	return nil
}
