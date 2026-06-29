import { Plus } from "lucide-react";
import type { Locale } from "@/lib/i18n";

const COPY = {
  eyebrow: { zh: "常见问题", en: "FAQ" },
  heading: { zh: "还有疑问？", en: "Still have questions?" },
};

const FAQS: { q: Record<Locale, string>; a: Record<Locale, string> }[] = [
  {
    q: { zh: "klws 是如何降低封号风险的？", en: "How does klws reduce ban risk?" },
    a: {
      zh: "我们提供动态高匿代理池、按账号画像的智能发送频率控制，以及 Spintax 多变体文案混淆。三者叠加，从网络指纹、发送节奏到内容差异化全方位隔离风控触发点。",
      en: "We combine dynamic elite proxy pools, profile-based rate control, and Spintax multi-variant obfuscation — isolating risk triggers across network fingerprint, sending cadence, and content variation.",
    },
  },
  {
    q: { zh: "计费方式是怎样的？发送失败会扣费吗？", en: "How does billing work? Are failed sends charged?" },
    a: {
      zh: "按条精确计费，发送前预扣、异步对账。任何未成功送达的消息都会在 5 秒内自动退回额度，账目实时可查、可导出，对得平财务的每一分钱。",
      en: "Per-message billing with pre-auth holds and async reconciliation. Any undelivered message is auto-refunded within 5 seconds, with a real-time, exportable ledger that balances to the cent.",
    },
  },
  {
    q: { zh: "接入需要多久？有 API 吗？", en: "How long does integration take? Is there an API?" },
    a: {
      zh: "Pro 及以上套餐开放完整 REST API 与 Webhook，配合清晰的文档，通常半天即可完成接入。也支持 Shopify、HubSpot、Zapier 等 100+ 应用的即插即用集成。",
      en: "Pro and above expose a full REST API and Webhooks with clear docs — typically half a day to integrate. We also support 100+ plug-and-play apps like Shopify, HubSpot, and Zapier.",
    },
  },
  {
    q: { zh: "能支撑多大的并发？", en: "What concurrency can it handle?" },
    a: {
      zh: "调度层基于 Asynq 深度优化，集群节点可平滑横向扩展，峰值可达每分钟百万级消息吞吐，且在高并发下保持有序投递与稳定到达率。",
      en: "The dispatch layer is Asynq-tuned and scales horizontally, reaching millions of messages per minute at peak while keeping ordered delivery and stable deliverability.",
    },
  },
  {
    q: { zh: "数据安全与合规如何保障？", en: "How is data security and compliance handled?" },
    a: {
      zh: "多租户 RLS 隔离，传输与存储全程加密。Enterprise 套餐支持私有化 / VPC 部署与独立合规策略，满足跨境业务的数据驻留要求。",
      en: "Multi-tenant RLS isolation with encryption in transit and at rest. Enterprise supports private / VPC deployment and custom compliance to meet cross-border data residency needs.",
    },
  },
];

export function Faq({ locale }: { locale: Locale }) {
  return (
    <section id="faq" className="scroll-mt-20 border-t border-border/60">
      <div className="mx-auto max-w-3xl px-5 py-20 sm:px-8 sm:py-28">
        <div className="text-center">
          <div className="text-sm font-semibold text-brand-600 dark:text-brand-400">
            {COPY.eyebrow[locale]}
          </div>
          <h2 className="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">
            {COPY.heading[locale]}
          </h2>
        </div>

        <div className="mt-12 divide-y divide-border/60 border-y border-border/60">
          {FAQS.map((item) => (
            <details key={item.q.en} className="group py-5">
              <summary className="flex cursor-pointer list-none items-center justify-between gap-4 text-left text-base font-medium">
                {item.q[locale]}
                <Plus className="size-5 shrink-0 text-muted-foreground transition-transform duration-200 group-open:rotate-45" />
              </summary>
              <p className="mt-3 pr-9 text-sm leading-relaxed text-muted-foreground">
                {item.a[locale]}
              </p>
            </details>
          ))}
        </div>
      </div>
    </section>
  );
}
