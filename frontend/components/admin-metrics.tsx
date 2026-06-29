"use client";

import { useEffect, useState } from "react";
import { Banknote, Smartphone, ListChecks, ShieldAlert } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { StatCard, MetricCardGroup, type Trend } from "@/components/admin/stat-card";

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
  const [stats, setStats] = useState<AdminStats | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    api
      .get<AdminStats>("/admin/stats")
      .then((d) => alive && setStats(d))
      .catch((e) => {
        if (alive && !(e instanceof ApiError && e.status === 401)) {
          setError(e instanceof ApiError ? e.message : "加载失败");
        }
      });
    return () => {
      alive = false;
    };
  }, []);

  if (error) {
    return <Card className="p-5 text-sm text-muted-foreground">大盘加载失败:{error}</Card>;
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
        label="平台总消耗 / 收入"
        value={usd(stats.consumed)}
        sub={`累计充值 ${usd(stats.total_topup)}`}
        icon={Banknote}
      />
      <StatCard
        accent="emerald"
        label="活跃 WA 账号"
        value={`${nf.format(stats.accounts_active)} / ${nf.format(stats.accounts_total)}`}
        sub={`封禁/标记 ${nf.format(stats.accounts_banned)}`}
        icon={Smartphone}
        trend={activeTrend(stats.accounts_active, stats.accounts_total)}
      />
      <StatCard
        accent="blue"
        label="队列积压任务"
        value={nf.format(stats.queue_backlog)}
        sub="待发送收件人"
        icon={ListChecks}
      />
      <StatCard
        accent="rose"
        label="风控封号率"
        value={pct(stats.ban_rate)}
        sub="banned+flagged / 总账号"
        icon={ShieldAlert}
        trend={banTrend(stats.ban_rate)}
      />
    </MetricCardGroup>
  );
}
