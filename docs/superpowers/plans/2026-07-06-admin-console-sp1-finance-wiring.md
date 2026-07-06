# Admin Console SP1 — Finance & Wiring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface three finished-but-unreachable admin backend capabilities (finance ledger, create tenant, assign sales) in the Next.js admin UI, and make the God-View dashboard cards clickable + auto-refreshing.

**Architecture:** Pure frontend work against already-registered, already-tested API routes. Reuse existing primitives (`ProDataTable`, `StatCard`, `Dialog`, `sonner` toasts, the `api` fetch wrapper). No Go/backend files change. No engine packages touched.

**Tech Stack:** Next.js 16.2.9 (App Router, client components), React, TypeScript, Tailwind, `lucide-react` ^1.21, `@base-ui/react` dialogs/dropdowns, `sonner`.

## Global Constraints

- **Modified Next.js:** this repo runs a customized Next 16.2.9. Per `frontend/AGENTS.md`, before writing any Next-specific code (Link, page/layout conventions), consult `frontend/node_modules/next/dist/docs/`. Do NOT assume training-data Next behavior.
- **No JS test runner exists** (`package.json` scripts: `dev`, `build`, `start`, `lint` only). Per-task verification is: `npm run build` (type-check + compile clean) → `npm run lint` → the task's manual smoke check. There is no unit-test step; do not invent one or add a test framework (out of scope for SP1).
- **API envelope:** every call goes through `@/lib/api` (`api.get/post`), which unwraps `{code,data,message}` and throws `ApiError`. 401 is globally intercepted — never handle 401 in components; swallow it (`e instanceof ApiError && e.status === 401`).
- **Money is integer cents.** Always format with the `usd()` helper (`n/100`, `Intl.NumberFormat` USD). Never display raw cents.
- **No backend edits.** If a task seems to need one, stop — it belongs to SP2/SP3.
- **Commit** after each task with the exact message shown. Work is on branch `feat/admin-sp1-finance-wiring`.
- Run all `npm` commands from `/var/klwa/frontend`.

---

### Task 1: StatCard gains an optional `href` (linkable tile)

Smallest, isolated change; Task 3 depends on it. Adds drill-down capability without altering any current call site.

**Files:**
- Modify: `frontend/components/admin/stat-card.tsx`

**Interfaces:**
- Consumes: nothing new.
- Produces: `StatCardProps.href?: string`. When set, the tile is wrapped in a Next `Link` to `href` and shows an `ArrowUpRight` in the top-right corner **only when `trend` is absent** (the trend chip owns that corner otherwise).

- [ ] **Step 1: Confirm the Link API for this Next build**

Read: `frontend/node_modules/next/dist/docs/` for the `Link` usage (import path, required props). Expected: `import Link from "next/link"` with an `href` prop. Note anything that differs before coding.

- [ ] **Step 2: Add `href` to `StatCardProps`**

In `stat-card.tsx`, add to the interface (after `trend?`):

```tsx
  /** When set, the whole tile becomes a link to this route and shows a
   *  drill-down arrow (only if no trend chip occupies the top-right). */
  href?: string;
```

- [ ] **Step 3: Render the linkable variant**

Import at top: `import Link from "next/link";` and add `ArrowUpRight` to the existing `lucide-react` import (it already imports `ArrowUpRight, ArrowDownRight, Minus`).

Change the `StatCard` signature to destructure `href`, and wrap the returned tile. Replace the current `return ( <div className="rounded-2xl ...">...</div> )` with:

```tsx
export function StatCard({ label, value, sub, icon: Icon, accent = "neutral", trend, href }: StatCardProps) {
  const intent = trend ? trendIntent(trend) : "neutral";
  const TrendIcon = trend ? TREND_ICON[trend.direction] : null;

  const inner = (
    <>
      <div className="flex items-start justify-between gap-3">
        <span className={cn("flex size-10 items-center justify-center rounded-xl", TILE[accent])}>
          <Icon className="size-5" strokeWidth={1.9} />
        </span>
        {trend && TrendIcon ? (
          <span
            className={cn(
              "inline-flex items-center gap-0.5 rounded-full px-2 py-0.5 font-mono text-xs font-medium tabular-nums ring-1 ring-inset",
              intent === "positive"
                ? "text-emerald-700 ring-emerald-600/20 dark:text-emerald-400"
                : intent === "negative"
                  ? "text-rose-700 ring-rose-600/20 dark:text-rose-400"
                  : "text-muted-foreground ring-foreground/10",
            )}
          >
            <TrendIcon className="size-3" strokeWidth={2.25} />
            {trend.value}
          </span>
        ) : href ? (
          <ArrowUpRight className="size-4 text-muted-foreground/50 transition-colors group-hover:text-foreground" />
        ) : null}
      </div>

      <div className="mt-4 font-mono text-3xl font-semibold tabular-nums tracking-tight">{value}</div>
      <div className="mt-1 text-sm font-medium text-foreground/90">{label}</div>
      {sub && <div className="mt-0.5 font-mono text-xs tabular-nums text-muted-foreground">{sub}</div>}
    </>
  );

  const base = "rounded-2xl border border-border/70 bg-card p-5 shadow-sm ring-1 ring-foreground/5 transition-shadow hover:shadow-md";

  if (href) {
    return (
      <Link href={href} className={cn(base, "group block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}>
        {inner}
      </Link>
    );
  }
  return <div className={base}>{inner}</div>;
}
```

- [ ] **Step 4: Verify build + lint**

Run (from `frontend/`): `npm run build && npm run lint`
Expected: clean compile, no type errors, no lint errors. Existing call sites (which omit `href`) are unaffected.

- [ ] **Step 5: Commit**

```bash
git add frontend/components/admin/stat-card.tsx
git commit -m "feat(admin): StatCard optional href for drill-down tiles

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Ledger page + nav item

Consumes `GET /admin/finance/ledger`. Creates the `/admin/ledger` route that Task 3's dashboard card will link to, so it must land before Task 3.

**Files:**
- Create: `frontend/app/admin/ledger/page.tsx`
- Create: `frontend/components/admin-ledger.tsx`
- Modify: `frontend/components/admin/nav.ts` (add nav item)

**Interfaces:**
- Consumes: `api.get`, `ProDataTable`/`Column`, `Badge`, `StatusBadge` (if present) or `Badge`, `PageHeader`.
- Produces: route `/admin/ledger`; component `AdminLedger`.
- API shapes (verbatim from backend):
  - `LedgerRow = { id:number; tenant_id:number; kind:string; delta_balance:number; delta_frozen:number; balance_after:number; created_at:string }`
  - `RefundRow = { id:number; tenant_id:number; amount:number; reason:string; state:string }`
  - `GET /admin/finance/ledger` → `{ ledger: LedgerRow[]; refunds: RefundRow[] }`
  - `GET /admin/tenants` → `{ id:number; name:string; ... }[]`

- [ ] **Step 1: Add the nav item**

In `frontend/components/admin/nav.ts`, add `ReceiptText` to the `lucide-react` import, and add a second item to the "IAM 与账单" group (after the Users item):

```ts
    { href: "/admin/ledger", label: "财务流水", en: "Ledger", icon: ReceiptText },
```

(The group's `items` array becomes: Tenants, Users, Ledger.)

- [ ] **Step 2: Create the page shell**

Create `frontend/app/admin/ledger/page.tsx` (mirror `app/admin/tenants/page.tsx`):

```tsx
import { PageHeader } from "@/components/admin/page-header";
import { AdminLedger } from "@/components/admin-ledger";

export default function AdminLedgerPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Billing & IAM"
        title="财务流水"
        description="全平台钱包流水与退款申请。仅显示最近记录,用于对账与审计。"
      />
      <AdminLedger />
    </div>
  );
}
```

- [ ] **Step 3: Create the ledger component**

Create `frontend/components/admin-ledger.tsx`. Fetch both endpoints in parallel, build a tenant-name map, render two stacked `ProDataTable`s. Follow the load/error pattern from `admin-tenants-table.tsx`.

```tsx
"use client";

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";

interface LedgerRow {
  id: number;
  tenant_id: number;
  kind: string;
  delta_balance: number;
  delta_frozen: number;
  balance_after: number;
  created_at: string;
}
interface RefundRow {
  id: number;
  tenant_id: number;
  amount: number;
  reason: string;
  state: string;
}
interface Tenant {
  id: number;
  name: string;
}

const usd = (smallest: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(smallest / 100);

/** Signed money: green for credits (>=0), red for debits (<0). */
function Money({ cents }: { cents: number }) {
  const cls = cents < 0 ? "text-rose-600 dark:text-rose-400" : "text-emerald-600 dark:text-emerald-400";
  return <span className={`font-mono tabular-nums ${cls}`}>{usd(cents)}</span>;
}

const refundStateVariant: Record<string, "default" | "secondary" | "outline"> = {
  approved: "default",
  pending: "secondary",
  rejected: "outline",
};

export function AdminLedger() {
  const [ledger, setLedger] = useState<LedgerRow[] | null>(null);
  const [refunds, setRefunds] = useState<RefundRow[] | null>(null);
  const [names, setNames] = useState<Map<number, string>>(new Map());
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [fin, tenants] = await Promise.all([
        api.get<{ ledger: LedgerRow[]; refunds: RefundRow[] }>("/admin/finance/ledger"),
        api.get<Tenant[]>("/admin/tenants"),
      ]);
      setNames(new Map(tenants.map((t) => [t.id, t.name])));
      setLedger(fin.ledger);
      setRefunds(fin.refunds);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const tenantName = (id: number) => names.get(id) ?? `#${id}`;

  const ledgerCols: Column<LedgerRow>[] = [
    { key: "kind", header: "类型", cell: (r) => <Badge variant="outline">{r.kind}</Badge> },
    { key: "tenant", header: "租户", cell: (r) => <span className="text-sm text-muted-foreground">{tenantName(r.tenant_id)}</span> },
    { key: "delta_balance", header: "余额变动", align: "right", cell: (r) => <Money cents={r.delta_balance} /> },
    { key: "delta_frozen", header: "冻结变动", align: "right", cell: (r) => <span className="font-mono tabular-nums text-muted-foreground">{usd(r.delta_frozen)}</span> },
    { key: "balance_after", header: "变动后余额", align: "right", cell: (r) => <span className="font-mono tabular-nums">{usd(r.balance_after)}</span> },
    { key: "created_at", header: "时间", cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.created_at}</span> },
  ];

  const refundCols: Column<RefundRow>[] = [
    { key: "tenant", header: "租户", cell: (r) => <span className="text-sm text-muted-foreground">{tenantName(r.tenant_id)}</span> },
    { key: "amount", header: "金额", align: "right", cell: (r) => <span className="font-mono tabular-nums">{usd(r.amount)}</span> },
    { key: "reason", header: "原因", cell: (r) => <span className="text-sm">{r.reason}</span> },
    { key: "state", header: "状态", cell: (r) => <Badge variant={refundStateVariant[r.state] ?? "outline"}>{r.state}</Badge> },
  ];

  return (
    <div className="space-y-8">
      <section className="space-y-2">
        <p className="font-mono text-xs uppercase tracking-wider text-muted-foreground">钱包流水 · 仅显示最近 200 条</p>
        <ProDataTable
          data={ledger}
          error={error}
          columns={ledgerCols}
          getRowKey={(r) => `l${r.id}`}
          search={{ placeholder: "搜索类型或租户…", accessor: (r) => `${r.kind} ${tenantName(r.tenant_id)}` }}
          emptyState="暂无流水"
        />
      </section>

      <section className="space-y-2">
        <p className="font-mono text-xs uppercase tracking-wider text-muted-foreground">退款申请 · 仅显示最近 100 条</p>
        <ProDataTable
          data={refunds}
          error={error}
          columns={refundCols}
          getRowKey={(r) => `r${r.id}`}
          search={{ placeholder: "搜索租户或原因…", accessor: (r) => `${tenantName(r.tenant_id)} ${r.reason}` }}
          emptyState="暂无退款申请"
        />
      </section>
    </div>
  );
}
```

Note: if `@/components/ui/badge` does not export the `variant`s used, check the file and fall back to `<Badge>` without a variant. Verify the import path matches the one used in `admin-tenants-table.tsx` (`@/components/ui/badge`).

- [ ] **Step 4: Verify build + lint**

Run: `npm run build && npm run lint`
Expected: clean. New route `/admin/ledger` compiles; nav shows the new item.

- [ ] **Step 5: Manual smoke**

Run `npm run dev`, log in as admin, open `/admin/ledger`. Expected: two tables render with the truncation captions; on an empty DB both show their empty state (no crash); tenant column shows names (or `#id` when unknown). Sidebar shows 财务流水 under IAM 与账单.

- [ ] **Step 6: Commit**

```bash
git add frontend/app/admin/ledger/page.tsx frontend/components/admin-ledger.tsx frontend/components/admin/nav.ts
git commit -m "feat(admin): finance ledger page (wallet ledger + refunds)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Dashboard drill-down + auto-refresh

Depends on Task 1 (`StatCard.href`) and Task 2 (`/admin/ledger` route exists).

**Files:**
- Modify: `frontend/components/admin-metrics.tsx`

**Interfaces:**
- Consumes: `StatCard.href` (Task 1); routes `/admin/ledger` (Task 2), `/admin/devices`, `/admin/audit` (existing).
- Produces: nothing downstream.

- [ ] **Step 1: Add hrefs to the four cards**

In `admin-metrics.tsx`, add an `href` prop to each `StatCard`:
- 平台总消耗 / 收入 → `href="/admin/ledger"`
- 活跃 WA 账号 → `href="/admin/devices"`
- 队列积压任务 → `href="/admin/audit"`
- 风控封号率 → `href="/admin/devices"`

- [ ] **Step 2: Convert the mount-only fetch into a 15s poll**

Replace the existing `useEffect` (lines ~42-55) with a polling version that (a) shows the skeleton only on first load, (b) keeps prior values on refresh, (c) does not surface transient refresh errors:

```tsx
  useEffect(() => {
    let alive = true;

    const fetchStats = (isRefresh: boolean) => {
      api
        .get<AdminStats>("/admin/stats")
        .then((d) => {
          if (alive) setStats(d);
        })
        .catch((e) => {
          if (!alive) return;
          // Only a first-load failure surfaces; refresh errors are ignored so
          // the dashboard doesn't flicker to an error card on a transient blip.
          if (!isRefresh && !(e instanceof ApiError && e.status === 401)) {
            setError(e instanceof ApiError ? e.message : "加载失败");
          }
        });
    };

    fetchStats(false);
    const id = setInterval(() => fetchStats(true), 15_000);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, []);
```

(The skeleton branch already keys off `stats === null`, so it naturally shows only until the first successful load and never again on refresh.)

- [ ] **Step 3: Verify build + lint**

Run: `npm run build && npm run lint`
Expected: clean.

- [ ] **Step 4: Manual smoke**

`npm run dev`, open `/admin`. Expected: hovering a card shows the drill-down arrow (on cards without a trend chip) and pointer cursor; clicking navigates (收入→ledger, WA→devices, 队列→audit, 封号率→devices). Leave the page open ~20s and confirm values refresh with **no** skeleton flash. Kill the API briefly — the cards keep their last values (no error card).

- [ ] **Step 5: Commit**

```bash
git add frontend/components/admin-metrics.tsx
git commit -m "feat(admin): dashboard cards drill down + 15s auto-refresh

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Tenants table — orphan-tenant rows, summary dedupe, sales-owner column (read side)

Restructures how rows are built so a user-less tenant is visible, fixes the multi-user balance double-count, and adds the 销售归属 column. No new dialogs yet (Task 5).

**Files:**
- Modify: `frontend/components/admin-tenants-table.tsx`

**Interfaces:**
- Consumes: existing `AdminUser`, `AdminTenant`, `Row` types; loaded `users` + `tenants`.
- Produces (used by Task 5): a `Row` type extended with `placeholder?: boolean`; the `load()` builder that unions user rows + orphan-tenant rows; a `salesEmail(id)` resolver.

- [ ] **Step 1: Extend the `Row` type**

In `admin-tenants-table.tsx`, change the `Row` interface:

```tsx
/** A user row joined with its tenant wallet, OR a placeholder for a tenant that
 *  has no console user yet (placeholder=true, email empty). */
interface Row extends AdminUser {
  tenant?: AdminTenant;
  placeholder?: boolean;
}
```

- [ ] **Step 2: Build union rows + a sales-email map in `load()`**

Replace the body of `load()` so that after fetching `users` and `tenants` it (a) keeps a `tenants` list in state for the create/summary logic, (b) builds user rows as today, (c) appends one placeholder row per tenant with **no** user. Add state: `const [salesById, setSalesById] = useState<Map<number, string>>(new Map());`

```tsx
  const load = useCallback(async () => {
    try {
      const [users, tenants] = await Promise.all([
        api.get<AdminUser[]>("/admin/users"),
        api.get<AdminTenant[]>("/admin/tenants"),
      ]);
      const byTenant = new Map(tenants.map((t) => [t.id, t]));
      setSalesById(new Map(users.filter((u) => u.role === "sales").map((u) => [u.id, u.email])));
      setSalesUsers(users.filter((u) => u.role === "sales"));

      const userRows: Row[] = users.map((u) => ({
        ...u,
        tenant: u.tenant_id != null ? byTenant.get(u.tenant_id) : undefined,
      }));

      // Tenants that no user references → placeholder rows (decision A).
      const usedTenantIds = new Set(users.map((u) => u.tenant_id).filter((x): x is number => x != null));
      const orphanRows: Row[] = tenants
        .filter((t) => !usedTenantIds.has(t.id))
        .map((t) => ({
          id: -t.id, // synthetic; getRowKey uses tenant_id for placeholders
          email: "",
          role: "customer" as const,
          tenant_id: t.id,
          disabled: false,
          tenant: t,
          placeholder: true,
        }));

      setRows([...userRows, ...orphanRows]);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);
```

Add the supporting state near the top of the component (with the other `useState`s):

```tsx
  const [salesUsers, setSalesUsers] = useState<AdminUser[]>([]);
```

- [ ] **Step 3: Dedupe the summary totals by tenant**

Replace the `summary` `useMemo` so balance/frozen sum over **unique tenants**, not per user row (fixes the double-count and prevents placeholder rows from inflating it). Customer/sales counts stay per-user (placeholder rows have empty email and are `customer` role — exclude them from the customer count by checking `!r.placeholder`):

```tsx
  const summary = useMemo(() => {
    if (!rows) return null;
    let customers = 0;
    let sales = 0;
    const seenTenants = new Map<number, AdminTenant>();
    for (const r of rows) {
      if (!r.placeholder && r.role === "customer") customers++;
      if (!r.placeholder && r.role === "sales") sales++;
      if (r.tenant) seenTenants.set(r.tenant.id, r.tenant);
    }
    let balance = 0;
    let frozen = 0;
    for (const t of seenTenants.values()) {
      balance += t.balance;
      frozen += t.frozen;
    }
    return { customers, sales, balance, frozen };
  }, [rows]);
```

- [ ] **Step 4: Render placeholder rows + add the 销售归属 column**

In the `email` column `cell`, handle placeholders; add a new `sales_owner` column after `tenant`:

Update the `email` column cell:

```tsx
      cell: (r) =>
        r.placeholder ? (
          <span className="text-sm italic text-muted-foreground">(未开通账号)</span>
        ) : (
          <div className="flex items-center gap-2.5">
            <RowAvatar text={r.email} />
            <span className="font-mono text-sm">{r.email}</span>
          </div>
        ),
```

Add this column object to the `columns` array, right after the `tenant` column:

```tsx
    {
      key: "sales_owner",
      header: "销售归属",
      cell: (r) => {
        const id = r.tenant?.sales_owner_id;
        const email = id != null ? salesById.get(id) : undefined;
        return <span className="text-sm text-muted-foreground">{email ?? (r.tenant ? "未指派" : "—")}</span>;
      },
    },
```

- [ ] **Step 5: Fix `getRowKey` for the two row sources**

Update the `ProDataTable` `getRowKey` prop:

```tsx
        getRowKey={(r) => (r.placeholder ? `t${r.tenant_id}` : `u${r.id}`)}
```

Also update the `rowActions` gate so placeholder rows (which have a `tenant_id` but empty email) still expose the tenant-level actions: change `const isCustomer = r.role === "customer" && r.tenant_id != null;` — this already holds for placeholders (`role: "customer"`, `tenant_id` set), so no change needed; just confirm placeholders get the actions dropdown.

- [ ] **Step 6: Verify build + lint**

Run: `npm run build && npm run lint`
Expected: clean. `salesUsers` is set but not yet consumed — that is intentional (Task 5 uses it). If lint flags it as unused, add a temporary `void salesUsers;` OR proceed straight to Task 5 in the same session and consume it there; prefer the latter.

- [ ] **Step 7: Manual smoke**

`npm run dev`, open `/admin/tenants`. Expected: a tenant with no user appears as a "(未开通账号)" row; the 销售归属 column shows the sales user's email or "未指派"; if a tenant has 2+ customer users, 总可用余额 counts it once (compare against DB).

- [ ] **Step 8: Commit**

```bash
git add frontend/components/admin-tenants-table.tsx
git commit -m "feat(admin): tenants table shows orphan tenants + sales-owner column, dedupe wallet totals

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Tenants table — create-tenant + assign-sales dialogs (write side)

Adds the two mutation flows. Consumes `salesUsers` state from Task 4.

**Files:**
- Modify: `frontend/components/admin-tenants-table.tsx`

**Interfaces:**
- Consumes: `salesUsers` state and `load` from Task 4; `POST /admin/tenants {name}`; `POST /admin/sales/:id/assign {sales_user_id}`.
- Produces: nothing downstream.

- [ ] **Step 1: Add dialog trigger state**

Near the other `useState`s add:

```tsx
  const [createOpen, setCreateOpen] = useState(false);
  const [assignTarget, setAssignTarget] = useState<Row | null>(null);
```

- [ ] **Step 2: Add the "新建租户" toolbar button**

Wrap the existing role-filter `DropdownMenu` in the `toolbar` prop together with a new button (the toolbar slot renders right-aligned controls):

```tsx
        toolbar={
          <>
            <Button variant="outline" size="sm" className="gap-1.5" onClick={() => setCreateOpen(true)}>
              <Plus className="size-3.5" />
              新建租户
            </Button>
            <DropdownMenu>
              {/* ...existing role-filter dropdown unchanged... */}
            </DropdownMenu>
          </>
        }
```

Add `Plus` to the `lucide-react` import.

- [ ] **Step 3: Add the "指派销售" row action**

In the `rowActions` dropdown content, add a third item after 设单价:

```tsx
                <DropdownMenuItem onClick={() => setAssignTarget(r)}>
                  <UserCog className="size-4" />
                  指派销售 Assign
                </DropdownMenuItem>
```

(`UserCog` is already imported.)

- [ ] **Step 4: Mount the two dialogs**

Next to the existing `<TopupDialog .../>` and `<PricingDialog .../>`, add:

```tsx
      <CreateTenantDialog open={createOpen} onClose={() => setCreateOpen(false)} onDone={load} />
      <AssignSalesDialog target={assignTarget} salesUsers={salesUsers} onClose={() => setAssignTarget(null)} onDone={load} />
```

- [ ] **Step 5: Implement `CreateTenantDialog`**

Append to the file (mirror `TopupDialog`'s structure):

```tsx
function CreateTenantDialog({ open, onClose, onDone }: { open: boolean; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) setName("");
  }, [open]);

  const valid = name.trim().length > 0 && !busy;

  async function submit() {
    setBusy(true);
    try {
      const created = await api.post<{ id: number; name: string }>("/admin/tenants", { name: name.trim() });
      toast.success("租户已创建", { description: `${created.name}(#${created.id})— 请到用户管理为其创建客户账号` });
      onClose();
      onDone();
    } catch (e) {
      toast.error("创建失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>新建租户</DialogTitle>
          <DialogDescription>创建一个新客户租户。新建后它会以「未开通账号」出现在列表中,可直接充值/设价/指派销售。</DialogDescription>
        </DialogHeader>
        <div className="space-y-2 py-1">
          <label htmlFor="tenant-name" className="text-sm font-medium">租户名称</label>
          <Input id="tenant-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Acme Corp" />
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>{busy ? "创建中…" : "确认创建"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
```

- [ ] **Step 6: Implement `AssignSalesDialog`**

Append to the file. Uses a native `<select>` (no new dependency) preselecting the current owner:

```tsx
function AssignSalesDialog({
  target,
  salesUsers,
  onClose,
  onDone,
}: {
  target: Row | null;
  salesUsers: AdminUser[];
  onClose: () => void;
  onDone: () => void;
}) {
  const [salesId, setSalesId] = useState<string>("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (target) setSalesId(target.tenant?.sales_owner_id ? String(target.tenant.sales_owner_id) : "");
  }, [target]);

  const valid = salesId !== "" && !busy;

  async function submit() {
    if (!target?.tenant_id) return;
    setBusy(true);
    try {
      await api.post(`/admin/sales/${target.tenant_id}/assign`, { sales_user_id: Number(salesId) });
      toast.success("已指派销售", { description: `${target.tenant?.name} → ${salesUsers.find((s) => String(s.id) === salesId)?.email ?? salesId}` });
      onClose();
      onDone();
    } catch (e) {
      toast.error("指派失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>指派销售归属</DialogTitle>
          <DialogDescription>为 <span className="font-mono">{target?.tenant?.name}</span> 选择负责的销售账号。</DialogDescription>
        </DialogHeader>
        <div className="space-y-2 py-1">
          <label htmlFor="assign-sales" className="text-sm font-medium">销售账号</label>
          {salesUsers.length === 0 ? (
            <p className="text-sm text-muted-foreground">暂无销售账号,请先在「用户管理」创建一个 sales 用户。</p>
          ) : (
            <select
              id="assign-sales"
              value={salesId}
              onChange={(e) => setSalesId(e.target.value)}
              className="h-9 w-full rounded-lg border bg-transparent px-2.5 text-sm outline-none"
            >
              <option value="" disabled>选择销售…</option>
              {salesUsers.map((s) => (
                <option key={s.id} value={String(s.id)}>{s.email}</option>
              ))}
            </select>
          )}
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>{busy ? "提交中…" : "确认指派"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
```

- [ ] **Step 7: Verify build + lint**

Run: `npm run build && npm run lint`
Expected: clean; `salesUsers` from Task 4 is now consumed.

- [ ] **Step 8: Manual smoke**

`npm run dev`, `/admin/tenants`. Expected:
- 新建租户 → dialog → create → toast → the new tenant appears as a "(未开通账号)" row (Task 4 union).
- On a customer/placeholder row → 操作 → 指派销售 → select a sales user → confirm → toast → 销售归属 column updates after reload.
- With zero sales users, the assign dialog shows the "先创建 sales 用户" hint and the confirm button is disabled.

- [ ] **Step 9: Commit**

```bash
git add frontend/components/admin-tenants-table.tsx
git commit -m "feat(admin): create-tenant + assign-sales dialogs on tenants table

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review (completed by author)

**Spec coverage:**
- ① Ledger page → Task 2. ✓ (both tables, truncation captions, tenant-name map, read-only refunds)
- ② Dashboard drill-down + auto-refresh → Task 1 (StatCard href) + Task 3 (hrefs + 15s poll). ✓
- ③ Sales-owner column + assign → Task 4 (column) + Task 5 (assign dialog). ✓
- ④ Create tenant + orphan visibility (decision A) → Task 4 (union rows) + Task 5 (create dialog). ✓
- Targeted fix: summary dedupe → Task 4 Step 3. ✓
- Non-goals (Top-N, real trends, refund actions, server-side pagination, audit rows) → not present. ✓

**Placeholder scan:** none — every code step contains full code.

**Type consistency:** `Row.placeholder?`, `salesUsers: AdminUser[]`, `salesById: Map<number,string>`, `LedgerRow`/`RefundRow` shapes, `getRowKey` `u`/`t`/`l`/`r` prefixes are consistent across Tasks 2/4/5. `StatCard.href` defined in Task 1, consumed in Task 3. Dialog props mirror existing `TopupDialog`.

**Note for implementer:** verify `@/components/ui/badge` variant names and the `Dialog`/`DropdownMenu` import surface against the actual files before relying on them — they were read during design but confirm at edit time.
