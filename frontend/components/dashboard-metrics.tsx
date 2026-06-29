"use client";

import { useEffect, useState } from "react";
import { Wallet, Snowflake, Smartphone, Rocket } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { MetricCard } from "@/components/metric-card";
import { Card } from "@/components/ui/card";

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
          setError(e instanceof ApiError ? e.message : "加载失败");
        }
      });
    return () => {
      alive = false;
    };
  }, []);

  if (error) {
    return (
      <Card className="p-5 text-sm text-muted-foreground">
        指标加载失败:{error}
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
      <MetricCard hero label="可用余额" value={usd(stats.balance)} sub="可用于发送 · USD" icon={Wallet} />
      <MetricCard label="冻结金额" value={usd(stats.frozen)} sub="进行中活动占用" icon={Snowflake} />
      <MetricCard
        label="在线 WA 账号"
        value={`${nf.format(stats.accounts_online)} / ${nf.format(stats.accounts_total)}`}
        sub={`${nf.format(stats.accounts_total - stats.accounts_online)} 个不可用`}
        icon={Smartphone}
        status={OnlineDot}
      />
      <MetricCard label="今日发送总量" value={nf.format(stats.sent_today)} sub="今日已送达" icon={Rocket} />
    </div>
  );
}
