"use client";

import { useState } from "react";
import { ArrowRight, Star, Check, Zap } from "lucide-react";
import type { Locale } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { CtaButton } from "@/components/landing/cta-button";

const T = {
  badge: { zh: "WhatsApp 并发营销引擎 · klws.cc", en: "WhatsApp concurrency engine · klws.cc" },
  title1: { zh: "企业级 WhatsApp", en: "The enterprise WhatsApp" },
  titleEm: { zh: "增长引擎", en: "growth engine" },
  sub: {
    zh: "金融级计费、防封号隔离、高到达率。从获客、培育到转化，覆盖客户旅程的每一步。",
    en: "Financial-grade billing, ban-proof isolation, high deliverability — across every step of the customer journey.",
  },
  ctaPrimary: { zh: "免费开始试用", en: "Start for free" },
  ctaSecondary: { zh: "预约产品演示", en: "Book a demo" },
  trust: { zh: "16,000+ 出海团队的共同选择", en: "Chosen by 16,000+ global teams" },
};

const FLOAT_T = {
  delivery: { zh: "平均到达率", en: "Avg. delivery" },
  throughput: { zh: "峰值并发", en: "Peak rate" },
};

type TabKey = "marketing" | "sales" | "support";

const TABS: { key: TabKey; label: Record<Locale, string>; active: string; dot: string }[] = [
  { key: "marketing", label: { zh: "营销", en: "Marketing" }, active: "border-emerald-500 text-emerald-600 dark:text-emerald-400", dot: "bg-emerald-500" },
  { key: "sales", label: { zh: "销售", en: "Sales" }, active: "border-teal-500 text-teal-600 dark:text-teal-400", dot: "bg-teal-500" },
  { key: "support", label: { zh: "客服", en: "Support" }, active: "border-lime-500 text-lime-600 dark:text-lime-500", dot: "bg-lime-500" },
];

export function Hero({ locale }: { locale: Locale }) {
  const [tab, setTab] = useState<TabKey>("marketing");

  return (
    <section className="relative overflow-hidden">
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 -z-10 bg-[radial-gradient(circle_at_1px_1px,var(--border)_1px,transparent_0)] [background-size:26px_26px] [mask-image:radial-gradient(ellipse_70%_55%_at_50%_0%,#000_45%,transparent_100%)]"
      />
      <div
        aria-hidden
        className="pointer-events-none absolute -top-40 left-1/2 -z-10 h-[460px] w-[900px] -translate-x-1/2 rounded-full bg-brand-500/10 blur-3xl dark:bg-brand-500/15"
      />

      <div className="mx-auto max-w-7xl px-5 pt-16 pb-20 sm:px-8 sm:pt-24">
        {/* Centered copy */}
        <div className="mx-auto max-w-3xl text-center">
          <span className="inline-flex items-center gap-2 rounded-full border border-brand-600/20 bg-brand-500/10 px-3 py-1 text-xs font-medium text-brand-700 dark:text-brand-400">
            <Zap className="size-3.5" />
            {T.badge[locale]}
          </span>

          <h1 className="mx-auto mt-6 max-w-3xl text-balance text-4xl font-semibold tracking-tight sm:text-6xl">
            {T.title1[locale]}{" "}
            <span className="relative inline-block">
              <span
                className={cn(
                  "text-brand-600 dark:text-brand-400",
                  locale === "en" && "italic",
                )}
              >
                {T.titleEm[locale]}
              </span>
              {/* hand-drawn underline accent */}
              <svg
                aria-hidden
                viewBox="0 0 200 14"
                preserveAspectRatio="none"
                className="absolute -bottom-1.5 left-0 h-2.5 w-full text-brand-500/70"
              >
                <path
                  d="M3 9C40 4 90 3 130 5c25 1.2 48 3 67 6"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="3"
                  strokeLinecap="round"
                />
              </svg>
            </span>
          </h1>

          <p className="mx-auto mt-6 max-w-xl text-pretty text-base leading-relaxed text-muted-foreground sm:text-lg">
            {T.sub[locale]}
          </p>

          <div className="mt-9 flex flex-col items-center justify-center gap-3 sm:flex-row">
            <CtaButton href="/login" className="w-full sm:w-auto">
              {T.ctaPrimary[locale]}
              <ArrowRight className="size-4" />
            </CtaButton>
            <CtaButton href="/login" variant="outline" className="w-full sm:w-auto">
              {T.ctaSecondary[locale]}
            </CtaButton>
          </div>

          <div className="mt-7 flex flex-wrap items-center justify-center gap-x-6 gap-y-2 text-sm">
            <div className="flex items-center gap-2">
              <div className="flex">
                {Array.from({ length: 5 }).map((_, i) => (
                  <Star key={i} className="size-4 fill-amber-400 text-amber-400" />
                ))}
              </div>
              <span className="font-medium">
                4.8<span className="text-muted-foreground">/5 · G2</span>
              </span>
            </div>
            <div className="flex items-center gap-2 text-muted-foreground">
              <Check className="size-4 text-emerald-600" />
              {T.trust[locale]}
            </div>
          </div>
        </div>

        {/* Tabs */}
        <div className="mt-12 flex justify-center">
          <div className="flex items-center gap-1 rounded-full border border-border/70 bg-card p-1 shadow-sm">
            {TABS.map((t) => (
              <button
                key={t.key}
                type="button"
                onClick={() => setTab(t.key)}
                className={cn(
                  "inline-flex items-center gap-2 rounded-full px-4 py-2 text-sm font-medium transition-colors",
                  tab === t.key
                    ? "bg-muted text-foreground"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                <span className={cn("size-1.5 rounded-full", t.dot)} />
                {t.label[locale]}
              </button>
            ))}
          </div>
        </div>

        {/* Product showcase */}
        <div className="mx-auto mt-8 max-w-5xl">
          <div className="relative">
            <div
              aria-hidden
              className="absolute -inset-3 -z-10 rounded-[32px] bg-gradient-to-tr from-brand-500/10 via-transparent to-emerald-500/10 blur-2xl"
            />
            <div className="overflow-hidden rounded-2xl border border-border/70 bg-card shadow-2xl shadow-foreground/10 ring-1 ring-foreground/5">
              {tab === "marketing" && <MarketingMock locale={locale} />}
              {tab === "sales" && <SalesMock locale={locale} />}
              {tab === "support" && <SupportMock locale={locale} />}
            </div>

            {/* Floating overlay cards (depth, wati-style) */}
            <div
              className="absolute -left-7 top-16 hidden animate-float items-center gap-2.5 rounded-xl border border-border/70 bg-card px-3.5 py-2.5 shadow-xl shadow-foreground/10 ring-1 ring-foreground/5 lg:flex"
              style={{ animationDelay: "0s" }}
            >
              <span className="flex size-8 items-center justify-center rounded-lg bg-emerald-500/10">
                <span className="size-2 rounded-full bg-emerald-500" />
              </span>
              <span className="leading-tight">
                <span className="block text-sm font-semibold tabular-nums">98.7%</span>
                <span className="block text-[10px] text-muted-foreground">
                  {FLOAT_T.delivery[locale]}
                </span>
              </span>
            </div>

            <div
              className="absolute -right-6 top-8 hidden animate-float items-center gap-2.5 rounded-xl border border-border/70 bg-card px-3.5 py-2.5 shadow-xl shadow-foreground/10 ring-1 ring-foreground/5 lg:flex"
              style={{ animationDelay: "1.6s" }}
            >
              <span className="leading-tight">
                <span className="flex gap-0.5">
                  {Array.from({ length: 5 }).map((_, i) => (
                    <Star key={i} className="size-3 fill-amber-400 text-amber-400" />
                  ))}
                </span>
                <span className="mt-1 block text-[10px] text-muted-foreground">
                  4.8 / 5 · G2
                </span>
              </span>
            </div>

            <div
              className="absolute -right-8 bottom-12 hidden animate-float items-center gap-2.5 rounded-xl border border-border/70 bg-card px-3.5 py-2.5 shadow-xl shadow-foreground/10 ring-1 ring-foreground/5 lg:flex"
              style={{ animationDelay: "3.2s" }}
            >
              <span className="flex size-8 items-center justify-center rounded-lg bg-brand-500/10 text-brand-600 dark:text-brand-400">
                <Zap className="size-4" />
              </span>
              <span className="leading-tight">
                <span className="block text-sm font-semibold tabular-nums">
                  1.2M<span className="ml-0.5 text-[10px] font-normal text-muted-foreground">/min</span>
                </span>
                <span className="block text-[10px] text-muted-foreground">
                  {FLOAT_T.throughput[locale]}
                </span>
              </span>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

function WindowBar({ title, accent }: { title: string; accent: string }) {
  return (
    <div className="flex items-center gap-2 border-b border-border/60 bg-muted/40 px-4 py-3">
      <span className="size-2.5 rounded-full bg-rose-400/80" />
      <span className="size-2.5 rounded-full bg-amber-400/80" />
      <span className="size-2.5 rounded-full bg-emerald-400/80" />
      <span className="ml-3 font-mono text-[11px] text-muted-foreground">{title}</span>
      <span className={cn("ml-auto inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[10px] font-medium", accent)}>
        <span className="size-1.5 rounded-full bg-current opacity-80" />
        live
      </span>
    </div>
  );
}

const M = {
  campaigns: { zh: "klws · 营销活动", en: "klws · Campaigns" },
  segments: { zh: "受众分群", en: "Segments" },
  newCampaign: { zh: "黑五大促 · 进行中", en: "Black Friday · Live" },
  funnelSent: { zh: "送达", en: "Sent" },
  funnelOpen: { zh: "打开", en: "Opened" },
  funnelClick: { zh: "点击", en: "Clicked" },
  funnelConv: { zh: "转化", en: "Converted" },
  pipeline: { zh: "klws · 销售管道", en: "klws · Pipeline" },
  stageNew: { zh: "新线索", en: "New" },
  stageQual: { zh: "已鉴别", en: "Qualified" },
  stageWon: { zh: "赢单", en: "Won" },
  score: { zh: "意向", en: "Score" },
  inbox: { zh: "klws · 共享收件箱", en: "klws · Inbox" },
  aiReply: { zh: "AI 自动应答", en: "AI auto-reply" },
  supMsgIn: { zh: "请问订单还能改地址吗？", en: "Can I still change my shipping address?" },
  supMsgOut: { zh: "可以的 🙌 已为你打开修改入口，点此完成", en: "Sure 🙌 I've opened the edit form for you — tap here" },
};

function MarketingMock({ locale }: { locale: Locale }) {
  const segs = [
    { n: { zh: "高价值客户", en: "High-value" }, c: "12,480" },
    { n: { zh: "购物车遗弃", en: "Cart abandon" }, c: "8,210" },
    { n: { zh: "沉睡用户", en: "Dormant" }, c: "23,900" },
  ];
  const funnel = [
    { k: M.funnelSent, v: "100,000", w: "w-full", c: "bg-emerald-500" },
    { k: M.funnelOpen, v: "78,400", w: "w-[78%]", c: "bg-emerald-500/80" },
    { k: M.funnelClick, v: "31,200", w: "w-[31%]", c: "bg-emerald-500/60" },
    { k: M.funnelConv, v: "9,640", w: "w-[10%]", c: "bg-emerald-500" },
  ];
  return (
    <div>
      <WindowBar title={M.campaigns[locale]} accent="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400" />
      <div className="grid gap-4 p-5 sm:grid-cols-[0.8fr_1.2fr]">
        <div className="space-y-2">
          <div className="font-mono text-[10px] uppercase tracking-[0.16em] text-muted-foreground">
            {M.segments[locale]}
          </div>
          {segs.map((s) => (
            <div key={s.n.en} className="flex items-center justify-between rounded-lg border border-border/60 bg-background px-3 py-2.5 text-xs">
              <span className="font-medium">{s.n[locale]}</span>
              <span className="font-mono tabular-nums text-muted-foreground">{s.c}</span>
            </div>
          ))}
        </div>
        <div className="rounded-xl border border-emerald-200/70 bg-emerald-50/60 p-4 dark:border-emerald-900/50 dark:bg-emerald-950/30">
          <div className="text-sm font-semibold">{M.newCampaign[locale]}</div>
          <div className="mt-4 space-y-2.5">
            {funnel.map((f) => (
              <div key={f.k.en}>
                <div className="flex items-center justify-between text-[11px]">
                  <span className="text-muted-foreground">{f.k[locale]}</span>
                  <span className="font-mono tabular-nums">{f.v}</span>
                </div>
                <div className="mt-1 h-2 overflow-hidden rounded-full bg-background">
                  <div className={cn("h-full rounded-full", f.w, f.c)} />
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

function SalesMock({ locale }: { locale: Locale }) {
  const leads = [
    { i: "A", n: "Aisha K.", s: 92, stage: M.stageWon, c: "bg-emerald-600", chip: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400" },
    { i: "L", n: "Liam O.", s: 78, stage: M.stageQual, c: "bg-teal-600", chip: "bg-teal-500/10 text-teal-700 dark:text-teal-400" },
    { i: "陈", n: "陈先生", s: 64, stage: M.stageQual, c: "bg-emerald-600", chip: "bg-teal-500/10 text-teal-700 dark:text-teal-400" },
    { i: "M", n: "María G.", s: 41, stage: M.stageNew, c: "bg-lime-500", chip: "bg-muted text-muted-foreground" },
  ];
  return (
    <div>
      <WindowBar title={M.pipeline[locale]} accent="bg-teal-500/10 text-teal-600 dark:text-teal-400" />
      <div className="space-y-1.5 p-5">
        {leads.map((l) => (
          <div key={l.n} className="flex items-center gap-3 rounded-lg border border-border/60 bg-background px-3 py-2.5">
            <span className={cn("flex size-8 items-center justify-center rounded-full text-xs font-semibold text-white", l.c)}>
              {l.i}
            </span>
            <span className="flex-1 text-sm font-medium">{l.n}</span>
            <span className="hidden items-center gap-1.5 sm:flex">
              <span className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
                {M.score[locale]}
              </span>
              <span className="h-1.5 w-16 overflow-hidden rounded-full bg-muted">
                <span className="block h-full rounded-full bg-teal-500" style={{ width: `${l.s}%` }} />
              </span>
              <span className="w-7 font-mono text-xs tabular-nums">{l.s}</span>
            </span>
            <span className={cn("rounded-full px-2.5 py-1 text-[11px] font-medium", l.chip)}>
              {l.stage[locale]}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

function SupportMock({ locale }: { locale: Locale }) {
  const convos = [
    { n: "Aisha K.", p: { zh: "地址改一下", en: "Change my address" }, unread: true },
    { n: "Liam O.", p: { zh: "发票什么时候", en: "When's my invoice" }, unread: false },
    { n: "María G.", p: { zh: "谢谢！", en: "Thanks!" }, unread: false },
  ];
  return (
    <div>
      <WindowBar title={M.inbox[locale]} accent="bg-lime-500/10 text-lime-600 dark:text-lime-500" />
      <div className="grid grid-cols-[0.9fr_1.1fr]">
        <div className="space-y-1 border-r border-border/60 p-4">
          {convos.map((c) => (
            <div key={c.n} className="flex items-center gap-2 rounded-lg px-2 py-2 text-xs hover:bg-muted/50">
              <span className="flex size-7 items-center justify-center rounded-full bg-muted text-[11px] font-semibold">
                {c.n[0]}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium">{c.n}</span>
                <span className="block truncate text-muted-foreground">{c.p[locale]}</span>
              </span>
              {c.unread && <span className="size-2 rounded-full bg-lime-500" />}
            </div>
          ))}
        </div>
        <div className="space-y-3 p-4">
          <div className="max-w-[85%] rounded-2xl rounded-tl-sm bg-muted px-3 py-2 text-xs leading-relaxed">
            {M.supMsgIn[locale]}
          </div>
          <div className="ml-auto flex max-w-[90%] flex-col items-end gap-1">
            <span className="inline-flex items-center gap-1 rounded-full bg-lime-500/10 px-2 py-0.5 text-[9px] font-medium uppercase tracking-wider text-lime-600 dark:text-lime-500">
              {M.aiReply[locale]}
            </span>
            <div className="rounded-2xl rounded-tr-sm bg-emerald-600 px-3 py-2 text-xs leading-relaxed text-white">
              {M.supMsgOut[locale]}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
