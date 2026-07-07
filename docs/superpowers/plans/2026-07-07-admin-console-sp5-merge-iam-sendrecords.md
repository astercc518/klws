# Admin Console SP5 Implementation Plan

**Goal:** Merge tenants management into the users page (tabs), and add a platform-wide send-records page backed by a new cross-tenant recipients endpoint.

**Architecture:** One new read-only backend endpoint following the SP4 `buildXxxWhere` + `{rows,total,stats}` pattern; two frontend changes (combine users+tenants under tabs; new send-records page reusing `ProDataTable` server mode). No engine packages, no new migrations, no audit (reads only).

## Global Constraints
- Handlers use `s.systemPool()` seam. Reads only — no audit writes.
- Response envelope `{rows,total,stats}` like SP4 lists. `stats` = global unfiltered state counts.
- Send-records data = `campaign_recipients` (columns: id, campaign_id, tenant_id, phone, state, last_error, updated_at, delivered_at, read_at). state enum: pending/sent/failed/skipped; delivered/read derive from `delivered_at`/`read_at` timestamps.
- No engine packages (billing/dispatch/store/sendgate/cluster). No new migrations.
- Frontend gate: `npm run build` + no new lint category (baseline house-style only).

---

## Task 1: Backend — cross-tenant recipients endpoint

**Files:** `internal/api/finance_query.go` (add builder), `internal/api/finance_query_test.go` (unit test), `internal/api/admin_api.go` (handler + parse), `internal/api/router.go` (route), `internal/api/finance_db_test.go` (DB test).

**Interfaces:**
- `type recipientFilter struct { TenantID, CampaignID int64; State, Q string; Limit, Offset int }`
- `func buildRecipientWhere(f recipientFilter) (string, []any)` for `campaign_recipients r`: `r.tenant_id = $n` (TenantID!=0), `r.campaign_id = $n` (CampaignID!=0), `r.state::text = $n` (State!=""), `r.phone ILIKE $n` with `%q%` (Q!="").
- `GET /admin/recipients?limit&offset&tenant_id&campaign_id&state&q` → `ok(c, {rows,total,stats})`.
  - `rows`: `[]adminRecipientRow` = `{id, campaign_id, tenant_id, tenant_name *string, phone, state, last_error *string, updated_at, delivered_at *string, read_at *string}` — SELECT joins `tenants t ON t.id=r.tenant_id`, `ORDER BY r.id DESC LIMIT/OFFSET`, `clampPage(f.Limit, 20, 200)`.
  - `total`: `count(*)` with same WHERE.
  - `stats`: GLOBAL unfiltered `{total, sent, delivered, read, failed, pending, skipped}` via `count(*) FILTER (WHERE ...)` over all of `campaign_recipients` (sent=state='sent', delivered=delivered_at IS NOT NULL, read=read_at IS NOT NULL, failed=state='failed', pending=state='pending', skipped=state='skipped').

Follow the exact shape of SP4's `handleAdminListCampaigns` (finance_db_test.go has `seedCampaign`; add `seedRecipient(t,ctx,s,campaignID,tenantID,phone,state)` inserting into campaign_recipients with required NOT NULL cols: campaign_id, tenant_id, phone, country_code). DB test: seed 2 tenants × campaigns × recipients in mixed states, filter `state=sent`, assert filtered `total` and global `stats.sent`.

TDD: unit test `buildRecipientWhere` first, then DB test. Commit.

## Task 2: Frontend — merge tenants into users page (tabs)

**Files:** `frontend/app/admin/users/page.tsx` (add tabs), `frontend/components/admin/nav.ts` (remove 租户与财务 item).

- Users page renders a two-tab shell (same segmented-button style as SP4 kind/state tabs): 用户 → `<AdminUsersTable/>`, 租户 → `<AdminTenantsTable/>`. PageHeader title "用户与租户", description "全站账号(管理员/销售/客户)与租户财务集中管理。". Keep both components unchanged.
- Because tabs need client state, extract a small `"use client"` wrapper component `components/admin-iam-tabs.tsx` (page.tsx stays a server component importing it), OR make the page a client component. Prefer a `admin-iam-tabs.tsx` client component with the two tabs; page.tsx renders `<AdminIamTabs/>` under the PageHeader.
- nav.ts: remove the `{ href: "/admin/tenants", label: "租户与财务", ... }` item from "IAM 与账单" (leaving 用户管理, 财务流水, 账单统计). Leave the `/admin/tenants` route file in place (still URL-reachable, just unlinked). `Building2` import becomes unused → remove it from the import.
- Gate: build + lint (no new category).

## Task 3: Frontend — send-records page

**Files:** `frontend/components/admin-send-records.tsx` (new), `frontend/app/admin/send-records/page.tsx` (new), `frontend/components/admin/nav.ts` (add nav item).

- `AdminSendRecords`: mirror SP4 `admin-campaigns.tsx` server-mode structure. Summary `StatCard`s (已发送/已送达/已读/失败 from `stats`), state filter tabs (全部/已发送 sent/已送达.../失败 failed/待发 pending), phone search box (server `q`), server pagination. Columns: 手机号(phone), 租户(tenant_name ?? #id), 任务(#campaign_id), 状态(state Badge), 失败原因(last_error ?? —), 更新时间(updated_at). Empty state note that records populate once sends run (this instance has 0).
- Page shell mirrors siblings; PageHeader title "发送记录", eyebrow "Send Center".
- nav.ts: add `{ href: "/admin/send-records", label: "发送记录", en: "Send Records", icon: <a lucide icon e.g. ListChecks or ScrollText> }` to the "发送中心" group after 发送任务.
- Gate: build + lint.

## Task 4: Verify + deploy
- `go build ./... && go test ./internal/api/` full pass.
- `cd frontend && npm run build && npm run lint` (lint baseline unchanged except new house-pattern lines).
- Merge to main, then `make deploy` (backend+frontend), confirm `/api/v1/admin/recipients` → 401 and `/admin/send-records` → 200 via the script's verify.
