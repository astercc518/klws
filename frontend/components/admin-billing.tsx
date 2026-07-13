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
import { useT } from "@/components/locale-provider";

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

const RANGES: { key: string; labelKey: string; days: number }[] = [
  { key: "7", labelKey: "admin.billing.range.7d", days: 7 },
  { key: "30", labelKey: "admin.billing.range.30d", days: 30 },
  { key: "90", labelKey: "admin.billing.range.90d", days: 90 },
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
  const t = useT();
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
        setError(e instanceof ApiError ? e.message : t("admin.billing.loadFailed"));
      }
    }
  }, [days, t]);

  useEffect(() => {
    loadStats();
  }, [loadStats]);

  const loadBill = useCallback(
    async (tenant: TenantSpend) => {
      const { from, to } = rangeParams(days);
      setSelected(tenant);
      setBill(null);
      try {
        setBill(await api.get<Bill>(`/admin/finance/bill?tenant_id=${tenant.tenant_id}&from=${from}&to=${to}`));
      } catch (e) {
        toast.error(t("admin.billing.billLoadFailedTitle"), {
          description: e instanceof ApiError ? e.message : t("admin.billing.retry"),
        });
      }
    },
    [days, t],
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
      toast.error(t("admin.billing.exportFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.billing.retry"),
      });
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
          {t(r.labelKey)}
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
            {t("admin.billing.backToOverview")}
          </Button>
          <Button variant="outline" size="sm" className="gap-1.5" onClick={exportBill} disabled={!bill}>
            <Download className="size-4" />
            {t("admin.billing.exportCsv")}
          </Button>
        </div>

        <div>
          <h2 className="text-lg font-semibold">
            {selected.name || t("admin.billing.tenantFallback").replace("{id}", () => String(selected.tenant_id))}
          </h2>
          <p className="font-mono text-xs text-muted-foreground">
            {t("admin.billing.periodBasisPrefix")}
            {t(RANGES.find((r) => r.key === rangeKey)?.labelKey ?? RANGES[1].labelKey)}
          </p>
        </div>

        {!bill ? (
          <div className="h-40 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />
        ) : (
          <>
            <MetricCardGroup>
              <StatCard accent="neutral" label={t("admin.billing.openingBalance")} value={usd(bill.opening)} icon={Wallet} />
              <StatCard accent="emerald" label={t("admin.billing.topup")} value={usd(bill.summary.topup)} icon={Wallet} />
              <StatCard accent="rose" label={t("admin.billing.settle")} value={usd(bill.summary.settle)} icon={TrendingDown} />
              <StatCard accent="brand" label={t("admin.billing.closingBalance")} value={usd(bill.closing)} icon={Scale} />
            </MetricCardGroup>
            <Card className="p-5">
              <p className="mb-3 font-mono text-xs uppercase tracking-wider text-muted-foreground">{t("admin.billing.dailyTopupSettle")}</p>
              <BillingTrendChart daily={bill.daily} />
            </Card>
          </>
        )}
      </div>
    );
  }

  // --- platform dashboard ---
  if (error)
    return (
      <Card className="p-5 text-sm text-muted-foreground">
        {t("admin.billing.loadFailedPrefix")}
        {error}
      </Card>
    );

  const tenantCols: Column<TenantSpend>[] = [
    { key: "name", header: t("admin.billing.col.tenant"), cell: (row) => <span className="text-sm">{row.name || `#${row.tenant_id}`}</span> },
    { key: "settle", header: t("admin.billing.settle"), align: "right", cell: (row) => <span className="font-mono tabular-nums text-rose-600 dark:text-rose-400">{usd(row.settle)}</span> },
    { key: "topup", header: t("admin.billing.topup"), align: "right", cell: (row) => <span className="font-mono tabular-nums text-emerald-600 dark:text-emerald-400">{usd(row.topup)}</span> },
  ];

  return (
    <div className="space-y-6">
      <div className="flex justify-end">{rangePicker}</div>

      {!stats ? (
        <div className="h-40 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />
      ) : (
        <>
          <MetricCardGroup>
            <StatCard accent="emerald" label={t("admin.billing.totalTopup")} value={usd(stats.summary.topup)} icon={Wallet} />
            <StatCard accent="rose" label={t("admin.billing.totalSettle")} value={usd(stats.summary.settle)} icon={TrendingDown} />
            <StatCard accent="amber" label={t("admin.billing.totalRefund")} value={usd(stats.summary.refund)} icon={RotateCcw} />
            <StatCard accent="brand" label={t("admin.billing.netAmount")} value={usd(stats.summary.net)} sub={t("admin.billing.netFormula")} icon={Scale} />
          </MetricCardGroup>

          <Card className="p-5">
            <p className="mb-3 font-mono text-xs uppercase tracking-wider text-muted-foreground">{t("admin.billing.dailyTopupSettle")}</p>
            <BillingTrendChart daily={stats.daily} />
          </Card>

          <section className="space-y-2">
            <p className="font-mono text-xs uppercase tracking-wider text-muted-foreground">{t("admin.billing.rankingCaption")}</p>
            <ProDataTable
              data={stats.top_tenants}
              columns={tenantCols}
              getRowKey={(row) => row.tenant_id}
              onRowClick={(row) => loadBill(row)}
              emptyState={t("admin.billing.emptyState")}
            />
          </section>

          <p className="font-mono text-xs text-muted-foreground">{t("admin.billing.footnote")}</p>
        </>
      )}
    </div>
  );
}
