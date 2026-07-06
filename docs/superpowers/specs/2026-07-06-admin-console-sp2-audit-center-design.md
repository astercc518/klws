# SP2 — 管理后台:审计中心(Audit Center)

**Date:** 2026-07-06
**Status:** Approved (design)
**Scope:** Second sub-project of "完善管理后台功能模块". Makes the platform's
audit trail real: instruments every admin write action to append an
`audit_log` row, adds a read endpoint, and surfaces an Audit Log tab in the
admin UI. All changes live in `internal/api` (the HTTP layer) and the Next.js
frontend — no engine package (billing/dispatch/store/sendgate/cluster) business
logic is modified.

## Background

The audit subsystem is half-built: `audit_log` (append-only) and `AuditWriter`
exist, but only 3 event types are ever written (`campaign.circuit_break`,
`campaign.resume`, `refund.approve/reject`), there is **no read endpoint**, and
the `/admin/audit` page is actually campaign monitoring — the sidebar label
"风控与审计 / Audit" is unmet. Sensitive admin actions (top-ups, pricing,
user disable, password reset, risk-config edits, force-stop) leave no trail, and
the risk page falsely claims edits are "持久化并审计".

## Goals

1. **Write side:** a centralized audit helper on `Server`, and instrumentation
   of all admin write handlers to append an `audit_log` row.
2. **Read side:** `GET /admin/audit` with filters + pagination + name joins.
3. **UI:** `/admin/audit` becomes two tabs — Audit Log (new, default) + Task
   Monitor (the existing campaign view, unchanged).

## Non-goals (deferred)

- Audit log CSV export, alerting.
- Real refund approve/deny actions (refunds stay read-only, from SP1).
- Upgrading best-effort post-action audit to fully-atomic (would require
  changing engine/store method signatures — separate project).
- Retroactively backfilling audit rows for past actions.

## Existing infrastructure (verified, reused unchanged)

- Table `audit_log (id, occurred_at DEFAULT now(), tenant_id, actor_id, action
  NOT NULL, resource_type, resource_id, details JSONB)`; append-only
  (`REVOKE UPDATE, DELETE`), `migrations/0008_security.sql:51`.
- `sessionFrom(c) *sessionData` with `.UserID` — actor id, available in every
  admin handler (e.g. `admin_api.go:857`, `:937`).
- Precedent for an in-transaction raw `INSERT INTO audit_log` is
  `handleAdminResumeCampaign` (`admin_api.go:876-878`).
- `s.deps.Mgr.SystemPool()` (BYPASSRLS system pool) is available in all admin
  handlers and already used for every admin query.

## Component 1 — Audit helper (`internal/api/audit.go`, new)

Centralize the insert so 13 handlers don't each repeat SQL.

```go
// auditEvent is one row to append. Zero-value id fields → SQL NULL.
type auditEvent struct {
    TenantID     int64  // 0 → NULL
    ActorID      int64  // 0 → NULL
    Action       string // required, e.g. "finance.topup"
    ResourceType string // "" → NULL
    ResourceID   int64  // 0 → NULL
    Details      any    // JSON-marshaled; nil → '{}'::jsonb
}

// actorID returns the current session's user id, or 0 if none.
func actorID(c *gin.Context) int64 { if sd := sessionFrom(c); sd != nil { return sd.UserID }; return 0 }

// recordAudit appends best-effort AFTER the action has committed. On failure it
// logs and returns — it never fails the caller's already-committed action.
func (s *Server) recordAudit(ctx context.Context, e auditEvent)

// recordAuditTx appends within an existing tx (atomic with the action). Returns
// the error so the caller can roll the whole action back on audit failure.
func (s *Server) recordAuditTx(ctx context.Context, tx pgx.Tx, e auditEvent) error
```

Both marshal `Details` to JSON (`nil`/marshal-error → `'{}'`), map zero ids to
NULL, and run the same `INSERT INTO audit_log (tenant_id, actor_id, action,
resource_type, resource_id, details) VALUES (...)`. `recordAudit` uses
`s.deps.Mgr.SystemPool()`; on error it logs via the package's logger and
swallows. Never logs `Details` contents that could carry secrets (there are
none by design — see the table below).

## Component 2 — Instrument all admin write handlers

All in `internal/api/admin_api.go`. After the mutation succeeds and before
`ok(c, ...)`, call `s.recordAudit(ctx, auditEvent{...})` (best-effort) — except
the two campaign handlers, which already own a tx and use `recordAuditTx`.

| Handler | action | resource_type | resource_id | details |
|---|---|---|---|---|
| `handleAdminTopup` | `finance.topup` | `tenant` | tenant_id | `{amount, ref}` |
| `handleAdminSetPricing` | `finance.pricing` | `tenant` | tenant_id | `{country, unit_price}` |
| `handleAdminCreateTenant` | `tenant.create` | `tenant` | new id | `{name}` |
| `handleAdminCreateUser` | `user.create` | `user` | new id | `{email, role, tenant_id}` |
| `handleAdminSetUserDisabled` | `user.disable` | `user` | user id | `{disabled}` |
| `handleAdminResetUserPassword` | `user.password_reset` | `user` | user id | `{}` — **never the password** |
| `handleAdminAssignSales` | `sales.assign` | `tenant` | tenant_id | `{sales_user_id}` |
| `handleAdminUpdateRiskConfig` | `risk.update` | `system_risk_config` | 1 | persisted new config |
| `handleAdminStopCampaign` | `campaign.stop` | `campaign` | campaign id | `{}` (in-tx, atomic) |
| `handleAdminImportProxies` | `proxy.import` | `proxy` | 0 | `{submitted, imported, skipped}` |
| `handleAdminImportDevices` | `device.import` | `device` | 0 | `{submitted, imported, skipped}` |
| `handleAdminBindDeviceProxy` | `device.bind_proxy` | `device` | device id | `{proxy_id}` |
| `handleAdminUnbindDeviceProxy` | `device.unbind_proxy` | `device` | device id | `{}` |

`campaign.resume` already writes its row (`admin_api.go:876`) — leave as-is; the
new helper is not retrofitted onto it to avoid churn, but its action string
(`campaign.resume`) is part of the taxonomy the UI filters on.

`handleAdminStopCampaign` currently runs a single `UPDATE`; wrap it in
`pgx.BeginTxFunc` (mirroring resume) so the `UPDATE` + `recordAuditTx` are
atomic. Only audit when a row was actually paused (state transition happened),
matching resume's `resumed` guard.

Once `risk.update` writes an audit row, the risk page's "持久化并审计" claim is
true — no copy change needed.

## Component 3 — Read endpoint `GET /admin/audit`

New handler `handleAdminListAudit` + route `admin.GET("/audit", ...)` in
`router.go` (admin group). Query params (all optional): `action` (exact),
`actor_id`, `tenant_id`, `since`/`until` (RFC3339 → timestamptz), `limit`
(default 100, clamp 1..500), `offset` (default 0).

SQL builds a dynamic `WHERE` from the supplied filters and joins names:

```sql
SELECT a.id, a.occurred_at::text, a.tenant_id, t.name AS tenant_name,
       a.actor_id, u.email AS actor_email,
       a.action, a.resource_type, a.resource_id, a.details
  FROM audit_log a
  LEFT JOIN tenants t      ON t.id = a.tenant_id
  LEFT JOIN console_users u ON u.id = a.actor_id
 WHERE <filters>
 ORDER BY a.id DESC
 LIMIT $n OFFSET $m
```

Plus a `SELECT count(*) FROM audit_log a WHERE <filters>` for `total`.
Response: `{ rows: auditRow[], total: number }` where

```go
type auditRow struct {
    ID           int64           `json:"id"`
    OccurredAt   string          `json:"occurred_at"`
    TenantID     *int64          `json:"tenant_id"`
    TenantName   *string         `json:"tenant_name"`
    ActorID      *int64          `json:"actor_id"`
    ActorEmail   *string         `json:"actor_email"`
    Action       string          `json:"action"`
    ResourceType *string         `json:"resource_type"`
    ResourceID   *int64          `json:"resource_id"`
    Details      json.RawMessage `json:"details"`
}
```

Server-side join means the frontend renders directly, no client-side name map.

## Component 4 — Frontend: `/admin/audit` two tabs

- `frontend/app/admin/audit/page.tsx`: keep `PageHeader`, replace the single
  `<AdminCampaigns />` with a new client component `AdminAuditTabs`.
- `frontend/components/admin-audit-tabs.tsx` (new): a lightweight local
  segmented control (two buttons + conditional render — no new shared primitive;
  `@base-ui/react/tabs` exists but is overkill for two tabs). Default tab =
  审计日志; second tab = 任务监控 rendering the existing `<AdminCampaigns />`
  unchanged.
- `frontend/components/admin-audit-log.tsx` (new): fetches `GET /admin/audit`,
  renders a `ProDataTable`. Columns: 时间 (`occurred_at`), 操作人
  (`actor_email` or `#actor_id` or "系统"), 动作 (`action` as a colored
  `Badge`), 对象 (`resource_type` + `#resource_id`), 详情 (`details` shown
  compactly — a truncated JSON string or key/value chips; expand on demand).
  Toolbar: an `action` filter dropdown (built from the known action taxonomy)
  and the built-in search. Uses server `limit`/`offset` via the endpoint but MAY
  start with a single page (default 100) + client search/paging through
  `ProDataTable`, consistent with the Ledger page (server pagination wiring is
  optional polish, not required for SP2).
- The nav item "风控与审计 / Audit" label is unchanged — now accurate.

## Error handling

- Write side: `recordAudit` is best-effort — a failed audit insert logs and is
  swallowed, never surfacing to the user or undoing the committed action.
  `recordAuditTx` (campaign.stop) returns its error into the tx so a failed
  audit rolls back the stop (the action hasn't committed yet).
- Read side: filter parse errors → `fail(c, 400, ...)`; query errors →
  `fail(c, 500, ...)`. Frontend: 401 swallowed (global interceptor); other
  errors → `ProDataTable error=`.

## Testing

Backend has real Go coverage (`internal/api/admin_test.go`,
`admin_write_test.go`) — SP2 follows **TDD**:

- **Write side** (one test per action group; assert the row lands): after
  calling each instrumented handler, assert exactly one new `audit_log` row with
  the expected `action`, `actor_id` (= the acting session user), `resource_type`,
  `resource_id`, and the expected `details` JSON. Assert `user.password_reset`
  details do **not** contain the password.
- **campaign.stop atomicity:** assert the audit row and the `state='paused'`
  update both land, and that a no-op stop (already paused) writes no row.
- **Read endpoint:** seed several audit rows; assert filtering by `action`,
  `actor_id`, `tenant_id`, and `since`/`until` each returns the right subset;
  assert `limit`/`offset` paginate; assert `total` reflects the filtered count;
  assert `actor_email`/`tenant_name` join correctly and are null for
  system/unknown ids.
- **Frontend:** no JS runner — verify via `npm run build` + `eslint` (no new
  errors beyond the accepted repo-wide `react-hooks/set-state-in-effect`
  pattern) + manual smoke of the two tabs.

## File-change summary

| File | Change |
|---|---|
| `internal/api/audit.go` | **new** — `auditEvent`, `actorID`, `recordAudit`, `recordAuditTx` |
| `internal/api/admin_api.go` | instrument 12 handlers + wrap `handleAdminStopCampaign` in a tx; add `handleAdminListAudit` |
| `internal/api/router.go` | add `admin.GET("/audit", s.handleAdminListAudit)` |
| `internal/api/*_test.go` | TDD tests for write instrumentation + read endpoint |
| `frontend/app/admin/audit/page.tsx` | render `AdminAuditTabs` instead of `AdminCampaigns` |
| `frontend/components/admin-audit-tabs.tsx` | **new** — two-tab segmented control |
| `frontend/components/admin-audit-log.tsx` | **new** — audit log table + filters |

No engine-package files change.
