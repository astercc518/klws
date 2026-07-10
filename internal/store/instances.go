// internal/store/instances.go
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// InstanceRow is one row of account_instances (Evolution routing table).
type InstanceRow struct {
	InstanceName string
	JID          string
	EvoNode      string
	State        string
	TenantID     int64
	ProxyID      int64
}

// UpsertInstance creates or updates an instance routing row. Idempotent on
// instance_name. Uses the system (BYPASSRLS) pool: routing has no tenant
// request-context, tenant_id is a stored column.
func (m *Manager) UpsertInstance(ctx context.Context, in InstanceRow) error {
	_, err := m.SystemPool().Exec(ctx, `
INSERT INTO account_instances (instance_name, jid, tenant_id, evo_node, state, updated_at)
VALUES ($1, NULLIF($2,''), $3, $4, $5, now())
ON CONFLICT (instance_name) DO UPDATE
   SET evo_node = EXCLUDED.evo_node, state = EXCLUDED.state, updated_at = now()`,
		in.InstanceName, in.JID, in.TenantID, in.EvoNode, in.State)
	return err
}

// BindInstanceJID back-fills the jid once pairing completes. Idempotent.
func (m *Manager) BindInstanceJID(ctx context.Context, instanceName, jid string) error {
	_, err := m.SystemPool().Exec(ctx,
		`UPDATE account_instances SET jid=$2, updated_at=now() WHERE instance_name=$1`,
		instanceName, jid)
	return err
}

// SetInstanceState updates lifecycle state (created/qr/connected/disconnected/loggedOut).
func (m *Manager) SetInstanceState(ctx context.Context, instanceName, state string) error {
	_, err := m.SystemPool().Exec(ctx,
		`UPDATE account_instances SET state=$2, updated_at=now() WHERE instance_name=$1`,
		instanceName, state)
	return err
}

// JIDForInstance resolves an Evolution instance_name to its bound WA jid.
// ok is false when the instance is unknown or has no jid bound yet.
func (m *Manager) JIDForInstance(ctx context.Context, instanceName string) (string, bool, error) {
	var jid *string
	err := m.SystemPool().QueryRow(ctx,
		`SELECT jid FROM account_instances WHERE instance_name=$1`, instanceName).Scan(&jid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if jid == nil {
		return "", false, nil
	}
	return *jid, true, nil
}

// InstanceForJID resolves a WA jid back to its Evolution instance_name.
// ok is false when no instance is bound to that jid.
func (m *Manager) InstanceForJID(ctx context.Context, jid string) (string, bool, error) {
	var name string
	err := m.SystemPool().QueryRow(ctx,
		`SELECT instance_name FROM account_instances WHERE jid=$1`, jid).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return name, true, nil
}
