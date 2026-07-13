"use client";

import { useEffect, useState } from "react";
import { Banknote, Smartphone, ListChecks, ShieldAlert } from "lucide-react";
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
  );
}
