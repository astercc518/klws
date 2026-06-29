"use client";

import { useEffect, useState } from "react";
import { AreaChart } from "@tremor/react";

// Mock 7-day send telemetry until /campaigns aggregation is wired.
const DATA = [
  { date: "06/20", 成功: 18420, 失败: 240 },
  { date: "06/21", 成功: 22110, 失败: 310 },
  { date: "06/22", 成功: 19880, 失败: 195 },
  { date: "06/23", 成功: 26540, 失败: 420 },
  { date: "06/24", 成功: 31200, 失败: 380 },
  { date: "06/25", 成功: 28760, 失败: 290 },
  { date: "06/26", 成功: 33980, 失败: 510 },
];

const nf = new Intl.NumberFormat("en-US");

export function SendTrendChart() {
  // Recharts measures container width on the client; render after mount so SSR
  // doesn't emit a zero-width chart (and to avoid hydration width warnings).
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  if (!mounted) {
    return <div className="h-72 w-full animate-pulse rounded-md bg-muted motion-reduce:animate-none" />;
  }

  return (
    <AreaChart
      className="h-72"
      data={DATA}
      index="date"
      categories={["成功", "失败"]}
      colors={["emerald", "rose"]}
      valueFormatter={(v) => nf.format(v)}
      showLegend
      showGradient
      curveType="monotone"
      yAxisWidth={56}
      showAnimation
    />
  );
}
