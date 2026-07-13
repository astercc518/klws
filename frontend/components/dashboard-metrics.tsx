"use client";

import { useEffect, useState } from "react";
import { Wallet, Snowflake, Smartphone, Rocket } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { MetricCard } from "@/components/metric-card";
import { Card } from "@/components/ui/card";
import { useT } from "@/components/locale-provider";

interface Stats {
  balance: number; // smallest currency unit (e.g. cents)
  frozen: number;
  accounts_online: number;
  accounts_total: number;
  sent_today: number;
}

const usd = (smallest: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(smallest / 100);
const nf = new Intl.NumberFormat("en-US");

const OnlineDot = (
  <span className="relative flex size-2">
    <span className="absolute inline-flex size-full animate-ping rounded-full bg-emerald-500 opacity-75 motion-reduce:animate-none" />
    <span className="relative inline-flex size-2 rounded-full bg-emerald-500" />
  </span>
);

export function DashboardMetrics() {
  const t = useT();
  const [stats, setStats] = useState<Stats | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    api
      .get<Stats>("/tenant/stats")
      .then((d) => alive && setStats(d))
      .catch((e) => {
        // 401 is handled globally (redirect to /login); surface anything else.
        if (alive && !(e instanceof ApiError && e.status === 401)) {
          setError(e instanceof ApiError ? e.message : t("dash.metrics.loadFailed"));
        }
      });
    return () => {
      alive = false;
    };
  }, [t]);

  if (error) {
    return (
      <Card className="p-5 text-sm text-muted-foreground">
        {t("dash.metrics.loadFailedPrefix")}{error}
      </Card>
    );
  }

  if (!stats) {
    return (
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <div key={i} className="h-[120px] animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />
        ))}
      </div>
    );
  }

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <MetricCard hero label={t("dash.metrics.balance.label")} value={usd(stats.balance)} sub={t("dash.metrics.balance.sub")} icon={Wallet} />
      <MetricCard label={t("dash.metrics.frozen.label")} value={usd(stats.frozen)} sub={t("dash.metrics.frozen.sub")} icon={Snowflake} />
      <MetricCard
        label={t("dash.metrics.online.label")}
        value={`${nf.format(stats.accounts_online)} / ${nf.format(stats.accounts_total)}`}
        sub={t("dash.metrics.online.sub").replace("{n}", () => nf.format(stats.accounts_total - stats.accounts_online))}
        icon={Smartphone}
        status={OnlineDot}
      />
      <MetricCard label={t("dash.metrics.sentToday.label")} value={nf.format(stats.sent_today)} sub={t("dash.metrics.sentToday.sub")} icon={Rocket} />
    </div>
  );
}
