import { Terminal, Webhook, KeyRound, Boxes, ArrowRight } from "lucide-react";
import type { Locale } from "@/lib/i18n";
import { CtaButton } from "@/components/landing/cta-button";

const COPY = {
  eyebrow: { zh: "为开发者而生", en: "Built for developers" },
  heading: { zh: "几行代码，把发送能力接进系统", en: "Wire sending into your system in a few lines" },
  sub: {
    zh: "干净的 REST API、可预测的错误码、完善的回执事件。从沙箱到生产，鉴权一致、行为一致。",
    en: "Clean REST API, predictable error codes, complete receipt events. From sandbox to production, consistent auth and behavior.",
  },
  cta: { zh: "查看 API 文档", en: "View API docs" },
  codeTitle: { zh: "发送一条消息", en: "Send a message" },
  comment1: { zh: "# 发起群发请求", en: "# send a message" },
  comment2: { zh: "# 200 OK", en: "# 200 OK" },
};

const POINTS: { icon: typeof Webhook; text: Record<Locale, string> }[] = [
  { icon: Webhook, text: { zh: "发送 / 回执 / 对账全量 Webhook 事件", en: "Webhook events for sends, receipts, and reconciliation" } },
  { icon: KeyRound, text: { zh: "幂等键保证重试不重复发送", en: "Idempotency keys make retries safe" } },
  { icon: Boxes, text: { zh: "Node、Python、Go 官方 SDK", en: "Official Node, Python, and Go SDKs" } },
];

export function Developer({ locale }: { locale: Locale }) {
  return (
    <section className="border-t border-border/60">
      <div className="mx-auto grid max-w-7xl items-center gap-12 px-5 py-20 sm:px-8 sm:py-28 lg:grid-cols-2 lg:gap-16">
        <div className="max-w-lg">
          <div className="text-sm font-semibold text-brand-600 dark:text-brand-400">
            {COPY.eyebrow[locale]}
          </div>
          <h2 className="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">
            {COPY.heading[locale]}
          </h2>
          <p className="mt-4 text-pretty text-muted-foreground">{COPY.sub[locale]}</p>
          <ul className="mt-7 space-y-3">
            {POINTS.map((p) => {
              const Icon = p.icon;
              return (
                <li key={p.text.en} className="flex items-center gap-3 text-sm sm:text-base">
                  <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-brand-500/10 text-brand-600 dark:text-brand-400">
                    <Icon className="size-4" />
                  </span>
                  <span className="text-muted-foreground">{p.text[locale]}</span>
                </li>
              );
            })}
          </ul>
          <div className="mt-8">
            <CtaButton href="/docs" size="md">
              {COPY.cta[locale]}
              <ArrowRight className="size-4" />
            </CtaButton>
          </div>
        </div>

        <div className="overflow-hidden rounded-2xl border border-white/10 bg-neutral-950 shadow-2xl shadow-foreground/10">
          <div className="flex items-center gap-2 border-b border-white/10 px-4 py-3">
            <span className="size-2.5 rounded-full bg-rose-400/80" />
            <span className="size-2.5 rounded-full bg-amber-400/80" />
            <span className="size-2.5 rounded-full bg-emerald-400/80" />
            <span className="ml-2 flex items-center gap-1.5 font-mono text-[11px] text-neutral-400">
              <Terminal className="size-3.5" />
              {COPY.codeTitle[locale]}
            </span>
          </div>
          <pre className="overflow-x-auto p-5 font-mono text-[12.5px] leading-relaxed">
            <code>
              <span className="text-neutral-500">{COPY.comment1[locale]}</span>
              {"\n"}
              <span className="text-emerald-400">curl</span>
              <span className="text-neutral-200"> https://api.klws.cc/v1/messages \</span>
              {"\n"}
              <span className="text-neutral-200">  -H </span>
              <span className="text-amber-300">&quot;Authorization: Bearer sk_live_•••&quot;</span>
              <span className="text-neutral-200"> \</span>
              {"\n"}
              <span className="text-neutral-200">  -d to=</span>
              <span className="text-amber-300">&quot;+1 415 ••• 0123&quot;</span>
              <span className="text-neutral-200"> \</span>
              {"\n"}
              <span className="text-neutral-200">  -d template=</span>
              <span className="text-amber-300">&quot;blackfriday&quot;</span>
              <span className="text-neutral-200"> \</span>
              {"\n"}
              <span className="text-neutral-200">  -d spintax=</span>
              <span className="text-sky-300">true</span>
              {"\n\n"}
              <span className="text-neutral-500">{COPY.comment2[locale]}</span>
              {"\n"}
              <span className="text-neutral-200">{"{"}</span>
              {"\n"}
              <span className="text-sky-300">  &quot;id&quot;</span>
              <span className="text-neutral-200">: </span>
              <span className="text-amber-300">&quot;msg_9F2a7c&quot;</span>
              <span className="text-neutral-200">,</span>
              {"\n"}
              <span className="text-sky-300">  &quot;status&quot;</span>
              <span className="text-neutral-200">: </span>
              <span className="text-amber-300">&quot;queued&quot;</span>
              <span className="text-neutral-200">,</span>
              {"\n"}
              <span className="text-sky-300">  &quot;cost&quot;</span>
              <span className="text-neutral-200">: </span>
              <span className="text-emerald-300">0.05</span>
              <span className="text-neutral-200">,</span>
              {"\n"}
              <span className="text-sky-300">  &quot;refundable&quot;</span>
              <span className="text-neutral-200">: </span>
              <span className="text-sky-300">true</span>
              {"\n"}
              <span className="text-neutral-200">{"}"}</span>
            </code>
          </pre>
        </div>
      </div>
    </section>
  );
}
