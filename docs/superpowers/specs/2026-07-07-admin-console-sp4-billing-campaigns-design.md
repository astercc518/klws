# SP4 — 管理后台:发送任务页 / 流水筛选导出 / 用户角色视图 / 账单统计

**Date:** 2026-07-07
**Status:** Approved (design)
**Scope:** Four gaps the operator called out after SP1–SP3b: ① a dedicated
send-campaigns page (promoted out of the audit tab), ② ledger kind
filtering + server pagination + CSV export, ③ role-grouped user management,
④ billing statistics (platform dashboard + per-tenant bill, both exportable).
Confined to `internal/api` + `cmd/console` + `frontend/`; read-only queries +
audit writes only — no engine packages ([[klws-redlines]] respected; SP4 adds
no writes to billing/dispatch/store/sendgate/cluster).

## Background

After SP1–SP3b the console's CRUD surface is complete, but:

- The admin campaigns monitor lives as the second tab of `/admin/audit` —
  operators do not find it, and `handleAdminListCampaigns` is a hard
  `LIMIT 200` with no filters (same disease SP3a cured for devices/proxies).
  The recipients drill-down (`CampaignDetailSheet` in admin mode) already
  works and is reused as-is.
- `handleAdminLedger` returns the latest 200 `wallet_ledger` rows, all kinds
  mixed; topup records exist (kind enum: `hold/settle/refund/reject/topup/
  adjust`) but cannot be isolated. No export.
- The users table is flat; roles exist but there is no per-role view and a
  sales row says nothing about the tenants it owns (`tenants.sales_owner_id`).
- No billing aggregation exists anywhere — no platform totals, no per-tenant
  bill. Frontend auth is a localStorage Bearer token, so CSV download must go
  through fetch→blob, not a bare `<a href>`.

## Money semantics (single source of truth)

All amounts come from `wallet_ledger` (BIGINT minor units). `billing_charges`
is **out of scope** this round — one ledger-based set of numbers, no second
opinion to disagree with.

- **Net movement of a row** = `delta_balance + delta_frozen` (change of the
  wallet's total holdings). `hold`/`reject` net to 0 (balance↔frozen moves);
  `topup` is +, `settle` is −, `refund` is +, `adjust` is ±.
- **总充值** = Σ net where kind=`topup`; **总消耗** = −Σ net where
  kind=`settle`; **总退款** = Σ net where kind=`refund`; **调整** = Σ net
  where kind=`adjust`; **净额** = 充值 − 消耗 + 退款 + 调整.
- **Per-tenant bill window**: closing balance = `balance_after +
  frozen_after` of the last ledger row ≤ `to`; opening balance = same for
  the last row < `from` (0 if none). DB tests must verify
  opening + Σ net(in window) = closing.
- Daily bucketing uses `date_trunc('day', created_at AT TIME ZONE
  'Asia/Shanghai')` — operators are CN-based; document the timezone in the
  UI footer.

## Goals

### ① Send-campaigns page (`/admin/campaigns` nav page)

Backend — extend `GET /api/v1/admin/campaigns` the SP3a way:

- Params: `limit/offset` (clampPage), `state` (enum filter), `q` (matches
  tenant name via join, or numeric campaign/tenant id).
- Returns `{rows, total, stats}`; `stats` is unfiltered: counts by state +
  auto-tripped count (for the summary cards). Drop `LIMIT 200`.
- Pure `buildCampaignWhere` + unit tests; handler on `s.systemPool()` seam.
- Response shape changes from bare array to envelope — the only consumer is
  the component we are editing anyway.

Frontend:

- New nav group **发送中心** (between 资源大厅 and 策略中心) with item
  发送任务 `/admin/campaigns` (en: Campaigns, icon: Send).
- Page = upgraded `AdminCampaigns`: ProDataTable server mode, state filter
  tabs, summary stat cards (运行中/已暂停/熔断中/总数), existing stop/resume
  dialogs and row-click → `CampaignDetailSheet` (admin mode) unchanged.
- `/admin/audit` drops the tab shell (`admin-audit-tabs.tsx` deleted) and
  renders the audit log directly.

### ② Ledger filtering + export

Backend:

- `GET /admin/finance/ledger`: add `kind/tenant_id/from/to/limit/offset`,
  envelope becomes `{rows, total, refunds}` — `rows`/`total` are the
  filtered+paginated ledger, `refunds` stays the existing capped list.
  Pure `buildLedgerWhere` shared by list + export + bill queries.
- `GET /admin/finance/ledger/export`: same WHERE, streams `text/csv`
  (`Content-Disposition: attachment`), full filtered set, no pagination.
  Columns: id, created_at, tenant_id, tenant_name, kind, delta_balance,
  delta_frozen, balance_after, frozen_after.
- Export writes audit `finance.ledger_export` (details: filters + row count).

Frontend:

- Kind tabs: 全部/充值(topup)/结算(settle)/退款(refund)/调整(adjust) —
  `hold`/`reject` rows appear under 全部 only. Server pagination + date
  range (from/to) + 导出 CSV button.
- New `api.download(path)` helper in `lib/api.ts`: fetch with Bearer →
  blob → object-URL click; reused by ④.

### ③ Role-grouped users (frontend only)

- Role tabs above the users table: 全部/管理员/销售/客户 (client-side
  filter — user counts are low; server pagination stays deferred as per
  SP3a's call).
- Sales rows get a 名下客户 column: count of tenants with
  `sales_owner_id = user.id`, client-side join on the tenants list the page
  already fetches for the tenant-name column; renders as a link to
  `/admin/tenants`.

### ④ Billing statistics (`/admin/billing`)

Backend (all read-only, on `s.systemPool()`):

- `GET /admin/finance/stats?from&to` →
  `{summary: {topup, settle, refund, adjust, net}, daily: [{day, topup,
  settle, refund}], top_tenants: [{tenant_id, name, settle, topup}]}` —
  one grouped query per block, TopN = 20 by settle.
- `GET /admin/finance/bill?tenant_id&from&to` →
  `{tenant, opening, closing, summary, daily}` per the money-semantics
  section.
- `GET /admin/finance/bill/export?tenant_id&from&to` → CSV of that
  tenant's ledger rows in the window (same columns as ②'s export) with a
  summary header row block. Writes audit `finance.bill_export`.
- `from/to` are `YYYY-MM-DD` (Asia/Shanghai day bounds); default = last
  30 days; reject reversed/oversized (>366d) ranges with 400.

Frontend `/admin/billing` (nav: IAM 与账单, after 财务流水, en: Billing):

- Range picker (近 7/30/90 天 presets + custom from/to) → summary stat
  cards (充值/消耗/退款/净额) + daily trend (lightweight inline SVG in the
  `send-trend-chart` style, topup vs settle) + tenant TopN table.
- Clicking a tenant row switches to the bill view (breadcrumb back):
  opening/closing balance, category totals, daily series, 导出 CSV.

## Non-goals (deferred)

- `billing_charges`-based message-level billing detail (per-country pricing
  breakdown) — needs its own reconciliation story vs the ledger.
- Users/tenants server pagination (still low-cardinality).
- Suspended-tenant enforcement, proxy health probes, device force-offline —
  engine-touching, explicitly parked.
- Scheduled/emailed statements; only on-demand CSV.

## Testing

- Unit: `buildCampaignWhere`/`buildLedgerWhere`/range-validation pure
  functions.
- DB (testcontainers, same harness as SP2/SP3): seeded ledger fixture →
  stats totals, daily buckets (incl. timezone edge: row at 23:30 UTC lands
  on next CN day), bill opening/closing reconciliation, campaign list
  filters/pagination/stats, CSV endpoints (status, Content-Type,
  row count, header line), export audit rows written.
- Frontend: `npm run build` + eslint (house rule — no JS test framework).
- Post-merge live-API smoke: campaigns page filters + recipients sheet,
  ledger tabs + export download, billing drill-down + export, audit entries
  visible on `/admin/audit`.

## Sequencing (one branch, four commits-ish)

② ledger backend+frontend (establishes `api.download`, WHERE-builder
pattern) → ④ stats/bill on top of it → ① campaigns page (independent) →
③ users tabs (independent, smallest). ③①can land in any order.
