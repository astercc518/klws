"use client";

import { useCallback, useEffect, useState } from "react";
import { Coins, TrendingDown, Users } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { StatCard, MetricCardGroup } from "@/components/admin/stat-card";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";

interface CommissionRow {
  sales_id: number;
  sales_email: string;
  sales_rate: number | null;
  tenant_count: number;
  consumption: number;
  commission: number;
}

interface CommissionResponse {
  month: string;
  rows: CommissionRow[];
  totals: { consumption: number; commission: number };
}

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

export function AdminCommissions() {
  const [month, setMonth] = useState(() => new Date().toISOString().slice(0, 7));
  const [data, setData] = useState<CommissionResponse | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await api.get<CommissionResponse>(`/admin/commissions?month=${month}`));
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, [month]);

  useEffect(() => {
    load();
  }, [load]);

  const cols: Column<CommissionRow>[] = [
    { key: "sales_email", header: "销售", cell: (r) => <span className="text-sm">{r.sales_email}</span> },
    {
      key: "sales_rate",
      header: "比例",
      cell: (r) => (
        <span className="font-mono text-sm tabular-nums text-muted-foreground">
          {r.sales_rate == null ? "按租户" : `${(r.sales_rate * 100).toFixed(1)}%`}
        </span>
      ),
    },
    { key: "tenant_count", header: "名下租户", cell: (r) => <span className="font-mono text-sm tabular-nums">{r.tenant_count}</span> },
    {
      key: "consumption",
      header: "当月消耗",
      align: "right",
      cell: (r) => <span className="font-mono tabular-nums text-rose-600 dark:text-rose-400">{usd(r.consumption)}</span>,
    },
    {
      key: "commission",
      header: "应得佣金",
      align: "right",
      cell: (r) => <span className="font-mono font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">{usd(r.commission)}</span>,
    },
  ];

  if (error) return <Card className="p-5 text-sm text-muted-foreground">加载失败:{error}</Card>;

  const monthPicker = (
    <div className="flex items-center gap-2">
      <input
        type="month"
        value={month}
        onChange={(e) => setMonth(e.target.value)}
        className="h-8 rounded-lg border bg-muted/40 px-2.5 text-sm text-foreground outline-none"
      />
      {data && <span className="font-mono text-xs text-muted-foreground">当前账期:{data.month}</span>}
    </div>
  );

  return (
    <div className="space-y-6">
      <div className="flex justify-end">{monthPicker}</div>

      {!data ? (
        <div className="h-40 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />
      ) : (
        <>
          <MetricCardGroup columns={3}>
            <StatCard accent="rose" label="当月总消耗" value={usd(data.totals.consumption)} icon={TrendingDown} />
            <StatCard accent="emerald" label="当月应得佣金" value={usd(data.totals.commission)} icon={Coins} />
            <StatCard accent="brand" label="销售人数" value={String(data.rows.length)} icon={Users} />
          </MetricCardGroup>

          <ProDataTable
            data={data.rows}
            columns={cols}
            getRowKey={(r) => r.sales_id}
            emptyState="本月暂无销售佣金记录。"
          />

          <p className="font-mono text-xs text-muted-foreground">
            佣金 = 名下各租户当月消耗 × 生效比例(租户比例优先,否则销售默认)。按 Asia/Shanghai 自然月计算,每月自动归零。
          </p>
        </>
      )}
    </div>
  );
}
