"use client";

import { useEffect, useState } from "react";
import { AreaChart } from "@tremor/react";

export interface DailyPoint {
  day: string;
  topup: number;
  settle: number;
  refund: number;
}

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

export function BillingTrendChart({ daily }: { daily: DailyPoint[] }) {
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  if (!mounted) {
    return <div className="h-72 w-full animate-pulse rounded-md bg-muted motion-reduce:animate-none" />;
  }

  const data = daily.map((d) => ({ date: d.day.slice(5), 充值: d.topup, 消耗: d.settle }));
  return (
    <AreaChart
      className="h-72"
      data={data}
      index="date"
      categories={["充值", "消耗"]}
      colors={["emerald", "rose"]}
      valueFormatter={usd}
      showLegend
      showGradient
      curveType="monotone"
      yAxisWidth={64}
      showAnimation
    />
  );
}
