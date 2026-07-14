"use client";

import { useCallback, useEffect, useState } from "react";
import { Download } from "lucide-react";
import { AreaChart } from "@tremor/react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { useT } from "@/components/locale-provider";

// ---------------------------------------------------------------------------
// Types — mirror internal/api/metrics_api.go's trendPointView / tenantConsumptionRow
// JSON shapes (P3 task-4/5).
// ---------------------------------------------------------------------------

interface TrendPoint {
  bucket: string;
  value: number;
}

interface ConsumptionRow {
  tenant_id: number;
  tenant_name: string;
  consumption: number;
}

// metrics.metricColumns (internal/metrics/snapshot.go) — the exact whitelist
// GET /admin/reports/trend(.csv) accepts; anything else 400s. Kept in sync by
// hand since the whitelist selects a SQL column name and can't be introspected
// from the frontend.
const METRICS = [
  "accounts_total",
  "accounts_active",
  "accounts_banned",
  "accounts_quarantined",
  "queue_backlog",
  "processed_1h",
  "delivered_1h",
  "failed_1h",
  "avg_delivery_ms",
] as const;
type Metric = (typeof METRICS)[number];

const METRIC_LABEL_KEY: Record<Metric, string> = {
  accounts_total: "admin.reports.metric.accounts_total",
  accounts_active: "admin.reports.metric.accounts_active",
  accounts_banned: "admin.reports.metric.accounts_banned",
  accounts_quarantined: "admin.reports.metric.accounts_quarantined",
  queue_backlog: "admin.reports.metric.queue_backlog",
  processed_1h: "admin.reports.metric.processed_1h",
  delivered_1h: "admin.reports.metric.delivered_1h",
  failed_1h: "admin.reports.metric.failed_1h",
  avg_delivery_ms: "admin.reports.metric.avg_delivery_ms",
};

// reportsBucketWhitelist (internal/api/metrics_api.go) — hour|day|week only,
// no "month". Exposed as-is rather than mapped from a 日/周/月 picker so the
// UI never offers a value the backend would reject.
const BUCKETS = ["hour", "day", "week"] as const;
type Bucket = (typeof BUCKETS)[number];

const BUCKET_LABEL_KEY: Record<Bucket, string> = {
  hour: "admin.reports.bucket.hour",
  day: "admin.reports.bucket.day",
  week: "admin.reports.bucket.week",
};

const RANGES: { key: string; labelKey: string; days: number }[] = [
  { key: "7", labelKey: "admin.reports.range.7d", days: 7 },
  { key: "30", labelKey: "admin.reports.range.30d", days: 30 },
  { key: "90", labelKey: "admin.reports.range.90d", days: 90 },
];

const PAGE_SIZE = 20;

const nf = new Intl.NumberFormat("en-US");
const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

function formatMetricValue(metric: Metric, value: number): string {
  if (metric === "avg_delivery_ms") {
    return value < 1000 ? `${nf.format(Math.round(value))}ms` : `${(value / 1000).toFixed(1)}s`;
  }
  return nf.format(Math.round(value));
}

// bucket is an RFC3339 timestamp string (e.g. "2026-07-14T15:00:00+08:00");
// sliced directly rather than parsed into a Date to sidestep browser-timezone
// hydration mismatches, same convention as admin-risk-monitor.tsx's TrendChart.
function formatBucketLabel(bucket: string, granularity: Bucket): string {
  if (granularity === "hour") return `${bucket.slice(5, 10)} ${bucket.slice(11, 16)}`;
  return bucket.slice(5, 10);
}

// Compute a YYYY-MM-DD pair for the last N days ending today (client-local),
// same helper shape as admin-billing.tsx's rangeParams.
function rangeParams(days: number): { from: string; to: string } {
  const now = new Date();
  const to = now.toISOString().slice(0, 10);
  const fromDate = new Date(now.getTime() - (days - 1) * 86400000);
  const from = fromDate.toISOString().slice(0, 10);
  return { from, to };
}

export function AdminReports() {
  const t = useT();

  const [metric, setMetric] = useState<Metric>("delivered_1h");
  const [bucket, setBucket] = useState<Bucket>("day");
  const [rangeKey, setRangeKey] = useState("30");
  const days = RANGES.find((r) => r.key === rangeKey)?.days ?? 30;

  // ---- Trend ----
  const [trend, setTrend] = useState<TrendPoint[] | null>(null);
  const [trendError, setTrendError] = useState<string | null>(null);

  const loadTrend = useCallback(async () => {
    const { from, to } = rangeParams(days);
    const params = new URLSearchParams({ metric, bucket, from, to });
    try {
      const d = await api.get<TrendPoint[]>(`/admin/reports/trend?${params.toString()}`);
      setTrend(d);
      setTrendError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setTrendError(e instanceof ApiError ? e.message : t("admin.reports.trend.loadFailed"));
      }
    }
  }, [metric, bucket, days, t]);

  useEffect(() => {
    loadTrend();
  }, [loadTrend]);

  async function exportTrend() {
    const { from, to } = rangeParams(days);
    const params = new URLSearchParams({ metric, bucket, from, to });
    try {
      await api.download(`/admin/reports/trend.csv?${params.toString()}`, "trend.csv");
    } catch (e) {
      toast.error(t("admin.reports.exportFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.billing.retry"),
      });
    }
  }

  // ---- Tenant consumption ranking (server-driven ProDataTable) ----
  const [rows, setRows] = useState<ConsumptionRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [consumptionError, setConsumptionError] = useState<string | null>(null);
  const [consumptionLoading, setConsumptionLoading] = useState(false);

  const loadConsumption = useCallback(async () => {
    const { from, to } = rangeParams(days);
    const params = new URLSearchParams({
      from,
      to,
      limit: String(PAGE_SIZE),
      offset: String(page * PAGE_SIZE),
    });
    setConsumptionLoading(true);
    try {
      const d = await api.get<{ rows: ConsumptionRow[]; total: number }>(
        `/admin/reports/tenant-consumption?${params.toString()}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setConsumptionError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setConsumptionError(e instanceof ApiError ? e.message : t("admin.reports.consumption.loadFailed"));
      }
    } finally {
      setConsumptionLoading(false);
    }
  }, [days, page, t]);

  useEffect(() => {
    loadConsumption();
  }, [loadConsumption]);

  async function exportConsumption() {
    const { from, to } = rangeParams(days);
    const params = new URLSearchParams({ from, to });
    try {
      await api.download(`/admin/reports/tenant-consumption.csv?${params.toString()}`, "tenant-consumption.csv");
    } catch (e) {
      toast.error(t("admin.reports.exportFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.billing.retry"),
      });
    }
  }

  const rangePicker = (
    <div className="inline-flex rounded-lg border bg-muted/40 p-0.5">
      {RANGES.map((r) => (
        <button
          key={r.key}
          onClick={() => {
            setRangeKey(r.key);
            setPage(0);
          }}
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

  const consumptionCols: Column<ConsumptionRow>[] = [
    {
      key: "tenant",
      header: t("admin.reports.consumption.col.tenant"),
      cell: (row) => <span className="text-sm">{row.tenant_name || `#${row.tenant_id}`}</span>,
    },
    {
      key: "consumption",
      header: t("admin.reports.consumption.col.consumption"),
      align: "right",
      cell: (row) => <span className="font-mono tabular-nums">{usd(row.consumption)}</span>,
    },
  ];

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <select
            value={metric}
            onChange={(e) => setMetric(e.target.value as Metric)}
            className="h-8 rounded-md border bg-transparent px-2 text-sm"
            aria-label={t("admin.reports.metricSelectAria")}
          >
            {METRICS.map((m) => (
              <option key={m} value={m}>
                {t(METRIC_LABEL_KEY[m])}
              </option>
            ))}
          </select>
          <select
            value={bucket}
            onChange={(e) => setBucket(e.target.value as Bucket)}
            className="h-8 rounded-md border bg-transparent px-2 text-sm"
            aria-label={t("admin.reports.bucketSelectAria")}
          >
            {BUCKETS.map((b) => (
              <option key={b} value={b}>
                {t(BUCKET_LABEL_KEY[b])}
              </option>
            ))}
          </select>
        </div>
        {rangePicker}
      </div>

      <Card className="p-5">
        <div className="flex items-center justify-between gap-3">
          <p className="font-mono text-xs uppercase tracking-wider text-muted-foreground">
            {t("admin.reports.trend.title")}
          </p>
          <Button variant="outline" size="sm" className="gap-1.5" onClick={exportTrend}>
            <Download className="size-4" />
            {t("admin.reports.exportTrendCsv")}
          </Button>
        </div>
        <div className="mt-3">
          <TrendChart trend={trend} error={trendError} metric={metric} bucket={bucket} />
        </div>
      </Card>

      <section className="space-y-2">
        <div className="flex items-center justify-between gap-3">
          <p className="font-mono text-xs uppercase tracking-wider text-muted-foreground">
            {t("admin.reports.consumption.title")}
          </p>
          <Button variant="outline" size="sm" className="gap-1.5" onClick={exportConsumption}>
            <Download className="size-4" />
            {t("admin.reports.exportConsumptionCsv")}
          </Button>
        </div>
        <ProDataTable
          data={rows}
          error={consumptionError}
          columns={consumptionCols}
          getRowKey={(row) => row.tenant_id}
          emptyState={t("admin.reports.consumption.emptyState")}
          server={{
            total,
            page,
            pageSize: PAGE_SIZE,
            onPageChange: setPage,
            query: "",
            onQueryChange: () => {},
            loading: consumptionLoading,
          }}
        />
      </section>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Trend chart. metric_snapshots starts empty on a fresh instance (the sampler
// only backfills going forward), so an empty `trend` array is a normal
// early-life state, not an error — same empty-state convention as
// admin-risk-monitor.tsx's TrendChart.
// ---------------------------------------------------------------------------
function TrendChart({
  trend,
  error,
  metric,
  bucket,
}: {
  trend: TrendPoint[] | null;
  error: string | null;
  metric: Metric;
  bucket: Bucket;
}) {
  const t = useT();
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  if (error) {
    return <div className="flex h-64 items-center justify-center text-sm text-muted-foreground">{error}</div>;
  }
  if (trend === null || !mounted) {
    return <div className="h-64 w-full animate-pulse rounded-md bg-muted motion-reduce:animate-none" />;
  }
  if (trend.length === 0) {
    return (
      <div className="flex h-64 flex-col items-center justify-center gap-1 text-center text-sm text-muted-foreground">
        <span>{t("admin.reports.trend.emptyTitle")}</span>
        <span className="text-xs">{t("admin.reports.trend.emptyHint")}</span>
      </div>
    );
  }

  const seriesLabel = t(METRIC_LABEL_KEY[metric]);
  const data = trend.map((p) => ({
    bucket: formatBucketLabel(p.bucket, bucket),
    [seriesLabel]: p.value,
  }));

  return (
    <AreaChart
      className="h-64"
      data={data}
      index="bucket"
      categories={[seriesLabel]}
      colors={["cyan"]}
      valueFormatter={(v) => formatMetricValue(metric, v)}
      showLegend={false}
      showGradient
      curveType="monotone"
      yAxisWidth={56}
      showAnimation
    />
  );
}
