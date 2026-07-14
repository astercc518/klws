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
// request-context, tenant_id is a stored column. ProxyID is optional
// (NULLIF(...,0) so the zero value of an unset field maps to SQL NULL,
// preserving prior behavior for callers — e.g. cmd/wadist/main.go's jid-sticky
// path — that never set it).
func (m *Manager) UpsertInstance(ctx context.Context, in InstanceRow) error {
	_, err := m.SystemPool().Exec(ctx, `
INSERT INTO account_instances (instance_name, jid, tenant_id, evo_node, proxy_id, state, updated_at)
VALUES ($1, NULLIF($2,''), $3, $4, NULLIF($5,0), $6, now())
ON CONFLICT (instance_name) DO UPDATE
   SET evo_node = EXCLUDED.evo_node, proxy_id = EXCLUDED.proxy_id, state = EXCLUDED.state, updated_at = now()`,
		in.InstanceName, in.JID, in.TenantID, in.EvoNode, in.ProxyID, in.State)
	return err
}

// BindInstanceProxy allocates one alive proxy with free capacity for
// countryCode and durably increments its proxy_pool counters — WITHOUT
// touching account_devices or account_instances.
//
// This is deliberately NOT BindProxy (proxy.go): BindProxy's durable write is
// an `UPDATE account_devices ... WHERE account_jid=$accountJID`, which returns
// ErrAccountMissing when no row exists yet for that key. At instance-creation
// time (POST /admin/instances) there is no account_devices row — the account
// isn't onboarded yet, may never get a real jid this session, and the
// account_instances row doesn't exist until the caller's follow-up
// UpsertInstance runs. Keying the allocation off instance_name would hit the
// exact same ErrAccountMissing wall. So this method only does the pool-side
// half of what `bind` does (redis pick + proxy_pool counters); the caller
// persists the returned ProxyID itself via UpsertInstance{ProxyID: ...}.
//
// Rollback contract: if anything downstream fails (SetProxy, UpsertInstance),
// the caller MUST call ReleaseInstanceProxy with the same ProxyID/Country to
// avoid leaking pool capacity — this method does not register a row an
// automatic release could key off of.
func (m *Manager) BindInstanceProxy(ctx context.Context, countryCode string) (*ProxyBinding, error) {
	nowMs := time.Now().UnixMilli()
	id, meta, err := m.proxyAlloc.pick(ctx, countryCode, nowMs)
	if err != nil {
		return nil, err // ErrNoProxyAvailable on miss
	}
	url, ptype, mcc, ok := parseMeta(meta)
	if !ok {
		_ = m.proxyAlloc.release(ctx, id, countryCode, nowMs) // return the slot; corrupt meta
		return nil, fmt.Errorf("proxy meta corrupt for id %d: %q", id, meta)
	}
	if _, err := m.bizPool.Exec(ctx,
		`UPDATE proxy_pool SET current_bindings=current_bindings+1, usage_count=usage_count+1 WHERE id=$1`, id); err != nil {
		_ = m.proxyAlloc.release(ctx, id, countryCode, nowMs) // PG failed → don't leak capacity in Redis
		return nil, err
	}
	return &ProxyBinding{ProxyID: id, ProxyURL: url, ProxyType: ptype, Country: mcc}, nil
}

// ReleaseInstanceProxy is BindInstanceProxy's inverse: decrements proxy_id's
// proxy_pool counter and returns its slot to the Redis hot index. It does NOT
// touch account_devices or account_instances (mirrors BindInstanceProxy's
// scope). Used both for fail-closed rollback (SetProxy/UpsertInstance failed
// after a successful bind) and, later, for instance teardown. Best-effort
// idempotent on a bad/already-released id: the PG UPDATE is a no-op (0 rows)
// and releaseLua no-ops on unknown members, so a double-release is safe.
func (m *Manager) ReleaseInstanceProxy(ctx context.Context, proxyID int64, countryCode string) error {
	if _, err := m.bizPool.Exec(ctx,
		`UPDATE proxy_pool SET current_bindings=GREATEST(current_bindings-1,0) WHERE id=$1`, proxyID); err != nil {
		return err
	}
	return m.proxyAlloc.release(ctx, proxyID, countryCode, time.Now().UnixMilli())
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

// GetInstance looks up one account_instances row by instance_name. ok is
// false when no such instance exists (callers 404). Uses the SystemPool
// (BYPASSRLS): this backs the admin god-view lifecycle endpoints
// (qr/state/reconnect/logout/delete), which have no tenant request-context.
func (m *Manager) GetInstance(ctx context.Context, instanceName string) (InstanceRow, bool, error) {
	var row InstanceRow
	var jid *string
	var proxyID *int64
	err := m.SystemPool().QueryRow(ctx,
		`SELECT instance_name, jid, tenant_id, evo_node, proxy_id, state
		   FROM account_instances WHERE instance_name=$1`, instanceName).
		Scan(&row.InstanceName, &jid, &row.TenantID, &row.EvoNode, &proxyID, &row.State)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InstanceRow{}, false, nil
		}
		return InstanceRow{}, false, err
	}
	if jid != nil {
		row.JID = *jid
	}
	if proxyID != nil {
		row.ProxyID = *proxyID
	}
	return row, true, nil
}

// DeleteInstanceRow removes one account_instances row by instance_name.
// Idempotent (0 rows affected is not an error) — the delete endpoint calls
// this ONLY after the Evolution DeleteInstance round-trip succeeds, so an
// Evolution failure never leaves an orphaned Evolution-side instance
// unreferenced-but-undeleted on our side, nor does a DB failure here leave a
// deleted-in-Evolution instance still routable.
func (m *Manager) DeleteInstanceRow(ctx context.Context, instanceName string) error {
	_, err := m.SystemPool().Exec(ctx, `DELETE FROM account_instances WHERE instance_name=$1`, instanceName)
	return err
}

// ReleaseInstanceProxyByID releases proxyID's pool slot on instance teardown.
// Unlike ReleaseInstanceProxy (called during create-time rollback, when the
// caller already has the country_code from the just-made ProxyBinding), the
// delete endpoint only has the account_instances row's proxy_id — so this
// looks up country_code from proxy_pool first, then delegates. No-op when
// proxyID is 0 (no proxy was ever bound) or the proxy row is already gone.
func (m *Manager) ReleaseInstanceProxyByID(ctx context.Context, proxyID int64) error {
	if proxyID == 0 {
		return nil
	}
	var cc string
	err := m.bizPool.QueryRow(ctx, `SELECT country_code FROM proxy_pool WHERE id=$1`, proxyID).Scan(&cc)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	return m.ReleaseInstanceProxy(ctx, proxyID, cc)
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
