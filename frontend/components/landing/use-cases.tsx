import { Check, ArrowRight } from "lucide-react";
import type { Locale } from "@/lib/i18n";
import { CtaButton } from "@/components/landing/cta-button";

const COPY = {
  eyebrow: { zh: "解决方案", en: "Solutions" },
  heading: { zh: "一个引擎，贯通营销、销售与客服", en: "One engine across marketing, sales, and support" },
  sub: { zh: "把 WhatsApp 放在增长的核心，覆盖客户旅程的每一步。", en: "Put WhatsApp at the core of growth, across every step of the customer journey." },
  learn: { zh: "了解详情", en: "Learn more" },
};

type UseCase = {
  key: string;
  eyebrow: Record<Locale, string>;
  en: string;
  title: Record<Locale, string>;
  bullets: Record<Locale, string[]>;
  metrics: { value: string; label: Record<Locale, string> }[];
  flow: Record<Locale, string[]>;
  text: string;
  softBg: string;
  border: string;
  dot: string;
  chipBg: string;
};

const USE_CASES: UseCase[] = [
  {
    key: "marketing",
    eyebrow: { zh: "营销", en: "Marketing" },
    en: "Marketing",
    title: { zh: "用个性化群发，规模化获客、培育与转化", en: "Acquire, nurture, and convert at scale with personalized campaigns" },
    bullets: {
      zh: ["Spintax 多变体文案，千人千面不撞车", "广告点击直达对话，落地无缝衔接", "分群标签与自动化旅程，按行为触发", "实时到达 / 阅读 / 转化漏斗看板"],
      en: ["Spintax variants — unique copy for every recipient", "Click-to-chat from ads, seamless landing", "Segments and journeys triggered by behavior", "Live delivery / read / conversion funnel"],
    },
    metrics: [
      { value: "4X", label: { zh: "更低获客成本", en: "Lower CAC" } },
      { value: "3X", label: { zh: "营销 ROI", en: "Marketing ROI" } },
      { value: "85%", label: { zh: "更高回复率", en: "Higher reply rate" } },
    ],
    flow: { zh: ["投放", "对话", "培育", "转化"], en: ["Ad", "Chat", "Nurture", "Convert"] },
    text: "text-emerald-600 dark:text-emerald-400",
    softBg: "bg-emerald-50 dark:bg-emerald-950/30",
    border: "border-emerald-200/70 dark:border-emerald-900/50",
    dot: "text-emerald-600 dark:text-emerald-400",
    chipBg: "bg-emerald-500/10",
  },
  {
    key: "sales",
    eyebrow: { zh: "销售", en: "Sales" },
    en: "Sales",
    title: { zh: "加速管道流转，提升转化、缩短成交周期", en: "Accelerate the pipeline, lift conversion, shorten cycles" },
    bullets: {
      zh: ["线索秒级响应，热度不流失", "智能问答自动鉴别意向并打分", "按区域 / 语种自动分配到对应销售", "对话上下文沉淀，交接零损耗"],
      en: ["Second-level lead response keeps intent warm", "AI qualifies and scores intent automatically", "Auto-route by region / language to the right rep", "Conversation context persists, zero handoff loss"],
    },
    metrics: [
      { value: "30%", label: { zh: "缩短销售周期", en: "Shorter cycle" } },
      { value: "3X", label: { zh: "更快响应", en: "Faster response" } },
      { value: "20%", label: { zh: "营收增长", en: "Revenue growth" } },
    ],
    flow: { zh: ["触达", "鉴别", "分配", "赢单"], en: ["Engage", "Qualify", "Assign", "Win"] },
    text: "text-teal-600 dark:text-teal-400",
    softBg: "bg-teal-50 dark:bg-teal-950/30",
    border: "border-teal-200/70 dark:border-teal-900/50",
    dot: "text-teal-600 dark:text-teal-400",
    chipBg: "bg-teal-500/10",
  },
  {
    key: "support",
    eyebrow: { zh: "客服", en: "Support" },
    en: "Support",
    title: { zh: "规模化承接咨询，让每位客户都被善待", en: "Handle inquiries at scale and delight every customer" },
    bullets: {
      zh: ["知识库驱动的自动应答，秒解高频问题", "复杂工单智能升级到人工坐席", "多人协作收件箱，内部备注不外泄", "满意度与解决时长全程可量化"],
      en: ["Knowledge-base auto-replies resolve FAQs instantly", "Complex tickets escalate smartly to agents", "Shared inbox with private internal notes", "CSAT and resolution time fully measurable"],
    },
    metrics: [
      { value: "40%", label: { zh: "降低工作量", en: "Less workload" } },
      { value: "80%", label: { zh: "FAQ 自动解决", en: "FAQs auto-resolved" } },
      { value: "40%", label: { zh: "更快解决", en: "Faster resolution" } },
    ],
    flow: { zh: ["咨询", "应答", "升级", "解决"], en: ["Inquiry", "Respond", "Escalate", "Resolve"] },
    text: "text-lime-600 dark:text-lime-500",
    softBg: "bg-lime-50 dark:bg-lime-950/30",
    border: "border-lime-200/70 dark:border-lime-900/50",
    dot: "text-lime-600 dark:text-lime-500",
    chipBg: "bg-lime-500/10",
  },
];

function UseCaseRow({ uc, flip, locale }: { uc: UseCase; flip: boolean; locale: Locale }) {
  return (
    <div className="grid items-center gap-10 lg:grid-cols-2 lg:gap-16">
      <div className={flip ? "lg:order-2" : ""}>
        <span className={`inline-flex items-center gap-2 rounded-full ${uc.chipBg} px-3 py-1 text-xs font-semibold ${uc.text}`}>
          {uc.eyebrow[locale]}
          <span className="font-mono text-[10px] uppercase tracking-[0.14em] opacity-70">
            {uc.en}
          </span>
        </span>
        <h3 className="mt-4 text-2xl font-semibold tracking-tight sm:text-3xl">
          {uc.title[locale]}
        </h3>
        <ul className="mt-6 space-y-3">
          {uc.bullets[locale].map((b) => (
            <li key={b} className="flex items-start gap-3 text-sm sm:text-base">
              <Check className={`mt-0.5 size-5 shrink-0 ${uc.dot}`} />
              <span className="text-muted-foreground">{b}</span>
            </li>
          ))}
        </ul>
        <div className="mt-8">
          <CtaButton href="/login" variant="outline" size="md">
            {COPY.learn[locale]}
            <ArrowRight className="size-4" />
          </CtaButton>
        </div>
      </div>

      <div className={flip ? "lg:order-1" : ""}>
        <div className={`rounded-3xl border ${uc.border} ${uc.softBg} p-6 sm:p-8`}>
          <div className="grid grid-cols-3 gap-4">
            {uc.metrics.map((m) => (
              <div key={m.label.en}>
                <div className={`text-3xl font-semibold tracking-tight tabular-nums sm:text-4xl ${uc.text}`}>
                  {m.value}
                </div>
                <div className="mt-1.5 text-xs leading-snug text-muted-foreground">
                  {m.label[locale]}
                </div>
              </div>
            ))}
          </div>

          <div className="mt-8 flex flex-wrap items-center gap-2">
            {uc.flow[locale].map((step, i) => (
              <div key={step} className="flex items-center gap-2">
                <span className="rounded-lg border border-border/60 bg-background px-3 py-1.5 text-xs font-medium shadow-sm">
                  {step}
                </span>
                {i < uc.flow[locale].length - 1 && (
                  <ArrowRight className={`size-3.5 ${uc.dot}`} />
                )}
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

export function UseCases({ locale }: { locale: Locale }) {
  return (
    <section id="use-cases" className="scroll-mt-20 bg-muted/30">
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

        <div className="mt-16 space-y-20 sm:space-y-24">
          {USE_CASES.map((uc, i) => (
            <UseCaseRow key={uc.key} uc={uc} flip={i % 2 === 1} locale={locale} />
          ))}
        </div>
      </div>
    </section>
  );
}
