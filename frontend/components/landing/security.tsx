import { ShieldCheck, Lock, Server, Database, KeyRound, BadgeCheck } from "lucide-react";
import type { Locale } from "@/lib/i18n";

const COPY = {
  eyebrow: { zh: "安全与合规", en: "Security & compliance" },
  heading: { zh: "企业级安全，从底层做起", en: "Enterprise security, from the ground up" },
  sub: {
    zh: "发送能力之外，klws 把隔离、加密与合规当作产品的第一性要求。",
    en: "Beyond sending, klws treats isolation, encryption, and compliance as first principles.",
  },
};

const POINTS: {
  icon: typeof ShieldCheck;
  title: Record<Locale, string>;
  body: Record<Locale, string>;
}[] = [
  {
    icon: ShieldCheck,
    title: { zh: "多租户 RLS 隔离", en: "Multi-tenant RLS isolation" },
    body: { zh: "行级安全策略，租户数据物理隔离，互不可见。", en: "Row-level security physically isolates tenant data, invisible across accounts." },
  },
  {
    icon: Lock,
    title: { zh: "全程加密", en: "End-to-end encryption" },
    body: { zh: "传输与存储端到端加密，密钥分级托管。", en: "Encryption in transit and at rest, with tiered key custody." },
  },
  {
    icon: Server,
    title: { zh: "私有化部署", en: "Private deployment" },
    body: { zh: "支持 VPC / 私有化交付，满足数据驻留要求。", en: "VPC / on-prem delivery to meet data residency needs." },
  },
  {
    icon: KeyRound,
    title: { zh: "细粒度密钥", en: "Granular API keys" },
    body: { zh: "按角色与场景签发 API 密钥，随时吊销。", en: "Issue keys per role and scope; revoke anytime." },
  },
  {
    icon: BadgeCheck,
    title: { zh: "完整审计日志", en: "Full audit logs" },
    body: { zh: "每一次发送与计费操作留痕，可追溯可导出。", en: "Every send and billing action is logged, traceable, exportable." },
  },
  {
    icon: Database,
    title: { zh: "高可用与备份", en: "HA & backups" },
    body: { zh: "多副本与定时备份，故障自动切换不丢数据。", en: "Replicas and scheduled backups with automatic failover." },
  },
];

const BADGES = ["SOC 2 Type II", "GDPR", "ISO 27001", "PCI-Ready"];

export function Security({ locale }: { locale: Locale }) {
  return (
    <section className="bg-neutral-950 text-neutral-100">
      <div className="mx-auto max-w-7xl px-5 py-20 sm:px-8 sm:py-28">
        <div className="mx-auto max-w-2xl text-center">
          <div className="text-sm font-semibold text-brand-400">{COPY.eyebrow[locale]}</div>
          <h2 className="mt-3 text-3xl font-semibold tracking-tight text-white sm:text-4xl">
            {COPY.heading[locale]}
          </h2>
          <p className="mt-4 text-pretty text-neutral-400">{COPY.sub[locale]}</p>
        </div>

        <div className="mt-14 grid gap-px overflow-hidden rounded-2xl bg-white/10 ring-1 ring-white/10 sm:grid-cols-2 lg:grid-cols-3">
          {POINTS.map((p) => {
            const Icon = p.icon;
            return (
              <div key={p.title.en} className="bg-neutral-950 p-6">
                <div className="flex size-11 items-center justify-center rounded-xl bg-brand-500/15 text-brand-400">
                  <Icon className="size-5.5" strokeWidth={1.9} />
                </div>
                <h3 className="mt-5 text-base font-semibold tracking-tight text-white">
                  {p.title[locale]}
                </h3>
                <p className="mt-2 text-sm leading-relaxed text-neutral-400">
                  {p.body[locale]}
                </p>
              </div>
            );
          })}
        </div>

        <div className="mt-12 flex flex-wrap items-center justify-center gap-3">
          {BADGES.map((b) => (
            <span
              key={b}
              className="inline-flex items-center gap-2 rounded-full border border-white/15 bg-white/5 px-4 py-2 text-sm font-medium text-neutral-200"
            >
              <BadgeCheck className="size-4 text-brand-400" />
              {b}
            </span>
          ))}
        </div>
      </div>
    </section>
  );
}
