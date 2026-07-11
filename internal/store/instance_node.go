package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// NodeForInstance returns the evo_node an instance is assigned to.
func (m *Manager) NodeForInstance(ctx context.Context, instanceName string) (string, bool, error) {
	var node string
	err := m.SystemPool().QueryRow(ctx,
		`SELECT evo_node FROM account_instances WHERE instance_name=$1`, instanceName).Scan(&node)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return node, true, nil
}
