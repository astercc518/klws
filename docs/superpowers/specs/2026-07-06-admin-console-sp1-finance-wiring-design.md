# SP1 — 管理后台:资金与接线(Finance & Wiring)

**Date:** 2026-07-06
**Status:** Approved (design)
**Scope:** First sub-project of the "完善管理后台功能模块" effort. Surfaces
existing-but-unreachable backend capability and adds light dashboard
interactivity. Pure frontend + reuse of already-registered API routes — no
engine packages touched, no new backend endpoints.

## Background

A full-tree audit of the seven admin modules found the backend is more complete
than the UI: three finished, tested handlers have **no frontend consumer**, and
the God-View dashboard is a static read-only snapshot. SP1 closes exactly those
gaps. Heavier work (audit center, CRUD + server-side pagination) is deferred to
SP2 / SP3.

## Goals (in scope)

1. **财务流水 Ledger 页** — consume `GET /admin/finance/ledger`.
2. **大盘卡片下钻 + 自动刷新** — make `StatCard` linkable; poll stats.
3. **租户表:销售归属列 + 指派** — consume `POST /admin/sales/:id/assign`.
4. **新建租户** — consume `POST /admin/tenants`, with orphan tenants made
   visible in the tenants table (decision **A**).

## Non-goals (explicitly deferred)

- Top-N tenant/device ranking tables and real historical trend lines (both need
  new backend aggregation / time-series storage).
- Refund approve/deny actions (refunds render **read-only** here).
- Ledger server-side pagination, date-range filters, CSV export (backend is a
  fixed `LIMIT 200 / 100`).
- Any edit/delete or server-side pagination for lists (that is SP3).
- Writing audit-log rows for these actions (that is SP2).

## Existing contracts (verified, unchanged)

| Endpoint | Request | Response | File |
|---|---|---|---|
| `GET /admin/finance/ledger` | — | `{ ledger: LedgerRow[], refunds: RefundRow[] }` | admin_api.go:308 |
| `GET /admin/tenants` | — | `AdminTenant[]` (`id,name,status,sales_owner_id,balance,frozen`) | admin_api.go:100 |
| `GET /admin/users` | — | `AdminUser[]` (`id,email,role,tenant_id,disabled`) | admin_api.go:153 |
| `POST /admin/tenants` | `{ name }` | `{ id, name, status }` | admin_api.go:126 |
| `POST /admin/sales/:id/assign` | `{ sales_user_id }` (`:id` = tenant id) | `{ tenant_id, sales_user_id }` | admin_api.go:214 |

`LedgerRow = { id, tenant_id, kind, delta_balance, delta_frozen, balance_after,
created_at }` (all money in integer cents). `RefundRow = { id, tenant_id,
amount, reason, state }`.

All four handlers already have passing Go tests; SP1 adds no backend code.

## Component 1 — Ledger page

**New route** `frontend/app/admin/ledger/page.tsx` (mirrors the other admin
pages: `PageHeader` + a single client component).
**New component** `frontend/components/admin-ledger.tsx`.
**Nav:** add one item to the "IAM 与账单" group in
`frontend/components/admin/nav.ts`:
`{ href: "/admin/ledger", label: "财务流水", en: "Ledger", icon: ReceiptText }`.

**Data flow:** on mount, `Promise.all([ GET /admin/finance/ledger, GET
/admin/tenants ])`. Build a `Map<tenant_id, name>` from tenants. Same
load/error pattern as `admin-tenants-table.tsx` (401 swallowed, other errors →
error card via `ProDataTable error=`).

**Render:** two stacked `ProDataTable`s inside the page, each with its own
search box (ProDataTable already provides client search + 10-row pagination):

- **钱包流水 (wallet_ledger):** columns — 类型 (`kind` as a `Badge`), 租户
  (name via map, fallback `#<id>`), 余额变动 (`usd(delta_balance)`, green when
  ≥0 / red when <0), 冻结变动 (`usd(delta_frozen)`, muted), 变动后余额
  (`usd(balance_after)`), 时间 (`created_at`). `getRowKey = "l"+id`.
- **退款申请 (refund_requests):** columns — 租户, 金额 (`usd(amount)`), 原因
  (`reason`), 状态 (`state` as a `StatusBadge`/`Badge`). Read-only. `getRowKey
  = "r"+id`.

**Honesty affordance:** a one-line caption above each table — e.g. "仅显示最近
200 条" / "仅显示最近 100 条退款" — because the backend truncates. No summary
cards (a 200-row window would make any total misleading).

**Boundaries:** the page owns fetch + shape mapping; ProDataTable owns
search/pagination; `usd()` is copied from the existing helper (or lifted to a
shared util if trivial). No new backend, no new endpoint.

## Component 2 — Dashboard drill-down + auto-refresh

**`StatCard` (stat-card.tsx):** add an optional `href?: string` to
`StatCardProps`. When set, wrap the existing tile in a Next `Link` and show a
small `ArrowUpRight` in the top-right on hover (only when no `trend` chip
occupies that corner; if both exist, the trend chip wins and the arrow is
omitted). Backward compatible — every current call site omits `href`.

**`AdminMetrics` (admin-metrics.tsx):**
- Add `href` to the four cards: 收入→`/admin/ledger`, WA账号→`/admin/devices`,
  队列→`/admin/audit`, 封号率→`/admin/devices`.
- **Auto-refresh:** `setInterval` re-fetches `/admin/stats` every **15s**.
  - Skeleton shows **only** on the first load (`stats === null`).
  - On refresh, keep the previous `stats` on screen; a transient error during
    refresh does **not** clear the values or flip to the error card (only a
    first-load failure shows the error card).
  - Clear the interval on unmount; guard against setState-after-unmount with the
    existing `alive` flag pattern.

**Boundaries:** no backend change; `trend.direction` stays `"flat"` (real trends
are a non-goal). Drill-down targets are all existing routes.

## Component 3 — Tenants table: sales-owner column + assign

All changes in `admin-tenants-table.tsx`.

- **New column「销售归属」:** resolve `tenant.sales_owner_id` → sales user's
  email via a `Map<user_id, email>` built from the already-loaded `users`
  (filtered to `role === "sales"`). Null / unresolved → "未指派" (muted).
- **Row action「指派销售 Assign」:** added to the existing customer-row dropdown
  (alongside 充值 / 设单价). Opens an `AssignSalesDialog`.
- **`AssignSalesDialog`:** a `<select>`/dropdown of all sales users (from loaded
  `users`), preselecting the current owner if any. Submit →
  `POST /admin/sales/${target.tenant_id}/assign { sales_user_id }` → toast →
  `onDone()` reload. Mirrors the existing `TopupDialog` structure (busy state,
  disabled-until-valid, `ApiError` toast). Disabled when there are zero sales
  users (with a hint to create one first).

## Component 4 — Create tenant (decision A: orphan tenants visible)

All changes in `admin-tenants-table.tsx`.

- **Toolbar button「新建租户」** (next to the role filter) opens a
  `CreateTenantDialog`: single `name` input → `POST /admin/tenants { name }` →
  toast → `onDone()` reload.
- **Row-source change (decision A):** the table currently derives rows from
  `users` only, so a tenant with no user is invisible. Rebuild `rows` as the
  **union** of:
  1. every user row (as today, joined to its tenant), plus
  2. one **placeholder row per tenant that has zero users**, rendered as
     「(未开通账号)」 in the 账号 column with a muted style.
- **Row model:** extend `Row` with `placeholder?: boolean`. Placeholder rows
  carry `email: ""`, `role: "customer"`, `tenant_id`, and the `tenant`. Their
  row actions offer 充值 / 设单价 / 指派销售 (all keyed on `tenant_id`, which
  they have) but the account-specific column shows the placeholder label.
- **`getRowKey`:** user rows → `"u"+id`; placeholder rows → `"t"+tenant_id`
  (guarantees uniqueness across both sources).
- **Targeted fix (code we're already touching):** the summary cards currently
  sum `tenant.balance/frozen` **per user row**, double-counting any tenant with
  multiple users. Recompute 总可用余额 / 总冻结 over **unique tenants** (dedupe
  by tenant id) so adding placeholder rows doesn't compound the error and the
  totals become correct. Customer/sales counts remain per-user.

## Error handling

- All fetches follow the established pattern: 401 → swallowed (global
  interceptor redirects); other errors → `ApiError.message` into the page/table
  error state.
- All mutations (create tenant, assign sales) → success/`ApiError` toast via
  `sonner`, disabled-until-valid submit, `busy` guard against double-submit.

## Testing & verification

- **Backend:** unchanged; existing Go handler tests remain the coverage.
- **Frontend:** repo has no JS test runner. Verify via:
  - `next build` (type-check + compile clean),
  - lint (`next lint` if configured),
  - manual smoke against a running API: Ledger page renders both tables +
    truncation captions; dashboard cards navigate on click and values refresh
    without a skeleton flash; create-tenant produces a visible placeholder row;
    assign-sales updates the 销售归属 column; summary totals no longer
    double-count a multi-user tenant.
- If a JS test harness is introduced later, add component tests for the row-union
  and summary-dedupe logic (the only non-trivial pure logic in SP1).

## File-change summary

| File | Change |
|---|---|
| `frontend/app/admin/ledger/page.tsx` | **new** — page shell |
| `frontend/components/admin-ledger.tsx` | **new** — ledger + refunds tables |
| `frontend/components/admin/nav.ts` | add Ledger nav item |
| `frontend/components/admin/stat-card.tsx` | optional `href` → linkable tile |
| `frontend/components/admin-metrics.tsx` | card hrefs + 15s auto-refresh |
| `frontend/components/admin-tenants-table.tsx` | sales-owner column, assign dialog, create-tenant dialog, orphan-tenant union rows, summary dedupe |

No backend files change in SP1.
