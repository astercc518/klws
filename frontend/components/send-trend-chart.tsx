"use client";

import { useEffect, useState } from "react";
import { AreaChart } from "@tremor/react";
import { useT } from "@/components/locale-provider";

// Mock 7-day send telemetry until /campaigns aggregation is wired. Keyed by
// date only; the success/failed series keys are localized at render time
// below since Tremor's `categories` must match the data object's keys.
const RAW = [
  { date: "06/20", success: 18420, failed: 240 },
  { date: "06/21", success: 22110, failed: 310 },
  { date: "06/22", success: 19880, failed: 195 },
  { date: "06/23", success: 26540, failed: 420 },
  { date: "06/24", success: 31200, failed: 380 },
  { date: "06/25", success: 28760, failed: 290 },
  { date: "06/26", success: 33980, failed: 510 },
];

const nf = new Intl.NumberFormat("en-US");

export function SendTrendChart() {
  const t = useT();
  // Recharts measures container width on the client; render after mount so SSR
  // doesn't emit a zero-width chart (and to avoid hydration width warnings).
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  if (!mounted) {
    return <div className="h-72 w-full animate-pulse rounded-md bg-muted motion-reduce:animate-none" />;
  }

  const successLabel = t("dash.trend.success");
  const failedLabel = t("dash.trend.failed");
  const data = RAW.map((d) => ({ date: d.date, [successLabel]: d.success, [failedLabel]: d.failed }));

  return (
    <AreaChart
      className="h-72"
      data={data}
      index="date"
      categories={[successLabel, failedLabel]}
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
