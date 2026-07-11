package store

import "context"

// NodeCounts returns the number of account_instances rows per evo_node. Used by
// nodering.AssignNode for capacity-aware sharding. Uses SystemPool (sharding is
// fleet-wide, no tenant context). Empty table → empty (non-nil) map.
func (m *Manager) NodeCounts(ctx context.Context) (map[string]int, error) {
	rows, err := m.SystemPool().Query(ctx,
		`SELECT evo_node, count(*) FROM account_instances GROUP BY evo_node`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var node string
		var n int
		if err := rows.Scan(&node, &n); err != nil {
			return nil, err
		}
		out[node] = n
	}
	return out, rows.Err()
}
