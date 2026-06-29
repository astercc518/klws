import { Check } from "lucide-react";
import type { Locale } from "@/lib/i18n";
import { CtaButton } from "@/components/landing/cta-button";

const COPY = {
  eyebrow: { zh: "定价方案", en: "Pricing" },
  heading: { zh: "按规模选择，随业务伸缩", en: "Pick by scale, grow with your business" },
  sub: {
    zh: "所有档位均含防封矩阵与金融级对账。用多少、计多少，发送失败自动退回额度。",
    en: "Every tier includes the anti-ban matrix and financial-grade reconciliation. Pay for what you use; failed sends are refunded.",
  },
  popular: { zh: "最受欢迎", en: "Most popular" },
};

const TIERS: {
  name: string;
  zh: Record<Locale, string>;
  price: Record<Locale, string>;
  period: Record<Locale, string>;
  blurb: Record<Locale, string>;
  features: Record<Locale, string[]>;
  cta: Record<Locale, string>;
  featured: boolean;
}[] = [
  {
    name: "Starter",
    zh: { zh: "入门", en: "" },
    price: { zh: "$50", en: "$50" },
    period: { zh: "/ 月", en: "/ mo" },
    blurb: { zh: "适合首次验证渠道与小规模触达。", en: "For validating the channel and small-scale outreach." },
    features: {
      zh: ["每月 1,000 条发信额度", "共享代理池", "基础 Spintax 变体", "标准发送频率控制", "邮件工单支持"],
      en: ["1,000 messages / month", "Shared proxy pool", "Basic Spintax variants", "Standard rate control", "Email support"],
    },
    cta: { zh: "选择方案", en: "Choose plan" },
    featured: false,
  },
  {
    name: "Pro",
    zh: { zh: "专业", en: "" },
    price: { zh: "$299", en: "$299" },
    period: { zh: "/ 月", en: "/ mo" },
    blurb: { zh: "成长团队的主力档位，API 与独立代理全开。", en: "The workhorse tier — full API and dedicated proxies." },
    features: {
      zh: ["每月 20,000 条发信额度", "独立高匿代理池", "完整 API 接入", "异步对账与失败秒退款", "高级防封策略", "优先技术支持"],
      en: ["20,000 messages / month", "Dedicated elite proxy pool", "Full API access", "Async reconciliation & instant refunds", "Advanced anti-ban strategy", "Priority support"],
    },
    cta: { zh: "选择方案", en: "Choose plan" },
    featured: true,
  },
  {
    name: "Enterprise",
    zh: { zh: "企业", en: "" },
    price: { zh: "定制", en: "Custom" },
    period: { zh: "", en: "" },
    blurb: { zh: "面向规模化跨境营销的专属底座。", en: "A dedicated foundation for cross-border marketing at scale." },
    features: {
      zh: ["不限量发送额度", "专属集群节点", "独立防封与合规策略", "SLA 与专属客户成功", "私有化 / VPC 部署"],
      en: ["Unlimited sending", "Dedicated cluster nodes", "Custom anti-ban & compliance", "SLA & dedicated CSM", "Private / VPC deployment"],
    },
    cta: { zh: "联系销售", en: "Contact sales" },
    featured: false,
  },
];

export function Pricing({ locale }: { locale: Locale }) {
  return (
    <section id="pricing" className="scroll-mt-20 border-t border-border/60">
      <div className="mx-auto max-w-7xl px-5 py-20 sm:px-8 sm:py-28">
        <div className="mx-auto max-w-2xl text-center">
          <div className="text-sm font-semibold text-brand-600 dark:text-brand-400">
            {COPY.eyebrow[locale]}
          </div>
          <h2 className="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">
            {COPY.heading[locale]}
          </h2>
          <p className="mt-4 text-pretty text-muted-foreground">{COPY.sub[locale]}</p>
        </div>

        <div className="mt-14 grid items-start gap-6 lg:grid-cols-3">
          {TIERS.map((tier) => (
            <div
              key={tier.name}
              className={
                tier.featured
                  ? "relative flex flex-col rounded-3xl border-2 border-brand-500 bg-card p-8 shadow-xl shadow-brand-600/10 lg:-mt-4 lg:pb-10"
                  : "relative flex flex-col rounded-3xl border border-border/70 bg-card p-8 shadow-sm ring-1 ring-foreground/5"
              }
            >
              {tier.featured && (
                <span className="absolute -top-3 left-1/2 -translate-x-1/2 rounded-full bg-brand-600 px-3 py-1 text-xs font-semibold text-white shadow-sm">
                  {COPY.popular[locale]}
                </span>
              )}

              <div className="text-sm font-semibold tracking-tight">
                {tier.name}
                {tier.zh[locale] && (
                  <span className="ml-2 text-xs font-normal text-muted-foreground">
                    {tier.zh[locale]}
                  </span>
                )}
              </div>

              <div className="mt-5 flex items-baseline gap-1">
                <span className="text-4xl font-semibold tracking-tight tabular-nums">
                  {tier.price[locale]}
                </span>
                {tier.period[locale] && (
                  <span className="text-sm text-muted-foreground">
                    {tier.period[locale]}
                  </span>
                )}
              </div>
              <p className="mt-2 text-sm text-muted-foreground">{tier.blurb[locale]}</p>

              <ul className="mt-7 space-y-3 text-sm">
                {tier.features[locale].map((feat) => (
                  <li key={feat} className="flex items-start gap-2.5">
                    <Check className="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-500" />
                    <span className="text-muted-foreground">{feat}</span>
                  </li>
                ))}
              </ul>

              <CtaButton
                href="/login"
                variant={tier.featured ? "primary" : "outline"}
                className="mt-8 w-full"
              >
                {tier.cta[locale]}
              </CtaButton>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}
