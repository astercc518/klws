"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { MoreHorizontal, Wallet, Tag, SlidersHorizontal, Users, UserCog, Lock } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { MetricCardGroup, StatCard } from "@/components/admin/stat-card";
import { RowAvatar } from "@/components/admin/row-avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
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
} from "@/components/ui/dialog";

interface AdminUser {
  id: number;
  email: string;
  role: "admin" | "sales" | "customer";
  tenant_id: number | null;
  disabled: boolean;
}
interface AdminTenant {
  id: number;
  name: string;
  status: string;
  sales_owner_id: number | null;
  balance: number;
  frozen: number;
}
/** A user row joined with its tenant wallet (customers only). */
interface Row extends AdminUser {
  tenant?: AdminTenant;
}

const usd = (smallest: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(smallest / 100);

const roleVariant: Record<AdminUser["role"], "default" | "secondary" | "outline"> = {
  admin: "default",
  sales: "secondary",
  customer: "outline",
};

type RoleFilter = "all" | AdminUser["role"];

const ROLE_FILTERS: { value: RoleFilter; label: string }[] = [
  { value: "all", label: "全部角色" },
  { value: "customer", label: "客户 Customer" },
  { value: "sales", label: "销售 Sales" },
  { value: "admin", label: "管理员 Admin" },
];

export function AdminTenantsTable() {
  const [rows, setRows] = useState<Row[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [roleFilter, setRoleFilter] = useState<RoleFilter>("all");
  const [topupTarget, setTopupTarget] = useState<Row | null>(null);
  const [pricingTarget, setPricingTarget] = useState<Row | null>(null);

  const load = useCallback(async () => {
    try {
      const [users, tenants] = await Promise.all([
        api.get<AdminUser[]>("/admin/users"),
        api.get<AdminTenant[]>("/admin/tenants"),
      ]);
      const byTenant = new Map(tenants.map((t) => [t.id, t]));
      setRows(
        users.map((u) => ({
          ...u,
          tenant: u.tenant_id != null ? byTenant.get(u.tenant_id) : undefined,
        })),
      );
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const visible = useMemo(() => {
    if (!rows) return null;
    return roleFilter === "all" ? rows : rows.filter((r) => r.role === roleFilter);
  }, [rows, roleFilter]);

  // Aggregate the loaded rows into a top-of-page summary (all roles, not the
  // current filter) — derived client-side from data already fetched.
  const summary = useMemo(() => {
    if (!rows) return null;
    let customers = 0;
    let sales = 0;
    let balance = 0;
    let frozen = 0;
    for (const r of rows) {
      if (r.role === "customer") customers++;
      if (r.role === "sales") sales++;
      if (r.tenant) {
        balance += r.tenant.balance;
        frozen += r.tenant.frozen;
      }
    }
    return { customers, sales, balance, frozen };
  }, [rows]);

  const columns: Column<Row>[] = [
    {
      key: "email",
      header: "账号",
      cell: (r) => (
        <div className="flex items-center gap-2.5">
          <RowAvatar text={r.email} />
          <span className="font-mono text-sm">{r.email}</span>
        </div>
      ),
    },
    {
      key: "role",
      header: "角色",
      cell: (r) => <Badge variant={roleVariant[r.role]}>{r.role}</Badge>,
    },
    {
      key: "tenant",
      header: "租户",
      cell: (r) => (
        <span className="text-sm text-muted-foreground">{r.tenant ? r.tenant.name : "—"}</span>
      ),
    },
    {
      key: "balance",
      header: "可用余额",
      align: "right",
      cell: (r) => (
        <span className="font-mono tabular-nums">{r.tenant ? usd(r.tenant.balance) : "—"}</span>
      ),
    },
    {
      key: "frozen",
      header: "冻结",
      align: "right",
      cell: (r) => (
        <span className="font-mono tabular-nums text-muted-foreground">
          {r.tenant ? usd(r.tenant.frozen) : "—"}
        </span>
      ),
    },
  ];

  const activeRoleLabel =
    ROLE_FILTERS.find((f) => f.value === roleFilter)?.label ?? "全部角色";

  return (
    <>
      {summary && (
        <MetricCardGroup className="mb-6">
          <StatCard accent="brand" label="客户账号" value={String(summary.customers)} sub="customer 角色" icon={Users} />
          <StatCard accent="blue" label="销售账号" value={String(summary.sales)} sub="sales 角色" icon={UserCog} />
          <StatCard accent="emerald" label="总可用余额" value={usd(summary.balance)} sub="所有租户钱包合计" icon={Wallet} />
          <StatCard accent="amber" label="总冻结金额" value={usd(summary.frozen)} sub="预扣发送中" icon={Lock} />
        </MetricCardGroup>
      )}
      <ProDataTable
        data={visible}
        error={error}
        columns={columns}
        getRowKey={(r) => `${r.role}-${r.id}`}
        search={{ placeholder: "搜索账号或租户…", accessor: (r) => `${r.email} ${r.tenant?.name ?? ""}` }}
        emptyState="暂无账号"
        toolbar={
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="outline" size="sm" className="gap-1.5" />}>
              <SlidersHorizontal className="size-3.5" />
              {activeRoleLabel}
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-44">
              <DropdownMenuLabel>按角色筛选</DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuRadioGroup
                value={roleFilter}
                onValueChange={(v) => setRoleFilter(v as RoleFilter)}
              >
                {ROLE_FILTERS.map((f) => (
                  <DropdownMenuRadioItem key={f.value} value={f.value}>
                    {f.label}
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        }
        rowActions={(r) => {
          const isCustomer = r.role === "customer" && r.tenant_id != null;
          if (!isCustomer) return null;
          return (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<Button variant="ghost" size="icon-sm" aria-label="操作" />}
              >
                <MoreHorizontal className="size-4" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onClick={() => setTopupTarget(r)}>
                  <Wallet className="size-4" />
                  充值 Top-up
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setPricingTarget(r)}>
                  <Tag className="size-4" />
                  设单价 Pricing
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          );
        }}
      />

      <TopupDialog target={topupTarget} onClose={() => setTopupTarget(null)} onDone={load} />
      <PricingDialog target={pricingTarget} onClose={() => setPricingTarget(null)} onDone={load} />
    </>
  );
}

// ---------------------------------------------------------------------------
// Top-up dialog → POST /admin/finance/topup
// ---------------------------------------------------------------------------

function TopupDialog({
  target,
  onClose,
  onDone,
}: {
  target: Row | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const [amount, setAmount] = useState("");
  const [ref, setRef] = useState("");
  const [busy, setBusy] = useState(false);

  // Reset fields whenever a new target opens the dialog.
  useEffect(() => {
    if (target) {
      setAmount("");
      setRef(`manual-${Date.now()}`);
    }
  }, [target]);

  const cents = Math.round(parseFloat(amount) * 100);
  const valid = !Number.isNaN(cents) && cents > 0 && ref.trim().length > 0 && !busy;

  async function submit() {
    if (!target?.tenant_id) return;
    setBusy(true);
    try {
      await api.post("/admin/finance/topup", {
        tenant_id: target.tenant_id,
        amount: cents,
        ref: ref.trim(),
      });
      toast.success("充值成功", { description: `${target.email} +$${(cents / 100).toFixed(2)}` });
      onClose();
      onDone();
    } catch (e) {
      toast.error("充值失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>充值 Top-up</DialogTitle>
          <DialogDescription>
            为 <span className="font-mono">{target?.tenant?.name}</span> 的钱包手动加款。请再次核对金额。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="topup-amount" className="text-sm font-medium">
              充值金额 <span className="font-mono text-xs text-muted-foreground">USD</span>
            </label>
            <Input
              id="topup-amount"
              type="number"
              min="0"
              step="0.01"
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
              placeholder="100.00"
              className="font-mono"
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="topup-ref" className="text-sm font-medium">
              流水号 <span className="font-mono text-xs text-muted-foreground">幂等键</span>
            </label>
            <Input
              id="topup-ref"
              value={ref}
              onChange={(e) => setRef(e.target.value)}
              className="font-mono text-sm"
            />
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? "提交中…" : "确认充值"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Pricing dialog → POST /admin/finance/pricing
// ---------------------------------------------------------------------------

function PricingDialog({
  target,
  onClose,
  onDone,
}: {
  target: Row | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const [country, setCountry] = useState("US");
  const [price, setPrice] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (target) {
      setCountry("US");
      setPrice("");
    }
  }, [target]);

  const cents = Math.round(parseFloat(price) * 100);
  const valid =
    country.trim().length === 2 && !Number.isNaN(cents) && cents > 0 && !busy;

  async function submit() {
    if (!target?.tenant_id) return;
    setBusy(true);
    try {
      await api.post("/admin/finance/pricing", {
        tenant_id: target.tenant_id,
        country: country.trim().toUpperCase(),
        unit_price: cents,
      });
      toast.success("单价已更新", {
        description: `${country.toUpperCase()} → $${(cents / 100).toFixed(2)} / 条`,
      });
      onClose();
      onDone();
    } catch (e) {
      toast.error("设置失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>配置发信单价</DialogTitle>
          <DialogDescription>
            为 <span className="font-mono">{target?.tenant?.name}</span> 设置指定国家的每条单价。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="price-country" className="text-sm font-medium">
              国家 <span className="font-mono text-xs text-muted-foreground">ISO-2</span>
            </label>
            <Input
              id="price-country"
              value={country}
              onChange={(e) => setCountry(e.target.value)}
              maxLength={2}
              className="w-24 font-mono uppercase"
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="price-unit" className="text-sm font-medium">
              每条单价 <span className="font-mono text-xs text-muted-foreground">USD</span>
            </label>
            <Input
              id="price-unit"
              type="number"
              min="0"
              step="0.01"
              value={price}
              onChange={(e) => setPrice(e.target.value)}
              placeholder="0.05"
              className="font-mono"
            />
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? "提交中…" : "确认设置"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
