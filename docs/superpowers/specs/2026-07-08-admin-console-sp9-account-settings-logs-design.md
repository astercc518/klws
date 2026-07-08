# SP9 — 管理后台:账户自助(退出/改密)+ 系统设置 + 系统日志

**Date:** 2026-07-08
**Status:** Approved (design)
**Scope:** Four gaps reported by the operator: logout "did nothing", no
self-service password change, no system settings page, no system logs view.
Confined to `internal/api` + `internal/console` (session store helper) +
`cmd/console` (log tee) + frontend; **no engine packages** (billing/dispatch/
store/sendgate/cluster untouched).

## Background

- Logout **exists** (header avatar dropdown → `logout()` → POST
  `/api/v1/auth/logout` → clear token → redirect `/login`). The row-action
  menus use the same Base UI `DropdownMenuItem onClick` mechanism and work in
  production, so the click plumbing is not broken. The reported failure +
  "This page couldn't load" screenshot coincide with a frontend container
  restart (deploy window) — the redirect to `/login` while the frontend was
  down renders exactly that browser error. **Hypothesis, not conclusion**: SP9
  must reproduce the flow end-to-end in a local stack before touching it.
- Password: backend has admin-resets-other-user (`POST /admin/users/:id/password`)
  and email-token reset only. There is **no authenticated change-own-password**,
  and the users table hides `role='admin'` rows (SP6), so the super admin
  cannot change their own password through any UI.
- Settings: only `/admin/settings/risk` (risk policy) exists. No platform
  settings storage of any kind.
- Logs: `/admin/audit` shows the operation audit trail (SP2). Logins are not
  recorded anywhere; runtime logs live only in `docker logs`.
- `audit_log` (migration 0008) is append-only with **nullable `actor_id`** —
  failed logins (no uid) can be recorded there; no new table needed.

## Goals

1. Logout verified working end-to-end + a persistent, discoverable entry point.
2. `POST /auth/password/change` for **all roles** + dialog in all three shells.
3. `/admin/settings` page: 平台设置 (registration toggle, default new-tenant
   pricing) + 风控策略 merged in as a tab; `platform_settings` KV storage.
4. 系统日志: login events recorded to `audit_log`; audit page becomes a
   three-tab 系统日志 page (操作审计 / 登录日志 / 运行日志); runtime ring
   buffer endpoint.

## Non-goals (deferred)

- Engine-container (`klws-wadist-1`) logs in the web UI — would require docker
  socket access from the backend; stays ops-only (`docker logs`).
- Login-failure rate limiting / lockout (Turnstile already gates bots; noted
  as future work).
- Any new settings beyond the two shipped keys — the page is a skeleton that
  future keys slot into.
- Session listing/management UI ("active sessions" page).

## Component 1 — Logout: verify + harden

**Diagnosis first (runtime repro, not speculation):** bring up a local stack
(local PG + Redis containers, `go run ./cmd/console` with `TURNSTILE_SECRET_KEY`
unset → fail-open, seeded admin, `npm run dev`), drive it with Playwright:
login → avatar menu → 退出登录 → assert landing on `/login` with token cleared.
- If a real defect appears → fix it (scope amendment to this spec if nontrivial).
- If it passes → conclude the production report was the deploy-window transient;
  record that in the summary.

**Harden regardless:** add a persistent 退出登录 button to the admin sidebar
footer (next to the existing `AdminIdentity` block, icon-only when collapsed),
reusing the same `logout()` + redirect handler. Header dropdown stays.

## Component 2 — Change own password (all roles)

**Backend** `POST /api/v1/auth/password/change` (requireAuth, any role):
`{old_password, new_password}`.
- Verify `old_password` against the stored argon2id hash (`console.VerifyPassword`);
  reject on mismatch with **HTTP 400** 「旧密码错误」 — NOT 401, because the
  frontend `request()` wrapper globally intercepts 401 (clears token +
  redirects to `/login`), which would kick the user out for a typo.
- `new_password` ≥ 6 chars (mirrors reset), must differ from old.
- Update hash (`console.HashPassword`).
- **Revoke all other sessions of this user**: new
  `SessionStore.DestroyAllForUser(ctx, uid, exceptSID)` — SCAN
  `console:session:*`, GET, match `uid`, DEL all but the current sid. Scale is
  tiny (tens of sessions). The auth middleware must stash the **sid** in the
  gin context (today only `SessionData` is stashed) so the handler can spare
  the current session.
- Audit `user.change_password` (actor = self; **never** log passwords).
- Note: an impersonation session *can* change the impersonated user's password
  — acceptable (admin can already reset any password); the audit actor is the
  impersonated uid.

**Frontend:** one shared `ChangePasswordDialog` (old + new + confirm, client
check confirm==new), wired into:
- admin header avatar dropdown (new item 修改密码 above 退出登录),
- customer header (`components/header.tsx`) and sales header — same entry.
On success: toast + stay logged in (current session survives by design).

## Component 3 — 系统设置 `/admin/settings`

**Storage** — migration `0018_platform_settings.sql`:
```sql
CREATE TABLE IF NOT EXISTS platform_settings (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
GRANT SELECT, INSERT, UPDATE ON platform_settings TO app_system;
```
Shipped keys:
- `registration_open` — bool, default **true** (absent row = open, preserves
  current behavior).
- `default_pricing` — JSON array `[{"country": "US", "unit_price": "0.05"}, …]`,
  default `[]`.

**API** (admin group):
- `GET /admin/settings/platform` → `{registration_open, default_pricing}`
  (defaults filled for absent rows).
- `PUT /admin/settings/platform` — upsert both keys in one tx; validate
  country codes (2-letter upper) and `unit_price` (numeric string ≥ 0, ≤ 4 dp);
  audit `settings.update` with the new values in details (no secrets here).

**Enforcement:**
- `handleRegister`: after Turnstile, read `registration_open`; if false →
  403 「注册已关闭」. Register page surfaces the backend message (it already
  renders API errors).
- `handleAdminCreateTenant`: after creating the tenant, apply each
  `default_pricing` row via the existing pricing repo (same path as
  `finance.pricing`), in the same request; pricing failures do not roll back
  tenant creation but are reported in the response message. No audit spam:
  one `tenant.create` entry (existing) carries a `default_pricing_applied`
  count in details.

**Frontend:** nav group 「策略中心」 → 「系统设置」 with one item 系统设置
(`/admin/settings`, Settings icon). Page = two tabs:
- **平台设置** (new `admin-platform-settings.tsx`): registration switch +
  editable rows (country, price, add/remove) for default pricing, 保存 button.
- **风控策略**: existing `admin-risk-settings.tsx` rendered as-is inside the
  tab. Old route `/admin/settings/risk` becomes a `redirect()` to
  `/admin/settings?tab=risk` (deep links keep working; `resolveRoute` prefix
  match already resolves it to the same nav item).

## Component 4 — 系统日志

**Login events → existing `audit_log`** (append-only, actor nullable):
- `auth.login` — success; actor = uid; details `{ip, ua}`.
- `auth.login_failed` — actor NULL; details `{identifier, ip, ua, reason}`
  (reason: bad-credentials / disabled). Recorded **only after Turnstile
  passes**, so bot floods don't bloat the table.
- `auth.register` — actor = new uid; details `{ip, ua}`.
- `auth.logout` — actor = uid.
No schema change.

**Audit list API filter split:** `GET /admin/audit` gains optional
`action_prefix` and `exclude_prefix` params (SQL `LIKE 'auth.%'` / `NOT LIKE`).
- 操作审计 tab → `exclude_prefix=auth.` (keeps the operations view clean).
- 登录日志 tab → `action_prefix=auth.` with purpose-built columns: 时间 /
  动作(登录成功|登录失败|注册|退出) / 账号(actor email, or the attempted
  identifier from details for failures) / IP / 结果.

**Runtime logs — in-process ring buffer:**
- `cmd/console` tees both log streams (stdlib `log` via
  `log.SetOutput(io.MultiWriter(stderr, ring))`, and the zap logger from
  `walog.Production()` via a wrapped WriteSyncer) into a fixed ~2000-line
  thread-safe ring buffer.
- **Redaction at write time**: lines matching `token=`/reset-link patterns are
  masked before entering the buffer (the password-reset log line embeds a
  live reset token).
- `GET /admin/logs/runtime?limit=` (admin) → most-recent-first lines. Read-only,
  no audit entry (it's a read), no persistence, resets on restart — stated in
  the UI ("仅当前进程,重启即清").
- Boundary stated in the UI: console backend only; engine logs stay in ops.

**Frontend:** nav group 「系统审计」 → 「系统日志」, item label 风控与审计 →
系统日志 (`/admin/audit` route unchanged). Page = three tabs: 操作审计
(existing table) / 登录日志 (new, server-mode pagination reusing the audit
list plumbing) / 运行日志 (new `admin-runtime-logs.tsx`: fetch + keyword
filter + refresh button).

## Red lines & conventions

- Engine packages untouched; all changes in `internal/api`, `internal/console`
  (SessionStore method), `cmd/console` (log tee), `migrations/0018`, frontend.
- Money untouched; pricing writes reuse the existing pricing repo path.
- Audit: every new mutation audited via SP2 helpers; passwords never logged.

## Testing

- Backend testcontainers (real PG): change-password (happy / wrong-old /
  short-new / other-sessions-revoked via fake redis or miniredis-equivalent —
  if the suite lacks a redis fixture, cover DestroyAllForUser with a unit test
  against a lightweight redis container), platform settings GET/PUT + register
  gate, tenant-create default pricing application, audit action_prefix /
  exclude_prefix filters, login success/failure audit rows.
- Frontend: `npm run build` + `eslint` (house lint baseline unchanged).
- **Runtime E2E (new for this roadmap):** local stack + Playwright smoke —
  login → 修改密码 → re-login with new password → 退出登录 → assert `/login`
  + token cleared. This also settles the Component 1 diagnosis.

## Deployment note

Ships via `make deploy` (two `-f` files, per topology rules). Migration 0018
must be applied to the live DB (`psql` like 0017) before/with the deploy.
