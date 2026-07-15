"use client";

import { useCallback, useEffect, useState } from "react";
import { ShieldAlert, Smartphone, ShieldOff, CheckCircle2, Timer, ListChecks } from "lucide-react";
import { AreaChart, DonutChart } from "@tremor/react";
import { api, ApiError } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { StatCard, MetricCardGroup, type Trend } from "@/components/admin/stat-card";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatusBadge, type StatusTone } from "@/components/admin/status-badge";
import { useT } from "@/components/locale-provider";

// ---------------------------------------------------------------------------
// Types — mirror internal/api/metrics_api.go's riskOverview / riskAccountRow /
// trendPointView JSON shapes (P3 task-3/4).
// ---------------------------------------------------------------------------

interface RiskOverview {
  ban_rate: number;
  accounts_total: number;
  accounts_active: number;
  accounts_quarantined: number;
  health_buckets: { low: number; mid: number; high: number };
  queue_backlog: number;
  delivered_rate_1h: number;
  failed_rate_1h: number;
  avg_delivery_ms: number | null;
  processed_1h: number;
}

type Reason = "ban" | "quarantine" | "low_health";
type RiskAccountStats = Partial<Record<Reason, number>>;

interface RiskAccountRow {
  jid: string;
  tenant_id: number;
  tenant_name: string;
  ban_status: string;
  health_score: number;
  quarantined_until: string | null;
  reason: string;
}

interface TrendPoint {
  bucket: string;
  value: number;
}

const PAGE_SIZE = 20;
const TREND_DAYS = 7;

const nf = new Intl.NumberFormat("en-US");
const pct = (r: number) => `${(r * 100).toFixed(1)}%`;

/** Ban rate colored by risk, same convention as admin-metrics.tsx's banTrend. */
function banRateTrend(rate: number): Trend {
  const intent = rate >= 0.05 ? "negative" : rate < 0.02 ? "positive" : "neutral";
  return { value: pct(rate), direction: "flat", intent };
}

function formatDelayMs(ms: number | null): string {
  if (ms == null) return "—";
  return ms < 1000 ? `${nf.format(ms)}ms` : `${(ms / 1000).toFixed(1)}s`;
}

const REASON_TABS: { key: "" | Reason; labelKey: string }[] = [
  { key: "", labelKey: "admin.risk.monitor.reason.all" },
  { key: "ban", labelKey: "admin.risk.monitor.reason.ban" },
  { key: "quarantine", labelKey: "admin.risk.monitor.reason.quarantine" },
  { key: "low_health", labelKey: "admin.risk.monitor.reason.lowHealth" },
];

// account_devices.ban_status is a DB enum (active/banned/flagged/logged_out),
// but this endpoint has no application-layer guarantee against future values
// reaching the client untranslated — fall back to the raw string + neutral
// tone rather than throwing/blanking, same pattern as P2 admin-instances.
const BAN_STATUS_TONE: Record<string, StatusTone> = {
  active: "positive",
  banned: "negative",
  flagged: "warning",
  logged_out: "neutral",
};
const BAN_STATUS_LABEL_KEY: Record<string, string> = {
  active: "admin.risk.monitor.banStatus.active",
  banned: "admin.risk.monitor.banStatus.banned",
  flagged: "admin.risk.monitor.banStatus.flagged",
  logged_out: "admin.risk.monitor.banStatus.loggedOut",
};

// reason is server-derived (CASE ban > quarantine > low_health) but treated
// the same defensive way as ban_status above.
const REASON_TONE: Record<string, StatusTone> = {
  ban: "negative",
  quarantine: "warning",
  low_health: "warning",
};
const REASON_LABEL_KEY: Record<string, string> = {
  ban: "admin.risk.monitor.reason.ban",
  quarantine: "admin.risk.monitor.reason.quarantine",
  low_health: "admin.risk.monitor.reason.lowHealth",
};

export function AdminRiskMonitor() {
  const t = useT();

  // ---- Overview (top stat cards + health distribution) ----
  const [overview, setOverview] = useState<RiskOverview | null>(null);
  const [overviewError, setOverviewError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const fetchOverview = (isRefresh: boolean) => {
      api
        .get<RiskOverview>("/admin/risk/overview")
        .then((d) => {
          if (alive) setOverview(d);
        })
        .catch((e) => {
          if (!alive) return;
          if (!isRefresh && !(e instanceof ApiError && e.status === 401)) {
            setOverviewError(e instanceof ApiError ? e.message : t("admin.risk.monitor.loadFailedGeneric"));
          }
        });
    };
    fetchOverview(false);
    const id = setInterval(() => fetchOverview(true), 15_000);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [t]);

  // ---- Trend (last 7 days, day bucket, accounts_banned) ----
  const [trend, setTrend] = useState<TrendPoint[] | null>(null);
  const [trendError, setTrendError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const to = new Date();
    const from = new Date(to.getTime() - TREND_DAYS * 86_400_000);
    const params = new URLSearchParams({
      metric: "accounts_banned",
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
          setTrendError(e instanceof ApiError ? e.message : t("admin.risk.monitor.trend.loadFailed"));
        }
      });
    return () => {
      alive = false;
    };
  }, [t]);

  // ---- Anomalous accounts table (server-driven ProDataTable) ----
  const [rows, setRows] = useState<RiskAccountRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<RiskAccountStats | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [reason, setReason] = useState<"" | Reason>("");
  const [tableError, setTableError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const loadAccounts = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (reason) params.set("reason", reason);
    try {
      const d = await api.get<{ rows: RiskAccountRow[]; total: number; stats: RiskAccountStats }>(
        `/admin/risk/accounts?${params.toString()}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setStats(d.stats);
      setTableError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setTableError(e instanceof ApiError ? e.message : t("admin.risk.monitor.table.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, reason, t]);

  useEffect(() => {
    loadAccounts();
  }, [loadAccounts]);

  const tenantLabel = (r: RiskAccountRow) => r.tenant_name || `#${r.tenant_id}`;
  const reasonCount = (k: Reason) => stats?.[k] ?? 0;
  const statsTotal = stats ? Object.values(stats).reduce((a, b) => a + (b ?? 0), 0) : 0;

  const columns: Column<RiskAccountRow>[] = [
    {
      key: "jid",
      header: t("admin.risk.monitor.table.col.jid"),
      title: t("admin.risk.monitor.table.col.jid"),
      hideable: false,
      cell: (r) => <span className="font-mono text-xs">{r.jid || "—"}</span>,
    },
    {
      key: "tenant",
      header: t("admin.risk.monitor.table.col.tenant"),
      title: t("admin.risk.monitor.table.col.tenant"),
      cell: (r) => <span className="text-sm text-muted-foreground">{tenantLabel(r)}</span>,
    },
    {
      key: "ban_status",
      header: t("admin.risk.monitor.table.col.banStatus"),
      title: t("admin.risk.monitor.table.col.banStatus"),
      cell: (r) => {
        const labelKey = BAN_STATUS_LABEL_KEY[r.ban_status];
        return (
          <StatusBadge tone={BAN_STATUS_TONE[r.ban_status] ?? "neutral"}>
            {labelKey ? t(labelKey) : r.ban_status}
          </StatusBadge>
        );
      },
    },
    {
      key: "health_score",
      header: t("admin.risk.monitor.table.col.healthScore"),
      title: t("admin.risk.monitor.table.col.healthScore"),
      cell: (r) => <span className="font-mono text-xs tabular-nums">{r.health_score}</span>,
    },
    {
      key: "quarantined_until",
      header: t("admin.risk.monitor.table.col.quarantinedUntil"),
      title: t("admin.risk.monitor.table.col.quarantinedUntil"),
      cell: (r) => (
        <span className="font-mono text-xs text-muted-foreground">{r.quarantined_until ?? "—"}</span>
      ),
    },
    {
      key: "reason",
      header: t("admin.risk.monitor.table.col.reason"),
      title: t("admin.risk.monitor.table.col.reason"),
      cell: (r) => {
        const labelKey = REASON_LABEL_KEY[r.reason];
        return (
          <StatusBadge tone={REASON_TONE[r.reason] ?? "neutral"}>
            {labelKey ? t(labelKey) : r.reason}
          </StatusBadge>
        );
      },
    },
  ];

  return (
    <div className="space-y-6">
      {overviewError ? (
        <Card className="p-5 text-sm text-muted-foreground">
          {t("admin.risk.monitor.overview.loadFailedPrefix")}
          {overviewError}
        </Card>
      ) : !overview ? (
        <MetricCardGroup columns={3}>
          {Array.from({ length: 6 }).map((_, i) => (
            <div
              key={i}
              className="h-[132px] animate-pulse rounded-xl bg-muted motion-reduce:animate-none"
            />
          ))}
        </MetricCardGroup>
      ) : (
        <MetricCardGroup columns={3}>
          <StatCard
            accent="rose"
            label={t("admin.risk.monitor.stat.banRate")}
            value={pct(overview.ban_rate)}
            sub={t("admin.risk.monitor.stat.banRateSub")}
            icon={ShieldAlert}
            trend={banRateTrend(overview.ban_rate)}
          />
          <StatCard
            accent="emerald"
            label={t("admin.risk.monitor.stat.active")}
            value={`${nf.format(overview.accounts_active)} / ${nf.format(overview.accounts_total)}`}
            sub={t("admin.risk.monitor.stat.activeSub")}
            icon={Smartphone}
          />
          <StatCard
            accent="amber"
            label={t("admin.risk.monitor.stat.quarantined")}
            value={nf.format(overview.accounts_quarantined)}
            sub={t("admin.risk.monitor.stat.quarantinedSub")}
            icon={ShieldOff}
          />
          <StatCard
            accent="blue"
            label={t("admin.risk.monitor.stat.deliveredRate")}
            value={pct(overview.delivered_rate_1h)}
            sub={t("admin.risk.monitor.stat.deliveredRateSub")
              .replace("{n}", () => nf.format(overview.processed_1h))
              .replace("{f}", () => pct(overview.failed_rate_1h))}
            icon={CheckCircle2}
          />
          <StatCard
            accent="neutral"
            label={t("admin.risk.monitor.stat.avgDelay")}
            value={formatDelayMs(overview.avg_delivery_ms)}
            sub={t("admin.risk.monitor.stat.avgDelaySub")}
            icon={Timer}
          />
          <StatCard
            accent="neutral"
            label={t("admin.risk.monitor.stat.queueBacklog")}
            value={nf.format(overview.queue_backlog)}
            sub={t("admin.risk.monitor.stat.queueBacklogSub")}
            icon={ListChecks}
          />
        </MetricCardGroup>
      )}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card className="p-5">
          <p className="mb-3 font-mono text-xs uppercase tracking-wider text-muted-foreground">
            {t("admin.risk.monitor.healthChart.title")}
          </p>
          <HealthBucketsChart overview={overview} />
        </Card>
        <Card className="p-5">
          <p className="mb-3 font-mono text-xs uppercase tracking-wider text-muted-foreground">
            {t("admin.risk.monitor.trend.title")}
          </p>
          <TrendChart trend={trend} error={trendError} />
        </Card>
      </div>

      <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
        {REASON_TABS.map((tab) => (
          <button
            key={tab.key || "all"}
            onClick={() => {
              setReason(tab.key);
              setPage(0);
            }}
            className={
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
              (reason === tab.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
            }
          >
            {t(tab.labelKey)}
            {stats && ` (${tab.key ? reasonCount(tab.key) : statsTotal})`}
          </button>
        ))}
      </div>

      <ProDataTable
        data={rows}
        error={tableError}
        columns={columns}
        getRowKey={(r) => r.jid}
        storageKey="admin-risk-monitor"
        emptyState={t("admin.risk.monitor.table.emptyState")}
        search={{ placeholder: t("admin.risk.monitor.table.searchPlaceholder"), accessor: () => "" }}
        server={{
          total,
          page,
          pageSize: PAGE_SIZE,
          onPageChange: setPage,
          query,
          onQueryChange: (q) => {
            setQuery(q);
            setPage(0);
          },
          loading,
        }}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Health-score distribution donut. colors map directly to risk semantics —
// low=rose (bad), mid=amber (caution), high=emerald (healthy) — and must stay
// in the amber/rose/emerald @source inline allowlist in app/globals.css (see
// P1-T4) since Tremor generates these class names at runtime, not statically.
// ---------------------------------------------------------------------------
function HealthBucketsChart({ overview }: { overview: RiskOverview | null }) {
  const t = useT();
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  if (!overview || !mounted) {
    return <div className="h-64 w-full animate-pulse rounded-md bg-muted motion-reduce:animate-none" />;
  }

  const data = [
    { bucket: t("admin.risk.monitor.healthChart.low"), value: overview.health_buckets.low },
    { bucket: t("admin.risk.monitor.healthChart.mid"), value: overview.health_buckets.mid },
    { bucket: t("admin.risk.monitor.healthChart.high"), value: overview.health_buckets.high },
  ];

  return (
    <DonutChart
      className="h-64"
      data={data}
      category="value"
      index="bucket"
      colors={["rose", "amber", "emerald"]}
      valueFormatter={(v) => nf.format(v)}
      showAnimation
    />
  );
}

// ---------------------------------------------------------------------------
// Trend mini-chart. This instance's metric_snapshots table starts empty (the
// sampler only backfills going forward), so an empty `trend` array is a normal
// early-life state, not an error — render a friendly notice instead of a bare
// empty chart canvas.
// ---------------------------------------------------------------------------
function TrendChart({ trend, error }: { trend: TrendPoint[] | null; error: string | null }) {
  const t = useT();
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  if (error) {
    return (
      <div className="flex h-64 items-center justify-center text-sm text-muted-foreground">{error}</div>
    );
  }
  if (trend === null || !mounted) {
    return <div className="h-64 w-full animate-pulse rounded-md bg-muted motion-reduce:animate-none" />;
  }
  if (trend.length === 0) {
    return (
      <div className="flex h-64 flex-col items-center justify-center gap-1 text-center text-sm text-muted-foreground">
        <span>{t("admin.risk.monitor.trend.emptyTitle")}</span>
        <span className="text-xs">{t("admin.risk.monitor.trend.emptyHint")}</span>
      </div>
    );
  }

  const bannedLabel = t("admin.risk.monitor.trend.bannedSeries");
  const data = trend.map((p) => ({ date: p.bucket.slice(5, 10), [bannedLabel]: p.value }));

  return (
    <AreaChart
      className="h-64"
      data={data}
      index="date"
      categories={[bannedLabel]}
      colors={["rose"]}
      valueFormatter={(v) => nf.format(v)}
      showLegend={false}
      showGradient
      curveType="monotone"
      yAxisWidth={48}
      showAnimation
    />
  );
}
