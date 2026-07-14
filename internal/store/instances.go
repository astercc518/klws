// internal/store/instances.go
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
		`UPDATE account_instances SET jid=NULLIF($2,''), updated_at=now() WHERE instance_name=$1`,
		instanceName, jid)
	return err
}

// BindInstanceJIDIfUnset binds the jid only when the row's jid is currently
// NULL (first-see). Idempotent; a later differing jid is ignored (prevents a
// forged connection.update from rebinding an instance). NULLIF guards empty.
func (m *Manager) BindInstanceJIDIfUnset(ctx context.Context, instanceName, jid string) error {
	_, err := m.SystemPool().Exec(ctx,
		`UPDATE account_instances SET jid=NULLIF($2,''), updated_at=now()
		 WHERE instance_name=$1 AND jid IS NULL`,
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

// ---------------------------------------------------------------------------
// Admin cross-tenant instance list (god view, read-only)
// ---------------------------------------------------------------------------

// InstanceListRow is one row of the admin cross-tenant instance list,
// account_instances joined to tenants for the display name. JID and ProxyID
// are nullable columns (an unpaired instance has no jid yet; a proxy binding
// is optional).
type InstanceListRow struct {
	InstanceName string
	JID          *string
	TenantID     int64
	TenantName   string
	EvoNode      string
	ProxyID      *int64
	State        string
	UpdatedAt    time.Time
}

// InstanceFilter is the parsed query for the admin cross-tenant instance list.
type InstanceFilter struct {
	State    string
	Node     string
	Q        string
	TenantID int64
	Limit    int
	Offset   int
}

// buildInstanceWhere returns the " WHERE ..." clause (or "") and positional
// args for account_instances aliased "ai". State/Node/TenantID are exact
// matches; Q is an ILIKE substring match against instance_name or jid.
func buildInstanceWhere(f InstanceFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(tmpl string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(tmpl, len(args)))
	}
	if f.State != "" {
		add("ai.state = $%d", f.State)
	}
	if f.Node != "" {
		add("ai.evo_node = $%d", f.Node)
	}
	if f.TenantID != 0 {
		add("ai.tenant_id = $%d", f.TenantID)
	}
	if f.Q != "" {
		args = append(args, "%"+f.Q+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf("(ai.instance_name ILIKE $%d OR ai.jid ILIKE $%d)", n, n))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListInstances returns a page of account_instances across ALL tenants (admin
// god view), joined to tenants for the display name, the total row count
// matching the filter, and a global (unfiltered) state → count breakdown.
// Uses the SystemPool (BYPASSRLS): this is a cross-tenant admin read with no
// tenant request-context, not a tenant-scoped one. Read-only — it never
// touches account_instances beyond SELECT, and never reaches the Evolution
// cluster.
func (m *Manager) ListInstances(ctx context.Context, f InstanceFilter) ([]InstanceListRow, int, map[string]int, error) {
	pool := m.SystemPool()
	where, args := buildInstanceWhere(f)

	limit := f.Limit
	if limit <= 0 {
		limit = 20
	} else if limit > 200 {
		limit = 200
	}
	listArgs := append(append([]any{}, args...), limit, f.Offset)
	list := `
SELECT ai.instance_name, ai.jid, ai.tenant_id, COALESCE(t.name, ''), ai.evo_node, ai.proxy_id, ai.state, ai.updated_at
  FROM account_instances ai
  LEFT JOIN tenants t ON t.id = ai.tenant_id` + where +
		fmt.Sprintf(" ORDER BY ai.updated_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := pool.Query(ctx, list, listArgs...)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("list instances: %w", err)
	}
	defer rows.Close()
	out := make([]InstanceListRow, 0)
	for rows.Next() {
		var r InstanceListRow
		if err := rows.Scan(&r.InstanceName, &r.JID, &r.TenantID, &r.TenantName, &r.EvoNode, &r.ProxyID, &r.State, &r.UpdatedAt); err != nil {
			return nil, 0, nil, fmt.Errorf("scan instance: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, nil, fmt.Errorf("iterate instances: %w", err)
	}

	var total int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM account_instances ai`+where, args...).Scan(&total); err != nil {
		return nil, 0, nil, fmt.Errorf("count instances: %w", err)
	}

	stats := make(map[string]int)
	srows, err := pool.Query(ctx, `SELECT state, count(*) FROM account_instances GROUP BY state`)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("instance state stats: %w", err)
	}
	defer srows.Close()
	for srows.Next() {
		var st string
		var n int
		if err := srows.Scan(&st, &n); err != nil {
			return nil, 0, nil, fmt.Errorf("scan instance stats: %w", err)
		}
		stats[st] = n
	}
	if err := srows.Err(); err != nil {
		return nil, 0, nil, fmt.Errorf("iterate instance stats: %w", err)
	}

	return out, total, stats, nil
}
