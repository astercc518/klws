"use client";

import { useCallback, useEffect, useState } from "react";
import { ArrowLeft, Download, Wallet, TrendingDown, RotateCcw, Scale } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { StatCard, MetricCardGroup } from "@/components/admin/stat-card";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { BillingTrendChart, type DailyPoint } from "@/components/billing-trend-chart";

interface Summary {
  topup: number;
  settle: number;
  refund: number;
  adjust: number;
  net: number;
}
interface TenantSpend {
  tenant_id: number;
  name: string;
  settle: number;
  topup: number;
}
interface Stats {
  summary: Summary;
  daily: DailyPoint[];
  top_tenants: TenantSpend[];
}
interface Bill {
  tenant_id: number;
  name: string;
  opening: number;
  closing: number;
  summary: { topup: number; settle: number; refund: number; adjust: number };
  daily: DailyPoint[];
}

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

const RANGES: { key: string; label: string; days: number }[] = [
  { key: "7", label: "近 7 天", days: 7 },
  { key: "30", label: "近 30 天", days: 30 },
  { key: "90", label: "近 90 天", days: 90 },
];

// Compute a YYYY-MM-DD pair for the last N days ending today (client-local).
function rangeParams(days: number): { from: string; to: string } {
  const now = new Date();
  const to = now.toISOString().slice(0, 10);
  const fromDate = new Date(now.getTime() - (days - 1) * 86400000);
  const from = fromDate.toISOString().slice(0, 10);
  return { from, to };
}

export function AdminBilling() {
  const [rangeKey, setRangeKey] = useState("30");
  const [stats, setStats] = useState<Stats | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<TenantSpend | null>(null);
  const [bill, setBill] = useState<Bill | null>(null);

  const days = RANGES.find((r) => r.key === rangeKey)?.days ?? 30;

  const loadStats = useCallback(async () => {
    const { from, to } = rangeParams(days);
    try {
      setStats(await api.get<Stats>(`/admin/finance/stats?from=${from}&to=${to}`));
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, [days]);

  useEffect(() => {
    loadStats();
  }, [loadStats]);

  const loadBill = useCallback(
    async (t: TenantSpend) => {
      const { from, to } = rangeParams(days);
      setSelected(t);
      setBill(null);
      try {
        setBill(await api.get<Bill>(`/admin/finance/bill?tenant_id=${t.tenant_id}&from=${from}&to=${to}`));
      } catch (e) {
        toast.error("加载账单失败", { description: e instanceof ApiError ? e.message : "请重试" });
      }
    },
    [days],
  );

  async function exportBill() {
    if (!selected) return;
    const { from, to } = rangeParams(days);
    try {
      await api.download(
        `/admin/finance/bill/export?tenant_id=${selected.tenant_id}&from=${from}&to=${to}`,
        `bill-${selected.tenant_id}.csv`,
      );
    } catch (e) {
      toast.error("导出失败", { description: e instanceof ApiError ? e.message : "请重试" });
    }
  }

  const rangePicker = (
    <div className="inline-flex rounded-lg border bg-muted/40 p-0.5">
      {RANGES.map((r) => (
        <button
          key={r.key}
          onClick={() => setRangeKey(r.key)}
          className={
            "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
            (rangeKey === r.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
          }
        >
          {r.label}
        </button>
      ))}
    </div>
  );

  // --- per-tenant bill view ---
  if (selected) {
    return (
      <div className="space-y-6">
        <div className="flex items-center justify-between gap-3">
          <Button variant="ghost" size="sm" className="gap-1.5" onClick={() => { setSelected(null); setBill(null); }}>
            <ArrowLeft className="size-4" />
            返回大盘
          </Button>
          <Button variant="outline" size="sm" className="gap-1.5" onClick={exportBill} disabled={!bill}>
            <Download className="size-4" />
            导出 CSV
          </Button>
        </div>

        <div>
          <h2 className="text-lg font-semibold">{selected.name || `租户 #${selected.tenant_id}`}</h2>
          <p className="font-mono text-xs text-muted-foreground">账期口径:Asia/Shanghai · {RANGES.find((r) => r.key === rangeKey)?.label}</p>
        </div>

        {!bill ? (
          <div className="h-40 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />
        ) : (
          <>
            <MetricCardGroup>
              <StatCard accent="neutral" label="期初余额" value={usd(bill.opening)} icon={Wallet} />
              <StatCard accent="emerald" label="充值" value={usd(bill.summary.topup)} icon={Wallet} />
              <StatCard accent="rose" label="消耗" value={usd(bill.summary.settle)} icon={TrendingDown} />
              <StatCard accent="brand" label="期末余额" value={usd(bill.closing)} icon={Scale} />
            </MetricCardGroup>
            <Card className="p-5">
              <p className="mb-3 font-mono text-xs uppercase tracking-wider text-muted-foreground">按天充值 / 消耗</p>
              <BillingTrendChart daily={bill.daily} />
            </Card>
          </>
        )}
      </div>
    );
  }

  // --- platform dashboard ---
  if (error) return <Card className="p-5 text-sm text-muted-foreground">加载失败:{error}</Card>;

  const tenantCols: Column<TenantSpend>[] = [
    { key: "name", header: "租户", cell: (t) => <span className="text-sm">{t.name || `#${t.tenant_id}`}</span> },
    { key: "settle", header: "消耗", align: "right", cell: (t) => <span className="font-mono tabular-nums text-rose-600 dark:text-rose-400">{usd(t.settle)}</span> },
    { key: "topup", header: "充值", align: "right", cell: (t) => <span className="font-mono tabular-nums text-emerald-600 dark:text-emerald-400">{usd(t.topup)}</span> },
  ];

  return (
    <div className="space-y-6">
      <div className="flex justify-end">{rangePicker}</div>

      {!stats ? (
        <div className="h-40 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />
      ) : (
        <>
          <MetricCardGroup>
            <StatCard accent="emerald" label="总充值" value={usd(stats.summary.topup)} icon={Wallet} />
            <StatCard accent="rose" label="总消耗" value={usd(stats.summary.settle)} icon={TrendingDown} />
            <StatCard accent="amber" label="总退款" value={usd(stats.summary.refund)} icon={RotateCcw} />
            <StatCard accent="brand" label="净额" value={usd(stats.summary.net)} sub="充值−消耗+退款+调整" icon={Scale} />
          </MetricCardGroup>

          <Card className="p-5">
            <p className="mb-3 font-mono text-xs uppercase tracking-wider text-muted-foreground">按天充值 / 消耗</p>
            <BillingTrendChart daily={stats.daily} />
          </Card>

          <section className="space-y-2">
            <p className="font-mono text-xs uppercase tracking-wider text-muted-foreground">租户消耗排行 · Top 20 · 点击查看账单</p>
            <ProDataTable
              data={stats.top_tenants}
              columns={tenantCols}
              getRowKey={(t) => t.tenant_id}
              onRowClick={(t) => loadBill(t)}
              emptyState="该账期暂无消耗记录"
            />
          </section>

          <p className="font-mono text-xs text-muted-foreground">金额单位 USD · 数据源 wallet_ledger · 时区 Asia/Shanghai</p>
        </>
      )}
    </div>
  );
}
