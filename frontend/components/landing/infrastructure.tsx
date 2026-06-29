import { Globe, Gauge, Boxes, Radio, MapPin } from "lucide-react";
import type { Locale } from "@/lib/i18n";

const COPY = {
  eyebrow: { zh: "全球基础设施", en: "Global infrastructure" },
  heading: { zh: "就近调度，全球稳定送达", en: "Routed close, delivered everywhere" },
  sub: {
    zh: "多区域边缘节点与运营商级直连，按收件人所在地就近发送，在峰值流量下依旧保持低延迟与高到达率。",
    en: "Multi-region edge nodes and carrier-grade routes send from nearest to the recipient, holding low latency and high delivery at peak.",
  },
};

const STATS: { icon: typeof Globe; value: string; label: Record<Locale, string> }[] = [
  { icon: Globe, value: "200+", label: { zh: "覆盖国家与地区", en: "Countries & regions" } },
  { icon: Gauge, value: "99.95%", label: { zh: "服务在线率 SLA", en: "Uptime SLA" } },
  { icon: Boxes, value: "12", label: { zh: "区域边缘节点", en: "Edge regions" } },
  { icon: Radio, value: "<80ms", label: { zh: "平均调度延迟", en: "Avg. dispatch latency" } },
];

const REGIONS: Record<Locale, string[]> = {
  zh: ["北美", "欧洲", "东南亚", "中东", "拉美", "南亚", "非洲", "大洋洲"],
  en: ["North America", "Europe", "SE Asia", "Middle East", "LATAM", "South Asia", "Africa", "Oceania"],
};

export function Infrastructure({ locale }: { locale: Locale }) {
  return (
    <section className="border-t border-border/60">
      <div className="mx-auto max-w-7xl px-5 py-20 sm:px-8 sm:py-28">
        <div className="grid items-center gap-12 lg:grid-cols-[1fr_1fr] lg:gap-16">
          <div className="max-w-lg">
            <div className="text-sm font-semibold text-brand-600 dark:text-brand-400">
              {COPY.eyebrow[locale]}
            </div>
            <h2 className="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">
              {COPY.heading[locale]}
            </h2>
            <p className="mt-4 text-pretty text-muted-foreground">{COPY.sub[locale]}</p>

            <div className="mt-8 flex flex-wrap gap-2">
              {REGIONS[locale].map((r) => (
                <span
                  key={r}
                  className="inline-flex items-center gap-1.5 rounded-full border border-border/70 bg-card px-3 py-1.5 text-sm text-muted-foreground"
                >
                  <MapPin className="size-3.5 text-brand-600 dark:text-brand-400" />
                  {r}
                </span>
              ))}
            </div>
          </div>

          <div className="grid grid-cols-2 gap-4">
            {STATS.map((s) => {
              const Icon = s.icon;
              return (
                <div
                  key={s.label.en}
                  className="rounded-2xl border border-border/70 bg-card p-6 shadow-sm ring-1 ring-foreground/5"
                >
                  <Icon className="size-5 text-brand-600 dark:text-brand-400" />
                  <div className="mt-4 text-3xl font-semibold tracking-tight tabular-nums">
                    {s.value}
                  </div>
                  <div className="mt-1 text-sm text-muted-foreground">{s.label[locale]}</div>
                </div>
              );
            })}
          </div>
        </div>
      </div>
    </section>
  );
}
