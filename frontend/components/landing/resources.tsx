import Link from "next/link";
import { ArrowUpRight, Clock } from "lucide-react";
import type { Locale } from "@/lib/i18n";

const COPY = {
  eyebrow: { zh: "资源中心", en: "Resources" },
  heading: { zh: "把出海增长的经验，讲给你听", en: "Cross-border growth, distilled" },
  all: { zh: "查看全部文章", en: "View all articles" },
  readUnit: { zh: "阅读", en: " read" },
};

const POSTS: {
  tag: Record<Locale, string>;
  tagColor: string;
  title: Record<Locale, string>;
  excerpt: Record<Locale, string>;
  read: Record<Locale, string>;
}[] = [
  {
    tag: { zh: "防封实战", en: "Anti-ban" },
    tagColor: "text-emerald-600 dark:text-emerald-500 bg-emerald-500/10",
    title: { zh: "WhatsApp 群发防封实战指南：代理、节奏与文案", en: "The WhatsApp anti-ban playbook: proxies, cadence, copy" },
    excerpt: { zh: "从账号养护到发送频率曲线，拆解封号背后的风控逻辑，给出可落地的参数。", en: "From account warm-up to rate curves — the risk logic behind bans, with practical parameters." },
    read: { zh: "8 分钟", en: "8 min" },
  },
  {
    tag: { zh: "增长策略", en: "Growth" },
    tagColor: "text-blue-600 dark:text-blue-400 bg-blue-500/10",
    title: { zh: "跨境黑五营销 Playbook：从广告点击到复购", en: "Cross-border Black Friday playbook: click to repeat purchase" },
    excerpt: { zh: "用个性化群发承接广告流量，搭建四段式自动化旅程，把一次性买家变成回头客。", en: "Catch ad traffic with personalized blasts and a four-stage journey that turns one-time buyers into regulars." },
    read: { zh: "11 分钟", en: "11 min" },
  },
  {
    tag: { zh: "开发者", en: "Developers" },
    tagColor: "text-rose-600 dark:text-rose-400 bg-rose-500/10",
    title: { zh: "半天接入 klws API：把发送能力嵌进你的系统", en: "Integrate the klws API in half a day" },
    excerpt: { zh: "鉴权、发送、回执 Webhook 与对账的最小可用接入路径，附完整示例。", en: "The minimal path through auth, send, receipt webhooks, and reconciliation — with full examples." },
    read: { zh: "6 分钟", en: "6 min" },
  },
];

export function Resources({ locale }: { locale: Locale }) {
  return (
    <section className="border-t border-border/60 bg-muted/30">
      <div className="mx-auto max-w-7xl px-5 py-20 sm:px-8 sm:py-28">
        <div className="flex flex-col items-start justify-between gap-4 sm:flex-row sm:items-end">
          <div className="max-w-xl">
            <div className="text-sm font-semibold text-brand-600 dark:text-brand-400">
              {COPY.eyebrow[locale]}
            </div>
            <h2 className="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">
              {COPY.heading[locale]}
            </h2>
          </div>
          <Link
            href="#"
            className="inline-flex items-center gap-1.5 text-sm font-medium text-brand-600 hover:text-brand-700 dark:text-brand-400"
          >
            {COPY.all[locale]}
            <ArrowUpRight className="size-4" />
          </Link>
        </div>

        <div className="mt-12 grid gap-5 lg:grid-cols-3">
          {POSTS.map((p) => (
            <Link
              key={p.title.en}
              href="#"
              className="group flex flex-col rounded-2xl border border-border/70 bg-card p-6 shadow-sm ring-1 ring-foreground/5 transition-shadow hover:shadow-md"
            >
              <span className={`inline-flex w-fit rounded-full px-2.5 py-1 text-xs font-medium ${p.tagColor}`}>
                {p.tag[locale]}
              </span>
              <h3 className="mt-4 text-base font-semibold leading-snug tracking-tight transition-colors group-hover:text-brand-600 dark:group-hover:text-brand-400">
                {p.title[locale]}
              </h3>
              <p className="mt-2 flex-1 text-sm leading-relaxed text-muted-foreground">
                {p.excerpt[locale]}
              </p>
              <div className="mt-5 flex items-center gap-1.5 font-mono text-[11px] text-muted-foreground">
                <Clock className="size-3.5" />
                {p.read[locale]}{COPY.readUnit[locale]}
              </div>
            </Link>
          ))}
        </div>
      </div>
    </section>
  );
}
