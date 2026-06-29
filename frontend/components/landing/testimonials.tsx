import { Star } from "lucide-react";
import type { Locale } from "@/lib/i18n";

const COPY = {
  eyebrow: { zh: "客户证言", en: "Testimonials" },
  heading: { zh: "出海团队，用结果投票", en: "Global teams vote with results" },
};

const QUOTES: {
  quote: Record<Locale, string>;
  name: string;
  role: Record<Locale, string>;
  initial: string;
  color: string;
}[] = [
  {
    quote: {
      zh: "迁到 klws 之后，黑五大促单日发出 120 万条，到达率稳定在 98% 以上，封号几乎归零。预扣费和秒退款让财务第一次能对得平账。",
      en: "After moving to klws, we sent 1.2M messages in a single Black Friday day with delivery above 98% and near-zero bans. Pre-auth billing and instant refunds let finance balance the books for the first time.",
    },
    name: "陈薇 · Wei Chen",
    role: { zh: "增长负责人 · NovaPay", en: "Head of Growth · NovaPay" },
    initial: "W",
    color: "bg-emerald-600",
  },
  {
    quote: {
      zh: "线索从广告点击到第一条回复不到 3 秒，销售周期肉眼可见地缩短。智能分配把对的客户送到对的人手里，转化提了一截。",
      en: "Leads go from ad click to first reply in under 3 seconds, and the sales cycle visibly shortened. Smart routing puts the right customer with the right rep, and conversion jumped.",
    },
    name: "Marcus Tan",
    role: { zh: "销售主管 · ShopiGo", en: "Head of Sales · ShopiGo" },
    initial: "M",
    color: "bg-blue-600",
  },
  {
    quote: {
      zh: "API 文档清晰，半天就接进了我们的工单系统。80% 的高频问题被自动应答消化，客服团队终于能专注复杂个案。",
      en: "The API docs are clear — we integrated our ticketing system in half a day. 80% of common questions are handled by auto-replies, so support can finally focus on hard cases.",
    },
    name: "Priya N.",
    role: { zh: "客户成功总监 · Finlytic", en: "Director of CS · Finlytic" },
    initial: "P",
    color: "bg-rose-600",
  },
];

export function Testimonials({ locale }: { locale: Locale }) {
  return (
    <section className="border-t border-border/60 bg-muted/30">
      <div className="mx-auto max-w-7xl px-5 py-20 sm:px-8 sm:py-28">
        <div className="mx-auto max-w-2xl text-center">
          <div className="text-sm font-semibold text-brand-600 dark:text-brand-400">
            {COPY.eyebrow[locale]}
          </div>
          <h2 className="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">
            {COPY.heading[locale]}
          </h2>
        </div>

        <div className="mt-14 grid gap-5 lg:grid-cols-3">
          {QUOTES.map((q) => (
            <figure
              key={q.name}
              className="flex flex-col rounded-2xl border border-border/70 bg-card p-6 shadow-sm ring-1 ring-foreground/5"
            >
              <div className="flex gap-0.5">
                {Array.from({ length: 5 }).map((_, i) => (
                  <Star key={i} className="size-4 fill-amber-400 text-amber-400" />
                ))}
              </div>
              <blockquote className="mt-4 flex-1 text-sm leading-relaxed text-foreground/90">
                “{q.quote[locale]}”
              </blockquote>
              <figcaption className="mt-6 flex items-center gap-3">
                <span className={`flex size-9 items-center justify-center rounded-full ${q.color} text-sm font-semibold text-white`}>
                  {q.initial}
                </span>
                <span className="leading-tight">
                  <span className="block text-sm font-semibold">{q.name}</span>
                  <span className="block text-xs text-muted-foreground">
                    {q.role[locale]}
                  </span>
                </span>
              </figcaption>
            </figure>
          ))}
        </div>
      </div>
    </section>
  );
}
