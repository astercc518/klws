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
