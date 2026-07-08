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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
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
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  async function toggleDisabled(u: User) {
    try {
      await api.post(`/admin/users/${u.id}/disable`, { disabled: !u.disabled });
      toast.success(u.disabled ? "已启用" : "已禁用", { description: u.email });
      load();
    } catch (e) {
      toast.error("操作失败", { description: e instanceof ApiError ? e.message : "请重试" });
    }
  }

  async function toggleTenantStatus(u: User, tenant: Tenant | undefined) {
    if (u.tenant_id == null || !tenant || statusBusyId === u.tenant_id) return;
    const next = tenant.status === "suspended" ? "active" : "suspended";
    setStatusBusyId(u.tenant_id);
    try {
      await api.post(`/admin/tenants/${u.tenant_id}/status`, { status: next });
      toast.success(next === "suspended" ? "已挂起" : "已恢复", { description: u.email });
      load();
    } catch (e) {
      toast.error("操作失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setStatusBusyId(null);
    }
  }

  async function quickLogin(u: User) {
    try {
      const r = await impersonate(u.id);
      window.open(`/impersonate#token=${encodeURIComponent(r.token)}&role=${r.role}`, "_blank");
    } catch (e) {
      toast.error("快捷登录失败", { description: e instanceof ApiError ? e.message : "请重试" });
    }
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
    { key: "", label: "全部" },
    { key: "sales", label: "销售" },
    { key: "customer", label: "客户" },
  ];

  if (error) return <Card className="p-5 text-sm text-muted-foreground">加载失败:{error}</Card>;
  if (!users) return <div className="h-64 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />;

  const visible = users.filter((u) => u.role !== "admin");
  const shown = roleTab ? visible.filter((u) => u.role === roleTab) : visible;
  const salesUsers = users.filter((u) => u.role === "sales");

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
          {ROLE_TABS.map((t) => (
            <button
              key={t.key || "all"}
              onClick={() => setRoleTab(t.key)}
              className={
                "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
                (roleTab === t.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
              }
            >
              {t.label}
            </button>
          ))}
        </div>
        <CreateUserDialog tenants={tenants} onDone={load} />
      </div>

      <Card className="overflow-hidden p-0">
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/40">
              <TableHead>账号</TableHead>
              <TableHead>角色</TableHead>
              <TableHead>归属 / 规模</TableHead>
              <TableHead>余额 / 佣金</TableHead>
              <TableHead>状态</TableHead>
              <TableHead className="w-12" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {shown.map((u) => {
              const isCustomer = u.role === "customer";
              const isSales = u.role === "sales";
              const tenant = u.tenant_id != null ? tenantMap.get(u.tenant_id) : undefined;
              const commission = commissionMap.get(u.id);
              const hasTenant = u.tenant_id != null;

              return (
                <TableRow key={u.id}>
                  <TableCell className="font-mono text-sm">{u.email}</TableCell>
                  <TableCell>
                    <Badge variant={roleVariant[u.role]}>{u.role}</Badge>
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">
                    {isSales
                      ? `名下 ${commission?.tenant_count ?? ownedCount.get(u.id) ?? 0} 租户`
                      : tenant
                        ? tenant.name
                        : hasTenant
                          ? `#${u.tenant_id}`
                          : "—"}
                  </TableCell>
                  <TableCell className="text-sm">
                    {isSales
                      ? `本月佣金 ${usd(commission?.commission ?? 0)}`
                      : tenant
                        ? usd(tenant.balance)
                        : "—"}
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {u.disabled ? (
                        <Badge variant="destructive">已禁用</Badge>
                      ) : (
                        <Badge variant="secondary">启用</Badge>
                      )}
                      {isCustomer && tenant?.status === "suspended" && (
                        <Badge variant="outline">挂起</Badge>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <DropdownMenu>
                      <DropdownMenuTrigger
                        render={<Button variant="ghost" size="icon-sm" aria-label="操作" />}
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
                              充值 Top-up
                            </DropdownMenuItem>
                            <DropdownMenuItem
                              disabled={!hasTenant}
                              onClick={() =>
                                u.tenant_id != null &&
                                setLedgerTarget({ tenantId: u.tenant_id, label: u.email })
                              }
                            >
                              <ScrollText className="size-4" />
                              查看流水
                            </DropdownMenuItem>
                            <DropdownMenuItem
                              disabled={!hasTenant}
                              onClick={() =>
                                u.tenant_id != null &&
                                setPricingTarget({ tenantId: u.tenant_id, label: u.email })
                              }
                            >
                              <Tag className="size-4" />
                              发信定价
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
                              指派销售
                            </DropdownMenuItem>
                            <DropdownMenuItem
                              disabled={!hasTenant || statusBusyId === u.tenant_id}
                              onClick={() => toggleTenantStatus(u, tenant)}
                            >
                              {tenant?.status === "suspended" ? (
                                <>
                                  <Unlock className="size-4" />
                                  恢复
                                </>
                              ) : (
                                <>
                                  <Lock className="size-4" />
                                  挂起
                                </>
                              )}
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                          </>
                        )}
                        <DropdownMenuItem onClick={() => quickLogin(u)}>
                          <LogIn className="size-4" />
                          快捷登录
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => setEditTarget(u)}>
                          <Pencil className="size-4" />
                          编辑 Edit
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => toggleDisabled(u)}>
                          {u.disabled ? <CircleCheck className="size-4" /> : <Ban className="size-4" />}
                          {u.disabled ? "启用账号" : "禁用账号"}
                        </DropdownMenuItem>
                        <DropdownMenuItem onClick={() => setPwTarget(u)}>
                          <KeyRound className="size-4" />
                          重置密码
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </Card>

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
      toast.success("用户已创建", { description: `${email} · ${role}` });
      setOpen(false);
      setEmail("");
      setPassword("");
      setRole("customer");
      setTenantId("");
      onDone();
    } catch (e) {
      toast.error("创建失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button className="gap-2" />}>
        <Plus className="size-4" />
        新建用户
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>新建用户</DialogTitle>
          <DialogDescription>创建管理员、销售或客户账号。客户须绑定一个租户。</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="nu-email" className="text-sm font-medium">账号</label>
            <Input
              id="nu-email"
              type="text"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="用户名或邮箱"
              className="font-mono"
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="nu-pw" className="text-sm font-medium">
              初始密码 <span className="font-mono text-xs text-muted-foreground">≥8 位</span>
            </label>
            <Input
              id="nu-pw"
              type="text"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="设置初始密码"
              className="font-mono"
            />
          </div>
          <div className="flex gap-3">
            <div className="space-y-2">
              <label htmlFor="nu-role" className="text-sm font-medium">角色</label>
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
                <label htmlFor="nu-tenant" className="text-sm font-medium">绑定租户</label>
                <select
                  id="nu-tenant"
                  value={tenantId}
                  onChange={(e) => setTenantId(e.target.value)}
                  className="h-9 w-full rounded-md border bg-transparent px-3 text-sm"
                >
                  <option value="">选择租户…</option>
                  {tenants.map((t) => (
                    <option key={t.id} value={t.id}>
                      #{t.id} {t.name}
                    </option>
                  ))}
                </select>
              </div>
            )}
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? "创建中…" : "确认创建"}
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
        toast.error("佣金比例无效", { description: "请输入 0-100 之间的数字" });
        return;
      }
      body.commission_rate = rate / 100;
    }

    setBusy(true);
    try {
      await api.put(`/admin/users/${target.id}`, body);
      toast.success("用户已更新", { description: `${email} · ${role}` });
      onClose();
      onDone();
    } catch (e) {
      toast.error("更新失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>编辑用户</DialogTitle>
          <DialogDescription>修改账号、角色或绑定租户。客户须绑定一个租户。</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="eu-email" className="text-sm font-medium">账号</label>
            <Input
              id="eu-email"
              type="text"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="用户名或邮箱"
              className="font-mono"
            />
          </div>
          <div className="flex gap-3">
            <div className="space-y-2">
              <label htmlFor="eu-role" className="text-sm font-medium">角色</label>
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
                <label htmlFor="eu-tenant" className="text-sm font-medium">绑定租户</label>
                <select
                  id="eu-tenant"
                  value={tenantId}
                  onChange={(e) => setTenantId(e.target.value)}
                  className="h-9 w-full rounded-md border bg-transparent px-3 text-sm"
                >
                  <option value="">选择租户…</option>
                  {tenants.map((t) => (
                    <option key={t.id} value={t.id}>
                      #{t.id} {t.name}
                    </option>
                  ))}
                </select>
              </div>
            )}
          </div>
          {role === "sales" && (
            <div className="space-y-2">
              <label htmlFor="eu-rate" className="text-sm font-medium">佣金比例(%)</label>
              <Input
                id="eu-rate"
                type="number"
                min={0}
                max={100}
                step={0.1}
                value={ratePct}
                onChange={(e) => setRatePct(e.target.value)}
                placeholder="例如 10"
                className="font-mono"
              />
              <p className="text-xs text-muted-foreground">留空表示未设置</p>
            </div>
          )}
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? "保存中…" : "保存"}
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
      toast.success("密码已重置", { description: target.email });
      onClose();
    } catch (e) {
      toast.error("重置失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>重置密码</DialogTitle>
          <DialogDescription>
            为 <span className="font-mono">{target?.email}</span> 设置新密码。用户下次用新密码登录。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2 py-1">
          <label htmlFor="rp-pw" className="text-sm font-medium">
            新密码 <span className="font-mono text-xs text-muted-foreground">≥8 位</span>
          </label>
          <Input
            id="rp-pw"
            type="text"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="输入新密码"
            className="font-mono"
          />
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? "提交中…" : "确认重置"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
