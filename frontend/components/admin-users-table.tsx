"use client";

import { useCallback, useEffect, useState } from "react";
import {
  MoreHorizontal,
  Plus,
  Ban,
  CircleCheck,
  KeyRound,
  Pencil,
  Wallet,
  Tag,
  UserCog,
  Lock,
  Unlock,
  LogIn,
  ScrollText,
} from "lucide-react";
import { toast } from "sonner";
import { api, ApiError, impersonate } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { BulkActionDialog } from "@/components/admin/bulk-action-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { CustomerLedgerSheet } from "@/components/customer-ledger-sheet";
import {
  TopupDialog,
  PricingDialog,
  AssignSalesDialog,
  type TenantActionTarget,
  type AssignActionTarget,
} from "@/components/admin-user-dialogs";
import { useT } from "@/components/locale-provider";

type Role = "admin" | "sales" | "customer";
interface User {
  id: number;
  email: string;
  role: Role;
  tenant_id: number | null;
  disabled: boolean;
  commission_rate: number | null;
}
interface Tenant {
  id: number;
  name: string;
  status: string;
  sales_owner_id: number | null;
  balance: number;
  frozen: number;
  commission_rate: number | null;
}
interface CommissionRow {
  sales_id: number;
  commission: number;
  tenant_count: number;
}
interface CommissionResponse {
  rows: CommissionRow[];
}

const roleVariant: Record<Role, "default" | "secondary" | "outline"> = {
  admin: "default",
  sales: "secondary",
  customer: "outline",
};

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

export function AdminUsersTable() {
  const t = useT();
  const [users, setUsers] = useState<User[] | null>(null);
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [commissions, setCommissions] = useState<CommissionRow[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [pwTarget, setPwTarget] = useState<User | null>(null);
  const [editTarget, setEditTarget] = useState<User | null>(null);
  const [topupTarget, setTopupTarget] = useState<TenantActionTarget | null>(null);
  const [pricingTarget, setPricingTarget] = useState<TenantActionTarget | null>(null);
  const [assignTarget, setAssignTarget] = useState<AssignActionTarget | null>(null);
  const [ledgerTarget, setLedgerTarget] = useState<TenantActionTarget | null>(null);
  const [statusBusyId, setStatusBusyId] = useState<number | null>(null);
  const [sel, setSel] = useState<Set<string | number>>(new Set());
  const [bulkStatusTarget, setBulkStatusTarget] = useState<{
    status: "suspended" | "active";
    keys: Array<string | number>;
  } | null>(null);
  const [bulkAssignKeys, setBulkAssignKeys] = useState<Array<string | number> | null>(null);
  const [bulkSalesId, setBulkSalesId] = useState("");

  const load = useCallback(async () => {
    try {
      const month = new Date().toISOString().slice(0, 7);
      const [u, t, c] = await Promise.all([
        api.get<User[]>("/admin/users"),
        api.get<Tenant[]>("/admin/tenants"),
        api.get<CommissionResponse>(`/admin/commissions?month=${month}`),
      ]);
      setUsers(u);
      setTenants(t);
      setCommissions(c.rows);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("admin.users.loadFailed"));
      }
    }
  }, [t]);
  useEffect(() => {
    load();
  }, [load]);

  async function toggleDisabled(u: User) {
    try {
      await api.post(`/admin/users/${u.id}/disable`, { disabled: !u.disabled });
      toast.success(u.disabled ? t("admin.users.toast.enabledTitle") : t("admin.users.toast.disabledTitle"), {
        description: u.email,
      });
      load();
    } catch (e) {
      toast.error(t("admin.users.toast.actionFailed"), {
        description: e instanceof ApiError ? e.message : t("admin.users.retry"),
      });
    }
  }

  async function toggleTenantStatus(u: User, tenant: Tenant | undefined) {
    if (u.tenant_id == null || !tenant || statusBusyId === u.tenant_id) return;
    const next = tenant.status === "suspended" ? "active" : "suspended";
    setStatusBusyId(u.tenant_id);
    try {
      await api.post(`/admin/tenants/${u.tenant_id}/status`, { status: next });
      toast.success(next === "suspended" ? t("admin.users.toast.suspendedTitle") : t("admin.users.toast.resumedTitle"), {
        description: u.email,
      });
      load();
    } catch (e) {
      toast.error(t("admin.users.toast.actionFailed"), {
        description: e instanceof ApiError ? e.message : t("admin.users.retry"),
      });
    } finally {
      setStatusBusyId(null);
    }
  }

  async function quickLogin(u: User) {
    try {
      const r = await impersonate(u.id);
      window.open(`/impersonate#token=${encodeURIComponent(r.token)}&role=${r.role}`, "_blank");
    } catch (e) {
      toast.error(t("admin.users.toast.quickLoginFailed"), {
        description: e instanceof ApiError ? e.message : t("admin.users.retry"),
      });
    }
  }

  // Bulk actions operate on the customer's tenant (status/assignment are
  // tenant-scoped, not user-scoped — see toggleTenantStatus/AssignSalesDialog
  // above). Selected rows without a role=customer + tenant_id (sales/admin
  // rows, or an orphaned customer with no tenant) are silently skipped; the
  // dialog description reports the skipped count so it isn't a silent no-op.
  function eligibleTenantTargets(keys: Array<string | number>): { tenantId: number; label: string }[] {
    if (!users) return [];
    const byId = new Map(users.map((u) => [u.id, u]));
    const out: { tenantId: number; label: string }[] = [];
    for (const k of keys) {
      const u = byId.get(Number(k));
      if (u && u.role === "customer" && u.tenant_id != null) out.push({ tenantId: u.tenant_id, label: u.email });
    }
    return out;
  }

  async function submitBulkStatus() {
    if (!bulkStatusTarget) return;
    const targets = eligibleTenantTargets(bulkStatusTarget.keys);
    let okCount = 0;
    let failCount = 0;
    for (const tgt of targets) {
      try {
        await api.post(`/admin/tenants/${tgt.tenantId}/status`, { status: bulkStatusTarget.status });
        okCount++;
      } catch {
        failCount++;
      }
    }
    const desc = t("admin.users.bulk.resultDesc")
      .replace("{ok}", () => String(okCount))
      .replace("{fail}", () => String(failCount));
    if (failCount === 0) {
      toast.success(t("admin.users.bulk.resultTitle"), { description: desc });
    } else {
      toast.error(t("admin.users.bulk.resultTitle"), { description: desc });
    }
    setBulkStatusTarget(null);
    setSel(new Set());
    load();
  }

  async function submitBulkAssign() {
    if (!bulkAssignKeys) return;
    if (bulkSalesId === "") {
      toast.error(t("admin.users.bulk.assignNoSalesSelected"));
      return;
    }
    const targets = eligibleTenantTargets(bulkAssignKeys);
    let okCount = 0;
    let failCount = 0;
    for (const tgt of targets) {
      try {
        await api.post(`/admin/sales/${tgt.tenantId}/assign`, { sales_user_id: Number(bulkSalesId) });
        okCount++;
      } catch {
        failCount++;
      }
    }
    const desc = t("admin.users.bulk.resultDesc")
      .replace("{ok}", () => String(okCount))
      .replace("{fail}", () => String(failCount));
    if (failCount === 0) {
      toast.success(t("admin.users.bulk.resultTitle"), { description: desc });
    } else {
      toast.error(t("admin.users.bulk.resultTitle"), { description: desc });
    }
    setBulkAssignKeys(null);
    setSel(new Set());
    load();
  }

  const [roleTab, setRoleTab] = useState<"" | Role>("");

  // tenant id → tenant (wallet/status/sales-owner/commission), for the
  // customer columns + tenant-scoped row actions below.
  const tenantMap = new Map<number, Tenant>(tenants.map((t) => [t.id, t]));

  // sales user id → this month's commission summary, from /admin/commissions.
  const commissionMap = new Map<number, { commission: number; tenant_count: number }>();
  for (const r of commissions) {
    commissionMap.set(r.sales_id, { commission: r.commission, tenant_count: r.tenant_count });
  }

  // tenants owned per sales user id (client-side join, fallback for the 名下
  // 租户 column when a sales rep has no commission row yet this month).
  const ownedCount = new Map<number, number>();
  for (const t of tenants) {
    if (t.sales_owner_id != null) {
      ownedCount.set(t.sales_owner_id, (ownedCount.get(t.sales_owner_id) ?? 0) + 1);
    }
  }

  const ROLE_TABS: { key: "" | Role; label: string }[] = [
    { key: "", label: t("admin.users.tab.all") },
    { key: "sales", label: t("admin.users.tab.sales") },
    { key: "customer", label: t("admin.users.tab.customer") },
  ];

  if (error)
    return (
      <Card className="p-5 text-sm text-muted-foreground">
        {t("admin.users.loadFailedPrefix")}
        {error}
      </Card>
    );
  if (!users) return <div className="h-64 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />;

  const visible = users.filter((u) => u.role !== "admin");
  const shown = roleTab ? visible.filter((u) => u.role === roleTab) : visible;
  const salesUsers = users.filter((u) => u.role === "sales");

  const columns: Column<User>[] = [
    {
      key: "account",
      header: t("admin.users.accountLabel"),
      title: t("admin.users.accountLabel"),
      hideable: false,
      cell: (u) => <span className="font-mono text-sm">{u.email}</span>,
    },
    {
      key: "role",
      header: t("admin.users.roleLabel"),
      title: t("admin.users.roleLabel"),
      cell: (u) => <Badge variant={roleVariant[u.role]}>{u.role}</Badge>,
    },
    {
      key: "scale",
      header: t("admin.users.col.affiliationScale"),
      title: t("admin.users.col.affiliationScale"),
      cell: (u) => {
        const tenant = u.tenant_id != null ? tenantMap.get(u.tenant_id) : undefined;
        const commission = commissionMap.get(u.id);
        return (
          <span className="text-sm text-muted-foreground">
            {u.role === "sales"
              ? t("admin.users.ownedTenants").replace(
                  "{n}",
                  () => String(commission?.tenant_count ?? ownedCount.get(u.id) ?? 0),
                )
              : tenant
                ? tenant.name
                : u.tenant_id != null
                  ? `#${u.tenant_id}`
                  : "—"}
          </span>
        );
      },
    },
    {
      key: "balance",
      header: t("admin.users.col.balanceCommission"),
      title: t("admin.users.col.balanceCommission"),
      cell: (u) => {
        const tenant = u.tenant_id != null ? tenantMap.get(u.tenant_id) : undefined;
        const commission = commissionMap.get(u.id);
        return (
          <span className="text-sm">
            {u.role === "sales"
              ? t("admin.users.monthlyCommission").replace("{amount}", () => usd(commission?.commission ?? 0))
              : tenant
                ? usd(tenant.balance)
                : "—"}
          </span>
        );
      },
    },
    {
      key: "status",
      header: t("admin.users.col.status"),
      title: t("admin.users.col.status"),
      cell: (u) => {
        const tenant = u.tenant_id != null ? tenantMap.get(u.tenant_id) : undefined;
        return (
          <div className="flex flex-wrap gap-1">
            {u.disabled ? (
              <Badge variant="destructive">{t("admin.users.badge.disabled")}</Badge>
            ) : (
              <Badge variant="secondary">{t("admin.users.badge.enabled")}</Badge>
            )}
            {u.role === "customer" && tenant?.status === "suspended" && (
              <Badge variant="outline">{t("admin.users.badge.suspended")}</Badge>
            )}
          </div>
        );
      },
    },
  ];

  function renderRowActions(u: User) {
    const isCustomer = u.role === "customer";
    const tenant = u.tenant_id != null ? tenantMap.get(u.tenant_id) : undefined;
    const hasTenant = u.tenant_id != null;
    return (
      <DropdownMenu>
        <DropdownMenuTrigger
          render={<Button variant="ghost" size="icon-sm" aria-label={t("admin.users.actionsAria")} />}
        >
          <MoreHorizontal className="size-4" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {isCustomer && (
            <>
              <DropdownMenuItem
                disabled={!hasTenant}
                onClick={() =>
                  u.tenant_id != null &&
                  setTopupTarget({ tenantId: u.tenant_id, label: u.email })
                }
              >
                <Wallet className="size-4" />
                {t("admin.users.action.topup")}
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={!hasTenant}
                onClick={() =>
                  u.tenant_id != null &&
                  setLedgerTarget({ tenantId: u.tenant_id, label: u.email })
                }
              >
                <ScrollText className="size-4" />
                {t("admin.users.action.viewLedger")}
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={!hasTenant}
                onClick={() =>
                  u.tenant_id != null &&
                  setPricingTarget({ tenantId: u.tenant_id, label: u.email })
                }
              >
                <Tag className="size-4" />
                {t("admin.users.action.pricing")}
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={!hasTenant}
                onClick={() =>
                  u.tenant_id != null &&
                  setAssignTarget({
                    tenantId: u.tenant_id,
                    label: u.email,
                    currentSalesId: tenant?.sales_owner_id ?? null,
                  })
                }
              >
                <UserCog className="size-4" />
                {t("admin.users.action.assignSales")}
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={!hasTenant || statusBusyId === u.tenant_id}
                onClick={() => toggleTenantStatus(u, tenant)}
              >
                {tenant?.status === "suspended" ? (
                  <>
                    <Unlock className="size-4" />
                    {t("admin.users.action.resume")}
                  </>
                ) : (
                  <>
                    <Lock className="size-4" />
                    {t("admin.users.action.suspend")}
                  </>
                )}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
            </>
          )}
          <DropdownMenuItem onClick={() => quickLogin(u)}>
            <LogIn className="size-4" />
            {t("admin.users.action.quickLogin")}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => setEditTarget(u)}>
            <Pencil className="size-4" />
            {t("admin.users.action.edit")}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => toggleDisabled(u)}>
            {u.disabled ? <CircleCheck className="size-4" /> : <Ban className="size-4" />}
            {u.disabled ? t("admin.users.action.enableAccount") : t("admin.users.action.disableAccount")}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => setPwTarget(u)}>
            <KeyRound className="size-4" />
            {t("admin.users.action.resetPassword")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    );
  }

  // Bulk dialog description text — recomputed from current selection so the
  // "N customers, M skipped" counts stay accurate while the dialog is open.
  const bulkStatusTargets = bulkStatusTarget ? eligibleTenantTargets(bulkStatusTarget.keys) : [];
  const bulkStatusSkipped = bulkStatusTarget ? bulkStatusTarget.keys.length - bulkStatusTargets.length : 0;
  const bulkStatusDescKey =
    bulkStatusTarget?.status === "suspended" ? "admin.users.bulkSuspendDialog.desc" : "admin.users.bulkResumeDialog.desc";
  const bulkStatusDescription =
    t(bulkStatusDescKey).replace("{n}", () => String(bulkStatusTargets.length)) +
    (bulkStatusSkipped > 0
      ? " " + t("admin.users.bulk.skippedNotice").replace("{skipped}", () => String(bulkStatusSkipped))
      : "");

  const bulkAssignTargets = bulkAssignKeys ? eligibleTenantTargets(bulkAssignKeys) : [];
  const bulkAssignSkipped = bulkAssignKeys ? bulkAssignKeys.length - bulkAssignTargets.length : 0;
  const bulkAssignDescription =
    t("admin.users.bulkAssignDialog.desc").replace("{n}", () => String(bulkAssignTargets.length)) +
    (bulkAssignSkipped > 0
      ? " " + t("admin.users.bulk.skippedNotice").replace("{skipped}", () => String(bulkAssignSkipped))
      : "");

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
          {ROLE_TABS.map((tab) => (
            <button
              key={tab.key || "all"}
              onClick={() => setRoleTab(tab.key)}
              className={
                "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
                (roleTab === tab.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
              }
            >
              {tab.label}
            </button>
          ))}
        </div>
        <CreateUserDialog tenants={tenants} onDone={load} />
      </div>

      <ProDataTable
        data={shown}
        columns={columns}
        getRowKey={(u) => u.id}
        pageSize={50}
        storageKey="admin-users-table"
        rowActions={renderRowActions}
        selection={{
          selected: sel,
          onChange: setSel,
          actions: (keys) => (
            <>
              <Button
                variant="outline"
                size="sm"
                className="gap-1.5"
                onClick={() => setBulkStatusTarget({ status: "suspended", keys })}
              >
                <Lock className="size-3.5" />
                {t("admin.users.bulk.suspend")}
              </Button>
              <Button
                variant="outline"
                size="sm"
                className="gap-1.5"
                onClick={() => setBulkStatusTarget({ status: "active", keys })}
              >
                <Unlock className="size-3.5" />
                {t("admin.users.bulk.resume")}
              </Button>
              <Button
                variant="outline"
                size="sm"
                className="gap-1.5"
                onClick={() => {
                  setBulkSalesId("");
                  setBulkAssignKeys(keys);
                }}
              >
                <UserCog className="size-3.5" />
                {t("admin.users.bulk.assignSales")}
              </Button>
            </>
          ),
        }}
      />

      <BulkActionDialog
        open={bulkStatusTarget != null}
        onOpenChange={(o) => !o && setBulkStatusTarget(null)}
        title={
          bulkStatusTarget?.status === "suspended"
            ? t("admin.users.bulkSuspendDialog.title")
            : t("admin.users.bulkResumeDialog.title")
        }
        description={bulkStatusDescription}
        confirmLabel={t("admin.users.bulk.confirm")}
        destructive={bulkStatusTarget?.status === "suspended"}
        onConfirm={submitBulkStatus}
      />
      <BulkActionDialog
        open={bulkAssignKeys != null}
        onOpenChange={(o) => !o && setBulkAssignKeys(null)}
        title={t("admin.users.bulkAssignDialog.title")}
        description={bulkAssignDescription}
        confirmLabel={t("admin.users.bulk.confirm")}
        onConfirm={submitBulkAssign}
      >
        <div className="space-y-2">
          <label htmlFor="bulk-assign-sales" className="text-sm font-medium">
            {t("admin.users.dlg.salesAccountLabel")}
          </label>
          {salesUsers.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("admin.users.dlg.noSalesUsers")}</p>
          ) : (
            <select
              id="bulk-assign-sales"
              value={bulkSalesId}
              onChange={(e) => setBulkSalesId(e.target.value)}
              className="h-9 w-full rounded-lg border bg-transparent px-2.5 text-sm outline-none"
            >
              <option value="" disabled>
                {t("admin.users.dlg.selectSalesPlaceholder")}
              </option>
              {salesUsers.map((s) => (
                <option key={s.id} value={String(s.id)}>
                  {s.email}
                </option>
              ))}
            </select>
          )}
        </div>
      </BulkActionDialog>

      <ResetPasswordDialog target={pwTarget} onClose={() => setPwTarget(null)} />
      <EditUserDialog
        target={editTarget}
        tenants={tenants}
        onClose={() => setEditTarget(null)}
        onDone={load}
      />
      <TopupDialog target={topupTarget} onClose={() => setTopupTarget(null)} onDone={load} />
      <PricingDialog target={pricingTarget} onClose={() => setPricingTarget(null)} onDone={load} />
      <AssignSalesDialog
        target={assignTarget}
        salesUsers={salesUsers}
        onClose={() => setAssignTarget(null)}
        onDone={load}
      />
      <CustomerLedgerSheet
        tenantId={ledgerTarget?.tenantId ?? null}
        label={ledgerTarget?.label ?? ""}
        onClose={() => setLedgerTarget(null)}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Create user → POST /admin/users
// ---------------------------------------------------------------------------

function CreateUserDialog({ tenants, onDone }: { tenants: Tenant[]; onDone: () => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("customer");
  const [tenantId, setTenantId] = useState<string>("");
  const [busy, setBusy] = useState(false);

  const needsTenant = role === "customer";
  const valid =
    email.trim().length >= 3 &&
    password.length >= 8 &&
    (!needsTenant || tenantId !== "") &&
    !busy;

  async function submit() {
    setBusy(true);
    try {
      await api.post("/admin/users", {
        email: email.trim(),
        password,
        role,
        tenant_id: needsTenant ? Number(tenantId) : undefined,
      });
      toast.success(t("admin.users.create.successTitle"), { description: `${email} · ${role}` });
      setOpen(false);
      setEmail("");
      setPassword("");
      setRole("customer");
      setTenantId("");
      onDone();
    } catch (e) {
      toast.error(t("admin.users.create.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.users.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button className="gap-2" />}>
        <Plus className="size-4" />
        {t("admin.users.create.title")}
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.users.create.title")}</DialogTitle>
          <DialogDescription>{t("admin.users.create.desc")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="nu-email" className="text-sm font-medium">{t("admin.users.accountLabel")}</label>
            <Input
              id="nu-email"
              type="text"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder={t("admin.users.accountPlaceholder")}
              className="font-mono"
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="nu-pw" className="text-sm font-medium">
              {t("admin.users.create.pwLabel")}{" "}
              <span className="font-mono text-xs text-muted-foreground">{t("admin.users.hint.min8")}</span>
            </label>
            <Input
              id="nu-pw"
              type="text"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder={t("admin.users.create.pwPlaceholder")}
              className="font-mono"
            />
          </div>
          <div className="flex gap-3">
            <div className="space-y-2">
              <label htmlFor="nu-role" className="text-sm font-medium">{t("admin.users.roleLabel")}</label>
              <select
                id="nu-role"
                value={role}
                onChange={(e) => setRole(e.target.value as Role)}
                className="h-9 rounded-md border bg-transparent px-3 text-sm"
              >
                <option value="customer">customer</option>
                <option value="sales">sales</option>
                <option value="admin">admin</option>
              </select>
            </div>
            {needsTenant && (
              <div className="flex-1 space-y-2">
                <label htmlFor="nu-tenant" className="text-sm font-medium">{t("admin.users.bindTenantLabel")}</label>
                <select
                  id="nu-tenant"
                  value={tenantId}
                  onChange={(e) => setTenantId(e.target.value)}
                  className="h-9 w-full rounded-md border bg-transparent px-3 text-sm"
                >
                  <option value="">{t("admin.users.selectTenantPlaceholder")}</option>
                  {tenants.map((tn) => (
                    <option key={tn.id} value={tn.id}>
                      #{tn.id} {tn.name}
                    </option>
                  ))}
                </select>
              </div>
            )}
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.users.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? t("admin.users.create.creating") : t("admin.users.create.confirm")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Edit user → PUT /admin/users/:id
// ---------------------------------------------------------------------------

function EditUserDialog({
  target,
  tenants,
  onClose,
  onDone,
}: {
  target: User | null;
  tenants: Tenant[];
  onClose: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("customer");
  const [tenantId, setTenantId] = useState<string>("");
  const [ratePct, setRatePct] = useState<string>("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (target) {
      setEmail(target.email);
      setRole(target.role);
      setTenantId(target.tenant_id != null ? String(target.tenant_id) : "");
      setRatePct(target.commission_rate != null ? String(Math.round(target.commission_rate * 10000) / 100) : "");
    }
  }, [target]);

  const needsTenant = role === "customer";
  const valid =
    email.trim().length >= 3 &&
    (!needsTenant || tenantId !== "") &&
    !busy;

  async function submit() {
    if (!target) return;

    const body: Record<string, unknown> = {
      email: email.trim(),
      role,
      tenant_id: needsTenant ? Number(tenantId) : undefined,
    };

    if (role === "sales" && ratePct.trim() !== "") {
      const rate = Number(ratePct);
      if (!Number.isFinite(rate) || rate < 0 || rate > 100) {
        toast.error(t("admin.users.edit.rateInvalidTitle"), { description: t("admin.users.edit.rateInvalidDesc") });
        return;
      }
      body.commission_rate = rate / 100;
    }

    setBusy(true);
    try {
      await api.put(`/admin/users/${target.id}`, body);
      toast.success(t("admin.users.edit.successTitle"), { description: `${email} · ${role}` });
      onClose();
      onDone();
    } catch (e) {
      toast.error(t("admin.users.edit.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.users.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.users.edit.title")}</DialogTitle>
          <DialogDescription>{t("admin.users.edit.desc")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="eu-email" className="text-sm font-medium">{t("admin.users.accountLabel")}</label>
            <Input
              id="eu-email"
              type="text"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder={t("admin.users.accountPlaceholder")}
              className="font-mono"
            />
          </div>
          <div className="flex gap-3">
            <div className="space-y-2">
              <label htmlFor="eu-role" className="text-sm font-medium">{t("admin.users.roleLabel")}</label>
              <select
                id="eu-role"
                value={role}
                onChange={(e) => setRole(e.target.value as Role)}
                className="h-9 rounded-md border bg-transparent px-3 text-sm"
              >
                <option value="customer">customer</option>
                <option value="sales">sales</option>
                <option value="admin">admin</option>
              </select>
            </div>
            {needsTenant && (
              <div className="flex-1 space-y-2">
                <label htmlFor="eu-tenant" className="text-sm font-medium">{t("admin.users.bindTenantLabel")}</label>
                <select
                  id="eu-tenant"
                  value={tenantId}
                  onChange={(e) => setTenantId(e.target.value)}
                  className="h-9 w-full rounded-md border bg-transparent px-3 text-sm"
                >
                  <option value="">{t("admin.users.selectTenantPlaceholder")}</option>
                  {tenants.map((tn) => (
                    <option key={tn.id} value={tn.id}>
                      #{tn.id} {tn.name}
                    </option>
                  ))}
                </select>
              </div>
            )}
          </div>
          {role === "sales" && (
            <div className="space-y-2">
              <label htmlFor="eu-rate" className="text-sm font-medium">{t("admin.users.edit.rateLabel")}</label>
              <Input
                id="eu-rate"
                type="number"
                min={0}
                max={100}
                step={0.1}
                value={ratePct}
                onChange={(e) => setRatePct(e.target.value)}
                placeholder={t("admin.users.edit.ratePlaceholder")}
                className="font-mono"
              />
              <p className="text-xs text-muted-foreground">{t("admin.users.edit.rateEmptyHint")}</p>
            </div>
          )}
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.users.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? t("admin.users.edit.saving") : t("admin.users.edit.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Reset password → POST /admin/users/:id/password
// ---------------------------------------------------------------------------

function ResetPasswordDialog({ target, onClose }: { target: User | null; onClose: () => void }) {
  const t = useT();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (target) setPassword("");
  }, [target]);

  const valid = password.length >= 8 && !busy;

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.post(`/admin/users/${target.id}/password`, { password });
      toast.success(t("admin.users.resetPw.successTitle"), { description: target.email });
      onClose();
    } catch (e) {
      toast.error(t("admin.users.resetPw.failedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.users.retry"),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("admin.users.resetPw.title")}</DialogTitle>
          <DialogDescription>
            {t("admin.users.resetPw.descPrefix")}
            <span className="font-mono">{target?.email}</span>
            {t("admin.users.resetPw.descSuffix")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2 py-1">
          <label htmlFor="rp-pw" className="text-sm font-medium">
            {t("admin.users.resetPw.newPwLabel")}{" "}
            <span className="font-mono text-xs text-muted-foreground">{t("admin.users.hint.min8")}</span>
          </label>
          <Input
            id="rp-pw"
            type="text"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t("admin.users.resetPw.newPwPlaceholder")}
            className="font-mono"
          />
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>{t("admin.users.cancel")}</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? t("admin.users.resetPw.submitting") : t("admin.users.resetPw.confirm")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
