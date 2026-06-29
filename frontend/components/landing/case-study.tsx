import { Quote, ArrowUpRight } from "lucide-react";
import Link from "next/link";
import type { Locale } from "@/lib/i18n";

const COPY = {
  tag: { zh: "跨境支付 · 客户案例", en: "Cross-border payments · Case study" },
  quote: {
    zh: "迁到 klws 之后，黑五大促单日发出 120 万条，到达率稳定在 98% 以上，封号几乎归零。预扣费和秒退款，让财务第一次能对得平账。",
    en: "After moving to klws, we sent 1.2M messages in a single Black Friday day — delivery held above 98% and bans were near zero. Pre-auth billing and instant refunds let finance balance the books for the first time.",
  },
  name: { zh: "陈薇 · Wei Chen", en: "Wei Chen" },
  role: { zh: "增长负责人 · NovaPay", en: "Head of Growth · NovaPay" },
  read: { zh: "阅读完整案例", en: "Read the full story" },
};

const METRICS: { value: string; label: Record<Locale, string> }[] = [
  { value: "1.2M", label: { zh: "大促单日发送量", en: "Messages in one day" } },
  { value: "98.4%", label: { zh: "平均到达率", en: "Avg. deliverability" } },
  { value: "0", label: { zh: "封号事故", en: "Ban incidents" } },
];

export function CaseStudy({ locale }: { locale: Locale }) {
  return (
    <section className="border-t border-border/60">
      <div className="mx-auto max-w-7xl px-5 py-20 sm:px-8 sm:py-28">
        <div className="overflow-hidden rounded-3xl border border-border/70 bg-card shadow-sm ring-1 ring-foreground/5">
          <div className="grid lg:grid-cols-[1.2fr_0.8fr]">
            <div className="p-8 sm:p-12">
              <div className="flex items-center gap-3">
                <span className="flex size-9 items-center justify-center rounded-lg bg-brand-600 text-sm font-bold text-white">
                  N
                </span>
                <span className="text-sm font-semibold tracking-tight">
                  NovaPay
                  <span className="ml-2 font-normal text-muted-foreground">
                    {COPY.tag[locale]}
                  </span>
                </span>
              </div>

              <Quote className="mt-8 size-8 text-brand-600/30 dark:text-brand-400/30" />
              <blockquote className="mt-3 text-pretty text-xl font-medium leading-relaxed tracking-tight sm:text-2xl">
                {COPY.quote[locale]}
              </blockquote>

              <div className="mt-8 flex items-center justify-between">
                <div className="leading-tight">
                  <div className="text-sm font-semibold">{COPY.name[locale]}</div>
                  <div className="text-xs text-muted-foreground">{COPY.role[locale]}</div>
                </div>
                <Link
                  href="#"
                  className="inline-flex items-center gap-1.5 text-sm font-medium text-brand-600 hover:text-brand-700 dark:text-brand-400"
                >
                  {COPY.read[locale]}
                  <ArrowUpRight className="size-4" />
                </Link>
              </div>
            </div>

            <div className="grid grid-cols-3 gap-px bg-border/70 lg:grid-cols-1">
              {METRICS.map((m) => (
                <div
                  key={m.label.en}
                  className="flex flex-col justify-center bg-gradient-to-br from-emerald-500/10 to-transparent p-6 text-center lg:p-8 lg:text-left"
                >
                  <div className="text-3xl font-semibold tracking-tight tabular-nums text-emerald-600 sm:text-4xl dark:text-emerald-500">
                    {m.value}
                  </div>
                  <div className="mt-1.5 text-xs text-muted-foreground sm:text-sm">
                    {m.label[locale]}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
