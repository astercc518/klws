"use client";

import { useEffect, useState } from "react";
import { Banknote, Smartphone, ListChecks, ShieldAlert } from "lucide-react";
import { AreaChart } from "@tremor/react";
import { api, ApiError } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { StatCard, MetricCardGroup, type Trend } from "@/components/admin/stat-card";
import { useT } from "@/components/locale-provider";

interface AdminStats {
  total_topup: number;
  total_balance: number;
  total_frozen: number;
  consumed: number; // settled spend = platform revenue (smallest unit)
  accounts_total: number;
  accounts_active: number;
  accounts_banned: number;
  ban_rate: number; // 0..1
  queue_backlog: number;
}

interface TrendPoint {
  bucket: string;
  value: number;
}

const TREND_DAYS = 7;

const usd = (smallest: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(smallest / 100);
const nf = new Intl.NumberFormat("en-US");
const pct = (r: number) => `${(r * 100).toFixed(1)}%`;

/** Utilization shown as a level indicator (flat — it's a ratio, not a delta). */
function activeTrend(active: number, total: number): Trend | undefined {
  if (total === 0) return undefined;
  return { value: pct(active / total), direction: "flat", intent: "neutral" };
}

/** Ban rate colored by risk: green when contained, red once it climbs. */
function banTrend(rate: number): Trend {
  const intent = rate >= 0.05 ? "negative" : rate < 0.02 ? "positive" : "neutral";
  return { value: pct(rate), direction: "flat", intent };
}

export function AdminMetrics() {
  const t = useT();
  const [stats, setStats] = useState<AdminStats | null>(null);
  const [error, setError] = useState<string | null>(null);

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
            setError(e instanceof ApiError ? e.message : t("admin.metrics.loadFailedGeneric"));
          }
        });
    };

    fetchStats(false);
    const id = setInterval(() => fetchStats(true), 15_000);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [t]);

  // ---- Trend (last 7 days, day bucket, delivered_1h) — reads P3's
  // metric_snapshots history via GET /admin/reports/trend, same endpoint and
  // empty-state convention as admin-risk-monitor.tsx's ban-rate trend.
  const [trend, setTrend] = useState<TrendPoint[] | null>(null);
  const [trendError, setTrendError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const to = new Date();
    const from = new Date(to.getTime() - TREND_DAYS * 86_400_000);
    const params = new URLSearchParams({
      metric: "delivered_1h",
      bucket: "day",
      from: from.toISOString(),
      to: to.toISOString(),
    });
    api
      .get<TrendPoint[]>(`/admin/reports/trend?${params.toString()}`)
      .then((d) => {
        if (alive) setTrend(d);
      })
      .catch((e) => {
        if (!alive) return;
        if (!(e instanceof ApiError && e.status === 401)) {
          setTrendError(e instanceof ApiError ? e.message : t("admin.metrics.trend.loadFailed"));
        }
      });
    return () => {
      alive = false;
    };
  }, [t]);

  if (error) {
    return (
      <Card className="p-5 text-sm text-muted-foreground">
        {t("admin.metrics.overviewLoadFailedPrefix")}
        {error}
      </Card>
    );
  }
  if (!stats) {
    return (
      <MetricCardGroup>
        {Array.from({ length: 4 }).map((_, i) => (
          <div
            key={i}
            className="h-[132px] animate-pulse rounded-xl bg-muted motion-reduce:animate-none"
          />
        ))}
      </MetricCardGroup>
    );
  }

  return (
    <div className="space-y-6">
      <MetricCardGroup>
        <StatCard
          accent="brand"
          label={t("admin.metrics.totalConsumedRevenue")}
          value={usd(stats.consumed)}
          sub={`${t("admin.metrics.cumulativeTopupPrefix")}${usd(stats.total_topup)}`}
          icon={Banknote}
          href="/admin/ledger"
        />
        <StatCard
          accent="emerald"
          label={t("admin.metrics.activeWaAccounts")}
          value={`${nf.format(stats.accounts_active)} / ${nf.format(stats.accounts_total)}`}
          sub={`${t("admin.metrics.bannedFlaggedPrefix")}${nf.format(stats.accounts_banned)}`}
          icon={Smartphone}
          trend={activeTrend(stats.accounts_active, stats.accounts_total)}
          href="/admin/devices"
        />
        <StatCard
          accent="blue"
          label={t("admin.metrics.queueBacklog")}
          value={nf.format(stats.queue_backlog)}
          sub={t("admin.metrics.pendingRecipients")}
          icon={ListChecks}
          href="/admin/audit"
        />
        <StatCard
          accent="rose"
          label={t("admin.metrics.banRate")}
          value={pct(stats.ban_rate)}
          sub={t("admin.metrics.bannedFlaggedOverTotal")}
          icon={ShieldAlert}
          trend={banTrend(stats.ban_rate)}
          href="/admin/devices"
        />
      </MetricCardGroup>

      <Card className="p-5">
        <p className="mb-3 font-mono text-xs uppercase tracking-wider text-muted-foreground">
          {t("admin.metrics.trend.title")}
        </p>
        <OverviewTrendChart trend={trend} error={trendError} />
      </Card>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Delivery-volume trend mini-chart. metric_snapshots starts empty on a fresh
// instance (the sampler only backfills going forward), so an empty `trend`
// array is a normal early-life state, not an error — same empty-state
// convention as admin-risk-monitor.tsx's TrendChart.
// ---------------------------------------------------------------------------
function OverviewTrendChart({ trend, error }: { trend: TrendPoint[] | null; error: string | null }) {
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
        <span>{t("admin.metrics.trend.emptyTitle")}</span>
        <span className="text-xs">{t("admin.metrics.trend.emptyHint")}</span>
      </div>
    );
  }

  const deliveredLabel = t("admin.metrics.trend.seriesLabel");
  const data = trend.map((p) => ({ date: p.bucket.slice(5, 10), [deliveredLabel]: p.value }));

  return (
    <AreaChart
      className="h-64"
      data={data}
      index="date"
      categories={[deliveredLabel]}
      colors={["cyan"]}
      valueFormatter={(v) => nf.format(v)}
      showLegend={false}
      showGradient
      curveType="monotone"
      yAxisWidth={48}
      showAnimation
    />
  );
}
