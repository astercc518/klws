import type { Locale } from "@/lib/i18n";

const STATS: {
  value: string;
  unit: string;
  label: Record<Locale, string>;
  color: string;
}[] = [
  { value: "1.2M", unit: "msg/min", label: { zh: "峰值并发吞吐", en: "Peak throughput" }, color: "text-emerald-600 dark:text-emerald-500" },
  { value: "98.7%", unit: "", label: { zh: "平均消息到达率", en: "Avg. deliverability" }, color: "text-blue-600 dark:text-blue-400" },
  { value: "3X", unit: "", label: { zh: "营销投放 ROI 提升", en: "Marketing ROI lift" }, color: "text-rose-600 dark:text-rose-400" },
  { value: "<5s", unit: "", label: { zh: "失败自动退款延迟", en: "Auto-refund latency" }, color: "text-amber-600 dark:text-amber-400" },
];

export function Stats({ locale }: { locale: Locale }) {
  return (
    <section className="mx-auto max-w-7xl px-5 py-16 sm:px-8 sm:py-20">
      <div className="grid grid-cols-2 gap-px overflow-hidden rounded-2xl bg-border/70 ring-1 ring-border/70 lg:grid-cols-4">
        {STATS.map((s) => (
          <div key={s.label[locale]} className="bg-background px-6 py-8 text-center">
            <div className="flex items-baseline justify-center gap-1">
              <span className={`text-4xl font-semibold tracking-tight tabular-nums sm:text-5xl ${s.color}`}>
                {s.value}
              </span>
              {s.unit && (
                <span className="font-mono text-xs text-muted-foreground">
                  {s.unit}
                </span>
              )}
            </div>
            <div className="mt-3 text-sm text-muted-foreground">{s.label[locale]}</div>
          </div>
        ))}
      </div>
    </section>
  );
}
