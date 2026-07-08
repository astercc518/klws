# Admin Console SP8 — User-centric hub + impersonation

**Goal:** Fold all tenant/finance + commission management into 用户管理 (customer rows get topup/ledger/pricing/assign-sales/suspend/balance; sales rows get commission display + rate; both get admin "quick login as"), and remove the standalone 租户与财务 + 销售佣金 pages.

**Decisions (from user):** remove 租户与财务 page; admin recharges customer directly (customer row); commission not a separate page — shown on sales rows in 用户管理, rate set by admin there; admin can quick-login-as sales+customer (new tab, admin stays logged in, audited); quick-view customer ledger. Relocate to customer rows: 指派销售归属, 发信定价, 挂起/恢复, 余额显示.

## Global Constraints
- A customer maps 1:1 to a tenant (`console_users.tenant_id`). Reuse existing tenant endpoints (topup/pricing/assign/status/ledger) operating on the customer's `tenant_id` — NO new backend for those.
- Impersonation: admin-only, target role MUST be sales or customer (reject admin → 400), audited (`user.impersonate`). Token minted exactly like login (`Sessions.Create` + `SignCookie`).
- **Impersonation uses sessionStorage (tab-local), NOT localStorage**, so the admin's own session (localStorage) is untouched. `lib/api.ts` token store reads sessionStorage first, then localStorage.
- Money BIGINT cents; rate fraction; usd=÷100. No engine packages. No new migrations.
- Frontend gate: build + no new lint category.

---

## Task 1: Impersonation endpoint (backend)
**Files:** `internal/api/admin_api.go` (handler), `internal/api/router.go` (route), `internal/api/*_test.go` (DB test).
- `POST /admin/users/:id/impersonate` → `handleAdminImpersonate`:
  - Parse id. Look up target `console_users` (id, role, tenant_id, disabled) via systemPool. Not found → 404. Disabled → 400 "账号已禁用".
  - If target role == admin → `fail(400, "不能模拟管理员账号")`.
  - Mint: `sid, _ := s.deps.Sessions.Create(ctx, console.SessionData{UserID: target.ID, Role: target.Role, TenantID: target.TenantID})`; `token := console.SignCookie(s.deps.SessionKey, sid)`.
  - Audit: `s.recordAudit(ctx, auditEvent{ActorID: actorID(c), Action: "user.impersonate", ResourceType: "console_user", ResourceID: id, Details: gin.H{"role": target.Role}})`.
  - `ok(c, gin.H{"token": token, "role": target.Role, "tenant_id": target.TenantID})`.
  - Route inside admin group: `admin.POST("/users/:id/impersonate", s.handleAdminImpersonate)`.
- DB test `TestHandleAdminImpersonate`: seed a customer (with tenant) → POST impersonate → 200, token non-empty, audit row `user.impersonate` written. Seed an admin → impersonate → 400. Missing id → 404.

## Task 2: Token store + impersonate landing (frontend)
**Files:** `frontend/lib/api.ts`, `frontend/app/impersonate/page.tsx` (new).
- `lib/api.ts`: change `getToken` to `sessionStorage.getItem(TOKEN_KEY) ?? localStorage.getItem(TOKEN_KEY)`. Add an `IMPERSONATE_KEY`? Simpler: keep one key; getToken prefers sessionStorage. `setToken`/`clearToken` stay on localStorage (normal login). Add `setImpersonationToken(t)` = `sessionStorage.setItem(TOKEN_KEY, t)` and export it. Add `impersonate(userId): Promise<{token,role,tenant_id}>` = `api.post('/admin/users/'+userId+'/impersonate')`.
  - Note the 401 handler currently `clearToken()` (localStorage) + redirect. For an impersonation tab, on 401 it should clear sessionStorage too. Update `clearToken` to remove from BOTH stores (safe), and the 401 path calls it. Keep login writing localStorage.
- New client page `app/impersonate/page.tsx`: on mount, read `token` + `role` from `window.location.hash` (format `#token=..&role=..`), call `setImpersonationToken(token)`, then `router.replace(role==='sales' ? '/sales' : '/dashboard')`. Show a minimal "正在进入..." placeholder. (Using the hash keeps the token out of server logs / referer better than a query.)

## Task 3: Customer ledger sheet (frontend)
**Files:** `frontend/components/customer-ledger-sheet.tsx` (new).
- A `Sheet` (side panel, reuse `@/components/ui/sheet`) that, given a `tenantId` + customer label, fetches `GET /admin/finance/ledger?tenant_id=${tenantId}&limit=50` and lists the ledger rows (kind badge, 余额变动 Money, 时间). Props `{ tenantId: number|null; label: string; onClose: () => void }`, open when tenantId != null. Mirror the compact table style of `admin-ledger.tsx`'s ledger columns. Empty state "该客户暂无流水".

## Task 4: Users-table hub refactor (frontend) — the big one
**Files:** `frontend/components/admin-users-table.tsx`, and MOVE the topup/pricing/assign/suspend dialogs in from `frontend/components/admin-tenants-table.tsx` (copy those dialog components + their logic; they POST the same endpoints keyed by tenant_id).
- Data: in `load()`, also fetch `/admin/tenants` (map by tenant id → {balance, frozen, status, sales_owner_id, commission_rate}) and `/admin/commissions` (map by sales_id → {commission, tenant_count}). Keep fetching `/admin/users` + the tenants list (already fetched for names). Also fetch sales users list for the assign dialog (derive from users where role='sales').
- Columns (role-aware, admins still hidden per SP6):
  - 账号 (email), 角色 (badge).
  - 租户/佣金 column: customer → tenant name; sales → `名下 N 租户 · 当月佣金 usd(commission)`.
  - 余额/比例 column: customer → `usd(balance)` (from tenant map; "—" if none); sales → 佣金比例 `rate*100%` or "未设".
  - 状态 (启用/禁用; for customer also show 挂起 if tenant.status==='suspended').
- Row actions dropdown (role-aware):
  - customer: 充值 Top-up, 查看流水 (opens CustomerLedgerSheet with tenant_id), 发信定价, 指派销售, 挂起/恢复, 快捷登录, 编辑, 禁用/启用, 重置密码.
  - sales: 快捷登录, 编辑 (incl. 佣金比例), 禁用/启用, 重置密码.
- 快捷登录 action: `const r = await impersonate(u.id); window.open('/impersonate#token='+encodeURIComponent(r.token)+'&role='+r.role, '_blank')`. Toast on error.
- Keep the existing role tabs (全部/销售/客户), sales owned-count, CreateUserDialog, EditUserDialog (with commission rate for sales), ResetPasswordDialog.
- Wire the moved dialogs (Topup/Pricing/AssignSales/Suspend) to operate on `u.tenant_id`. Suspend uses `POST /admin/tenants/${tenant_id}/status`.
- This file grows large; that's acceptable (it's now the console hub). If it exceeds ~700 lines, split the dialog components into `admin-user-dialogs.tsx` and import them.

## Task 5: Remove standalone pages + OKCC-style nav reorg
**Files:** `frontend/components/admin/nav.ts`, delete `frontend/app/admin/tenants/page.tsx`, `frontend/app/admin/commissions/page.tsx`, `frontend/components/admin-commissions.tsx`, `frontend/components/admin-tenants-table.tsx` (dialogs moved to T4).
- `git rm` the four files. Verify no dangling imports.
- **Restructure `NAV_GROUPS` to mirror OKCC's layout** (klws-mapped; no telephony terms). Rename 用户管理 → 客户 (it is now the customer/user hub). Target groups + items:
  - **概览**: 运行信息 `/admin` (label 运行信息, en Overview) — rename label from 全局大盘.
  - **客户与财务**: 客户 `/admin/users` (label 客户, en Customers, icon Users), 财务流水 `/admin/ledger`, 账单统计 `/admin/billing`.
  - **资源**: 代理网络池 `/admin/resources`, 节点与设备 `/admin/devices`.
  - **发送中心**: 发送任务 `/admin/campaigns`, 发送记录 `/admin/send-records`.
  - **策略中心**: 风控策略 `/admin/settings/risk`.
  - **系统审计**: 风控与审计 `/admin/audit` (voice/CID review folded here per user — no new module).
  - **配置流程**: 配置向导 `/admin/setup` (label 配置流程, en Setup, icon ListChecks) — added by T6.
- Keep every referenced page real; adjust icon imports (remove Coins/Building2 if now unused; keep Users).

## Task 6: 6-step config wizard page
**Files:** `frontend/components/admin-setup-wizard.tsx` (new), `frontend/app/admin/setup/page.tsx` (new). (Nav item added in T5.)
- A guided landing page: 6 numbered step cards mirroring OKCC's 配置流程, mapped to klws. Each card = number badge + title + one-line description + a primary button linking to the page/action that fulfills it (this is a UX orchestration layer over existing features — NO new backend):
  1. **资费套餐** — 设置发信单价/套餐 → links to 客户 page (pricing is per-customer via the 发信定价 action) with a note; button "去设置定价" → `/admin/users`.
  2. **企业客户** — 新建客户 → button "新建客户" → `/admin/users` (the CreateUser flow lives there).
  3. **账户充值** — 给客户充值 → button "去充值" → `/admin/users`.
  4. **配置账号/设备** (was 配置分机) — 接入 WhatsApp 账号/设备 → button → `/admin/devices`.
  5. **配置代理** (was 配置中继) — 配置出口代理 → button → `/admin/resources`.
  6. **配置发送路由** (was 配置路由) — 创建发送任务/编排 → button → `/admin/campaigns`.
  - Layout: a vertical numbered stepper (or responsive card grid) using the design tokens already in the console (Card, StatCard-like styling, brand accents). Each step card shows step N/6. Read-only guidance; clicking a button navigates (Next.js `<Link>`). Add a short intro explaining this is the setup flow.
  - Page shell mirrors siblings; PageHeader eyebrow "Setup", title "配置流程", description "从资费套餐到发送路由的开通引导，六步完成一个客户的接入。".

## Task 7: Verify + deploy
- `go build ./... && go test ./internal/api/` pass. Frontend `npm run build && npm run lint`.
- Merge to main, `make deploy`. Confirm `/api/v1/admin/users/1/impersonate` → 401, `/admin/users` → 200, `/admin/setup` → 200; removed routes (`/admin/tenants`, `/admin/commissions`) → 404. Live-smoke impersonation manually.
