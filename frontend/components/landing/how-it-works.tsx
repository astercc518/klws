import { UserPlus, PlugZap, Send } from "lucide-react";
import type { Locale } from "@/lib/i18n";

const COPY = {
  eyebrow: { zh: "快速上手", en: "Get started" },
  heading: { zh: "三步，把 WhatsApp 接进你的增长链路", en: "Three steps to wire WhatsApp into your growth stack" },
  sub: { zh: "从注册到第一条群发，通常一个下午就能跑通。", en: "From sign-up to your first blast — usually within an afternoon." },
};

const STEPS: {
  n: string;
  icon: typeof UserPlus;
  title: Record<Locale, string>;
  body: Record<Locale, string>;
}[] = [
  {
    n: "01",
    icon: UserPlus,
    title: { zh: "注册并充值钱包", en: "Sign up & top up" },
    body: {
      zh: "几分钟创建账号，按量充值，无需信用卡即可起步。预扣费模式，余额实时可见。",
      en: "Create an account in minutes and top up as you go — no credit card to start. Pre-auth billing, balance always visible.",
    },
  },
  {
    n: "02",
    icon: PlugZap,
    title: { zh: "接入账号与 API", en: "Connect accounts & API" },
    body: {
      zh: "把 WhatsApp 账号绑定进隔离代理池，或用 REST API / Webhook 接入你现有的系统。",
      en: "Bind WhatsApp accounts into isolated proxy pools, or plug into your stack via REST API / Webhooks.",
    },
  },
  {
    n: "03",
    icon: Send,
    title: { zh: "发起群发与对账", en: "Send & reconcile" },
    body: {
      zh: "导入名单、配置 Spintax 变体，一键群发，实时看到达、阅读与按条计费。",
      en: "Import lists, configure Spintax variants, blast in one click, and watch delivery, reads, and per-message billing live.",
    },
  },
];

export function HowItWorks({ locale }: { locale: Locale }) {
  return (
    <section className="border-t border-border/60">
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

        <div className="relative mt-16 grid gap-8 lg:grid-cols-3 lg:gap-6">
          <div
            aria-hidden
            className="absolute top-7 left-0 hidden h-px w-full bg-gradient-to-r from-transparent via-border to-transparent lg:block"
          />
          {STEPS.map((s) => {
            const Icon = s.icon;
            return (
              <div key={s.n} className="relative">
                <div className="flex items-center gap-4">
                  <div className="relative z-10 flex size-14 shrink-0 items-center justify-center rounded-2xl border border-brand-600/20 bg-brand-500/10 text-brand-600 dark:text-brand-400">
                    <Icon className="size-6" strokeWidth={1.9} />
                  </div>
                  <span className="font-mono text-3xl font-semibold tracking-tight text-muted-foreground/30">
                    {s.n}
                  </span>
                </div>
                <h3 className="mt-5 text-lg font-semibold tracking-tight">
                  {s.title[locale]}
                </h3>
                <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                  {s.body[locale]}
                </p>
              </div>
            );
          })}
        </div>
      </div>
    </section>
  );
}
