import { ShieldCheck, Wallet, Rocket, Shuffle } from "lucide-react";
import type { Locale } from "@/lib/i18n";

const COPY = {
  eyebrow: { zh: "核心引擎", en: "Core engine" },
  heading: { zh: "把发送系统该有的难题，提前替你解决", en: "The hard problems of a sending platform, solved up front" },
  sub: {
    zh: "klws 不是又一个发送脚本，而是一套覆盖防封、计费、调度与内容混淆的完整控制平面。",
    en: "klws isn't another sending script — it's a complete control plane for anti-ban, billing, scheduling, and content obfuscation.",
  },
};

const FEATURES: {
  icon: typeof ShieldCheck;
  title: Record<Locale, string>;
  body: Record<Locale, string>;
  accent: string;
  bg: string;
}[] = [
  {
    icon: ShieldCheck,
    title: { zh: "独家防封矩阵", en: "Anti-ban matrix" },
    body: {
      zh: "动态代理池与智能发送频率控制，按账号画像调度节奏，把封号风险隔离在边界之外。",
      en: "Dynamic proxy pools and smart rate control pace each account by its profile, keeping ban risk outside the perimeter.",
    },
    accent: "text-emerald-600 dark:text-emerald-500",
    bg: "bg-emerald-500/10",
  },
  {
    icon: Wallet,
    title: { zh: "金融级财务闭环", en: "Financial-grade billing" },
    body: {
      zh: "预扣费与异步对账引擎，按条精确计费；发送失败自动秒级退款，每分预算可追溯。",
      en: "Pre-auth holds and async reconciliation bill per message; failed sends are auto-refunded in seconds, every cent traceable.",
    },
    accent: "text-blue-600 dark:text-blue-400",
    bg: "bg-blue-500/10",
  },
  {
    icon: Rocket,
    title: { zh: "百万级并发调度", en: "Million-scale concurrency" },
    body: {
      zh: "基于 Asynq 深度优化的任务编排，集群节点平滑扩展，峰值流量下稳定吞吐。",
      en: "Asynq-tuned orchestration scales cluster nodes smoothly, holding steady throughput at peak load.",
    },
    accent: "text-rose-600 dark:text-rose-400",
    bg: "bg-rose-500/10",
  },
  {
    icon: Shuffle,
    title: { zh: "智能 Spintax 混淆", en: "Smart Spintax obfuscation" },
    body: {
      zh: "原生多变体文案组合，单次群发自动生成差异化内容，显著提升触达与存活率。",
      en: "Native multi-variant copy generates differentiated content per blast, lifting reach and survival rates.",
    },
    accent: "text-amber-600 dark:text-amber-400",
    bg: "bg-amber-500/10",
  },
];

export function Features({ locale }: { locale: Locale }) {
  return (
    <section id="features" className="scroll-mt-20 border-t border-border/60">
      <div className="mx-auto max-w-7xl px-5 py-20 sm:px-8 sm:py-28">
        <div className="mx-auto max-w-2xl text-center">
          <div className="text-sm font-semibold text-brand-600 dark:text-brand-400">
            {COPY.eyebrow[locale]}
          </div>
          <h2 className="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">
            {COPY.heading[locale]}
          </h2>
          <p className="mt-4 text-pretty text-muted-foreground">
            {COPY.sub[locale]}
          </p>
        </div>

        <div className="mt-14 grid gap-5 sm:grid-cols-2 lg:grid-cols-4">
          {FEATURES.map((f) => {
            const Icon = f.icon;
            return (
              <div
                key={f.title.en}
                className="rounded-2xl border border-border/70 bg-card p-6 shadow-sm ring-1 ring-foreground/5 transition-shadow hover:shadow-md"
              >
                <div className={`flex size-11 items-center justify-center rounded-xl ${f.bg}`}>
                  <Icon className={`size-5.5 ${f.accent}`} strokeWidth={1.9} />
                </div>
                <h3 className="mt-5 text-base font-semibold tracking-tight">
                  {f.title[locale]}
                </h3>
                <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                  {f.body[locale]}
                </p>
              </div>
            );
          })}
        </div>
      </div>
    </section>
  );
}
