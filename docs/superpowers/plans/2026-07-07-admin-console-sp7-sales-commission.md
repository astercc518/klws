# Admin Console SP7 — Sales Commission

**Goal:** Track and display each sales rep's monthly commission = Σ(owned tenants' monthly 消耗 × effective rate), with rate configurable per sales rep (default) and per tenant (override).

**Rules (from user):** ①commission on 消耗 (settle spend, not topup); ②rate configurable per sales AND per tenant, **tenant rate wins, else sales default, else 0**; ③admin views each sales rep's current-month owed-commission summary, **auto-resets each calendar month** (computed over the current CN month — no payout/settled state).

## Global Constraints
- Money from `wallet_ledger`; 消耗 = `-SUM(delta_balance+delta_frozen) FILTER (WHERE kind='settle')`, positive magnitude, BIGINT cents.
- Effective rate for a tenant = `COALESCE(tenants.commission_rate, owner.commission_rate, 0)` where owner = the `console_users` row with `id = tenants.sales_owner_id`.
- Rate stored as a fraction NUMERIC(5,4) (0.1000 = 10%). UI edits as a percentage (10) and converts ÷100 / ×100 at the boundary.
- Commission (cents) = `round(consumption_cents * effective_rate)`.
- Month param `YYYY-MM`, Asia/Shanghai calendar-month bounds `[month_start, next_month)`. Default = current CN month.
- Handlers on `s.systemPool()`. Reads have no audit; the two rate-update PUTs audit (extend existing `tenant.update`/`user.update`).
- Migration is idempotent (`ADD COLUMN IF NOT EXISTS`); apply to live DB via psql at deploy (no auto-runner exists).
- No engine packages.

---

## Task 1: Migration + commission summary endpoint
**Files:** `migrations/0017_commission.sql` (new), `internal/api/finance_stats.go` (handler+types), `internal/api/finance_query.go` (month parse), `internal/api/router.go` (route), `internal/api/finance_db_test.go` (DB test), `internal/api/finance_query_test.go` (unit test).

- Migration `0017_commission.sql`:
```sql
ALTER TABLE console_users ADD COLUMN IF NOT EXISTS commission_rate NUMERIC(5,4);
ALTER TABLE tenants       ADD COLUMN IF NOT EXISTS commission_rate NUMERIC(5,4);
```
- `parseMonth(s string) (from, to time.Time, err error)` in finance_query.go: parse `YYYY-MM` as CN month → `[first-of-month 00:00 CN, first-of-next-month 00:00 CN)`; empty → current CN month; invalid → error. Unit test `TestParseMonth` (explicit month + rollover to next-month exclusive bound + empty default + bad format error). Reuse `cnLoc`.
- `GET /admin/commissions?month=YYYY-MM` → `handleAdminCommissions` → `ok(c, {month, rows, totals})`:
  - `rows`: one per sales rep (role='sales') who owns ≥1 tenant: `{sales_id, sales_email, sales_rate *float64 (the rep's own default rate, null if unset), tenant_count, consumption int64, commission int64}`.
  - Query: join tenants (grouped by sales_owner) to per-tenant monthly settle, apply `COALESCE(t.commission_rate, u.commission_rate, 0)` per tenant, sum `round(settle * rate)` and settle per sales rep. One SQL statement using a subquery/CTE:
    ```sql
    SELECT u.id, u.email, u.commission_rate,
           count(DISTINCT t.id),
           COALESCE(SUM(m.consumption),0)::bigint,
           COALESCE(SUM(round(m.consumption * COALESCE(t.commission_rate, u.commission_rate, 0))),0)::bigint
      FROM console_users u
      JOIN tenants t ON t.sales_owner_id = u.id
      LEFT JOIN (
        SELECT tenant_id, -SUM(delta_balance+delta_frozen) AS consumption
          FROM wallet_ledger
         WHERE kind='settle' AND created_at >= $1 AND created_at < $2
         GROUP BY tenant_id
      ) m ON m.tenant_id = t.id
     WHERE u.role='sales'
     GROUP BY u.id, u.email, u.commission_rate
     ORDER BY 6 DESC
    ```
    (consumption is 0 when a tenant had no settle that month — LEFT JOIN + COALESCE.)
  - `totals`: `{consumption, commission}` = sum over rows.
- DB test `TestHandleAdminCommissions`: seed a sales user (rate 0.10), 2 tenants owned (one with tenant rate 0.20 override, one without), settle ledger rows in-month for each tenant; assert per-sales consumption and commission use tenant-override where set and sales default otherwise; assert an out-of-month settle is excluded.

## Task 2: Rate-config endpoints (extend user + tenant update)
**Files:** `internal/api/admin_api.go` (extend `handleAdminUpdateUser`, `handleAdminUpdateTenant`, and add `commission_rate` to the users + tenants LIST responses), `internal/api/finance_db_test.go` (DB test).

- `handleAdminUpdateTenant` req: add `CommissionRate *float64 json:"commission_rate"` (optional). When present (incl. explicit null to clear), `UPDATE tenants SET commission_rate=$..`. Validate 0 ≤ rate ≤ 1 (reject >1 with 400). Keep name update. Audit details include rate when changed.
- `handleAdminUpdateUser` req: add `CommissionRate *float64 json:"commission_rate"`. When present, update `console_users.commission_rate` (only meaningful for sales but store regardless). Validate 0 ≤ rate ≤ 1.
- Add `commission_rate` (nullable float) to the `adminTenantRow` and `adminUserRow` structs + their list SELECTs so the UI can show current values.
- DB test: PUT a tenant rate 0.2 and a user rate 0.1, assert persisted; PUT rate 1.5 → 400.

## Task 3: Commission page (frontend)
**Files:** `frontend/components/admin-commissions.tsx` (new), `frontend/app/admin/commissions/page.tsx` (new), `frontend/components/admin/nav.ts` (nav item).
- `AdminCommissions`: month picker (default current month; `<input type="month">` or prev/next buttons producing `YYYY-MM`), summary StatCards (当月总消耗 / 当月应得佣金总额 / 销售人数), table columns: 销售(sales_email), 比例(sales_rate as % or "按租户"), 名下租户(tenant_count), 当月消耗(usd), 应得佣金(usd). Consuming `GET /admin/commissions?month=`. usd(cents)=÷100. Read-only.
- Page shell mirrors siblings; title "销售佣金", eyebrow "Commission".
- nav.ts: add `{ href:"/admin/commissions", label:"销售佣金", en:"Commission", icon: <lucide e.g. Percent or Coins> }` — new group "佣金" or append to "策略中心". Put it in a new group "分成结算" above 策略中心 (or simplest: add to IAM 与账单 group after 账单统计). Choose IAM 与账单 group (commission is a billing concept), after 账单统计.

## Task 4: Rate-config UI (edit dialogs)
**Files:** `frontend/components/admin-users-table.tsx` (EditUserDialog), `frontend/components/admin-tenants-table.tsx` (EditTenantDialog), and the row interfaces to carry `commission_rate`.
- User edit dialog: when role==='sales', show a "佣金比例(%)" number input, prefilled from `commission_rate*100`. On save send `commission_rate` = input/100 (or null if blank). Only send for sales (or always send; backend stores regardless).
- Tenant edit dialog: add "佣金比例(%)" number input (prefill `commission_rate*100`, blank = inherit sales default). Send `commission_rate` = input/100 or null. Keep name field.
- Add `commission_rate: number|null` to the User + Tenant row interfaces.

## Task 5: Verify + deploy
- `go build ./... && go test ./internal/api/` pass. Frontend `npm run build && npm run lint`.
- Merge to main. Apply migration to live DB: `docker exec -i klws-postgres-1 psql -U app -d wadist < migrations/0017_commission.sql` (idempotent). Then `make deploy`. Confirm `/api/v1/admin/commissions` → 401 and `/admin/commissions` → 200.
