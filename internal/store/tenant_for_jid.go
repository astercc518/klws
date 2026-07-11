package store

import (
	"context"
	"fmt"
)

// TenantForJID returns the tenant that owns an account, from account_devices.
func (m *Manager) TenantForJID(ctx context.Context, accountJID string) (int64, error) {
	var tid int64
	err := m.SystemPool().QueryRow(ctx,
		`SELECT tenant_id FROM account_devices WHERE account_jid=$1`, accountJID).Scan(&tid)
	if err != nil {
		return 0, fmt.Errorf("tenant for jid %s: %w", accountJID, err)
	}
	return tid, nil
}
