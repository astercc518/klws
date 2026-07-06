# SP3b — 管理后台:编辑/删除 CRUD

**Date:** 2026-07-06
**Status:** Approved (design)
**Scope:** Second half of SP3. Adds edit + (per-entity) delete/status operations
for the four admin entities — users, tenants, proxies, devices — with
per-entity delete semantics dictated by the foreign-key analysis. Built on top
of the paginated lists from SP3a. Confined to `internal/api` + frontend; no
engine packages.

## Background

The admin console can create + list all four entities but cannot edit or remove
them; the only lifecycle mutations that exist are user disable/enable, user
password reset, tenant create + sales assign, and proxy/device import + bind.
The FK analysis established that hard-delete is only safe for proxies (`ON
DELETE SET NULL` on `account_devices.proxy_id`) and devices (nothing references
them); users are referenced by `tenants.sales_owner_id` and by `audit_log`
(preserve the trail), and tenants are referenced by pricing/wallets/users/
campaigns/audit — so both are soft-lifecycle only.

## Goals

Per-entity edit + the safe destructive/lifecycle op, each auditing the change:

| Entity | Edit endpoint | Delete / status endpoint |
|---|---|---|
| User | `PUT /admin/users/:id` `{email, role, tenant_id}` | — (existing disable is the lifecycle) |
| Tenant | `PUT /admin/tenants/:id` `{name}` | `POST /admin/tenants/:id/status` `{status}` (active/suspended) |
| Proxy | `PUT /admin/resources/proxies/:id` `{proxy_type, country_code, max_bindings}` | `DELETE /admin/resources/proxies/:id` (hard) |
| Device | `PUT /admin/resources/devices/:id` `{tenant_id?, phone_number?, tags?}` | `DELETE /admin/resources/devices/:id` (hard, releases proxy binding) |

New audit actions: `user.update`, `tenant.update`, `tenant.status`,
`proxy.update`, `proxy.delete`, `device.update`, `device.delete`.

## Non-goals (deferred)

- Hard-deleting users or tenants (FK + audit-trail integrity).
- **Enforcing `suspended`** — SP3b sets/shows the status but does NOT block a
  suspended tenant's customers from login or sending (that touches auth/dispatch;
  separate project). `suspended` remains a marker this round.
- Editing identity columns: `account_jid` (device), `proxy_url` (proxy) — change
  = delete + re-import.
- Proxy health-check / device reconnect (engine).

## Existing infrastructure reused

- SP2 `recordAudit`/`recordAuditTx` + `actorID(c)` for auditing every mutation.
- SP2 testcontainers harness (`testPool`, `seedTenantUser`); SP3a `seedDevice`,
  `seedProxy`.
- The device-unbind transaction pattern in `handleAdminUnbindDeviceProxy`
  (`SELECT proxy_id … FOR UPDATE` → `UPDATE proxy_pool SET current_bindings =
  GREATEST(current_bindings-1,0)` → clear binding) — device DELETE replicates its
  decrement step, then deletes the row, in one tx.
- The role/tenant validation from `handleAdminCreateUser` (customer requires a
  tenant_id; admin/sales must have none) — user edit applies the same rule.

## Component 1 — User edit

`PUT /admin/users/:id` `{email, role, tenant_id?}`. Validation mirrors create:
`role ∈ {admin,sales,customer}`; customer → `tenant_id` required (>0); admin/
sales → `tenant_id` forced null. `UPDATE console_users SET email=$, role=$,
tenant_id=$ WHERE id=$`. A duplicate email (unique violation) → 409. A missing id
→ 404 (0 rows). On success → `recordAudit(user.update, resource user/id,
details {email, role, tenant_id})`. Password stays out (separate reset endpoint).

## Component 2 — Tenant edit + status

- `PUT /admin/tenants/:id` `{name}` (name required, non-empty) → `UPDATE tenants
  SET name=$ WHERE id=$`; 404 on no row; `recordAudit(tenant.update, tenant/id,
  {name})`.
- `POST /admin/tenants/:id/status` `{status}` where `status ∈ {active,
  suspended}` (matches the CHECK constraint) → `UPDATE tenants SET status=$
  WHERE id=$`; 400 on invalid status; 404 on no row; `recordAudit(tenant.status,
  tenant/id, {status})`.

## Component 3 — Proxy edit + delete

- `PUT /admin/resources/proxies/:id` `{proxy_type, country_code, max_bindings}`.
  Validate `proxy_type ∈ {socks5,http,https}`, `country_code` length 2,
  `max_bindings ≥ 1`. **`max_bindings` must be ≥ the proxy's current
  `current_bindings`** (else the `chk_bindings` CHECK would fail) — validate in a
  single `UPDATE … WHERE id=$ AND max_bindings >= (SELECT current_bindings …)`
  form or read-then-check; on violation → 400 "max_bindings below current
  bindings". 404 on no row. `recordAudit(proxy.update, proxy/id, {proxy_type,
  country_code, max_bindings})`.
- `DELETE /admin/resources/proxies/:id` → `DELETE FROM proxy_pool WHERE id=$`
  (the FK `ON DELETE SET NULL` nulls `account_devices.proxy_id` automatically).
  404 on no row. `recordAudit(proxy.delete, proxy/id)`.

## Component 4 — Device edit + delete

- `PUT /admin/resources/devices/:id` `{tenant_id?, phone_number?, tags?}`.
  Only the provided fields update (`COALESCE`-style or build a dynamic SET).
  If `tenant_id` provided, validate it exists (`SELECT 1 FROM tenants WHERE id=$`)
  → 400 if not. 404 on no device row. `recordAudit(device.update, device/id,
  {tenant_id, phone_number, tags})` (only the changed keys).
- `DELETE /admin/resources/devices/:id` in a tx (reusing the unbind pattern):
  `SELECT proxy_id FROM account_devices WHERE id=$ FOR UPDATE` (404 if none); if
  `proxy_id` non-null, `UPDATE proxy_pool SET current_bindings =
  GREATEST(current_bindings-1,0) WHERE id=proxy_id`; then `DELETE FROM
  account_devices WHERE id=$`; `recordAuditTx(device.delete, device/id, {})`.

## Component 5 — Frontend row actions

- **`admin-users-table.tsx`:** add an "编辑" item to the existing customer/staff
  row dropdown (opens an `EditUserDialog` with email/role/tenant, mirroring the
  create dialog's fields + validation) → `PUT /admin/users/:id` → reload. Keep
  disable + reset password.
- **`admin-tenants-table.tsx`:** on customer + placeholder rows add "编辑名称"
  (`EditTenantDialog`, name) and "挂起/恢复" (toggles status via `POST
  /admin/tenants/:id/status`, label reflects current status). Reload after.
- **`admin-proxies.tsx`:** add a `rowActions` dropdown (it has none today) with
  "编辑" (`EditProxyDialog`: type/country/max_bindings) and "删除" (confirm
  dialog → `DELETE`). Reload the current page after either.
- **`admin-devices.tsx`:** extend the existing row-actions menu with "编辑"
  (`EditDeviceDialog`: tenant/phone/tags) and "删除" (confirm dialog → `DELETE`),
  keeping "配置网络". Reload the current page after.
- All destructive deletes use a confirmation dialog; all mutations toast on
  success/`ApiError` and are disabled-until-valid + busy-guarded (matching the
  existing dialog patterns).

## Error handling

- Backend: validation → `fail(c, 400, …)`; not-found → 404; unique-email
  conflict → 409; other errors → 500. Audit is best-effort (post-commit) except
  device delete which uses `recordAuditTx` inside its tx.
- Frontend: 401 swallowed; other errors → `ApiError.message` toast.

## Testing

- **Backend (Go, testcontainers):** DB round-trip per endpoint. Emphasis cases:
  - Device DELETE **decrements the bound proxy's `current_bindings`** (seed a
    device bound to a proxy at bindings=1 → delete → assert proxy bindings=0 and
    device gone); deleting an unbound device touches no proxy; 404 on missing.
  - Proxy edit **rejects `max_bindings < current_bindings`** (400) and accepts
    `≥`; proxy DELETE nulls bound devices' `proxy_id`.
  - User edit enforces role/tenant rule (customer w/o tenant → 400; staff with
    tenant → tenant nulled) and 409 on duplicate email.
  - Tenant status rejects an invalid value (400) and 404 on missing id.
  - Each successful mutation writes the expected `audit_log` action row.
- **Frontend:** no JS runner — `npm run build` + `eslint` (no new error type
  beyond the accepted `react-hooks/set-state-in-effect`) + manual smoke.

## File-change summary

| File | Change |
|---|---|
| `internal/api/admin_api.go` | 7 new handlers (user/tenant×2/proxy×2/device×2) + audit |
| `internal/api/router.go` | 7 new routes (PUT/POST/DELETE under admin) |
| `internal/api/crud_db_test.go` | **new** — DB round-trip tests per endpoint |
| `frontend/components/admin-users-table.tsx` | edit dialog + row action |
| `frontend/components/admin-tenants-table.tsx` | edit-name + suspend/activate |
| `frontend/components/admin-proxies.tsx` | rowActions: edit + delete |
| `frontend/components/admin-devices.tsx` | row actions: edit + delete |

No engine-package files change.
