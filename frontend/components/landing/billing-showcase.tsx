import { Check, ArrowRight } from "lucide-react";
import type { Locale } from "@/lib/i18n";
import { CtaButton } from "@/components/landing/cta-button";

const COPY = {
  eyebrow: { zh: "金融级财务闭环", en: "Financial-grade billing" },
  heading: { zh: "每一分预算，都对得平账", en: "Every cent of budget, fully reconciled" },
  sub: {
    zh: "大多数群发工具只管发，不管钱。klws 把计费做成了一套可审计的闭环引擎。",
    en: "Most blasters only send — they don't account. klws turns billing into an auditable closed-loop engine.",
  },
  cta: { zh: "查看计费文档", en: "Billing docs" },
  points: {
    zh: ["发送前预扣、异步对账，杜绝超发与漏计", "按条精确计价，多国多档单价灵活配置", "失败消息 5 秒内自动退回额度", "流水可导出，对得平财务的每一分钱"],
    en: ["Pre-auth holds and async reconciliation prevent over- or under-billing", "Per-message pricing with flexible multi-country tiers", "Failed messages refunded within 5 seconds", "Exportable ledger that balances to the cent"],
  },
};

const M = {
  ledger: { zh: "klws · 账单流水", en: "klws · Ledger" },
  balance: { zh: "钱包余额", en: "Wallet balance" },
  recon: { zh: "本月对账", en: "This month" },
  reconciled: { zh: "已平账", en: "Reconciled" },
};

const ROWS: { t: Record<Locale, string>; k: Record<Locale, string>; v: string; c: string }[] = [
  { t: { zh: "群发活动 · 黑五", en: "Campaign · Black Friday" }, k: { zh: "成功扣费", en: "Charged" }, v: "-$4,155.00", c: "text-foreground" },
  { t: { zh: "失败自动退款", en: "Auto-refund" }, k: { zh: "退回额度", en: "Credited" }, v: "+$83.20", c: "text-emerald-600 dark:text-emerald-500" },
  { t: { zh: "钱包充值", en: "Top-up" }, k: { zh: "入账", en: "Deposit" }, v: "+$5,000.00", c: "text-emerald-600 dark:text-emerald-500" },
  { t: { zh: "群发活动 · 复购召回", en: "Campaign · Win-back" }, k: { zh: "成功扣费", en: "Charged" }, v: "-$1,290.50", c: "text-foreground" },
];

export function BillingShowcase({ locale }: { locale: Locale }) {
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
            {COPY.points[locale].map((p) => (
              <li key={p} className="flex items-start gap-3 text-sm sm:text-base">
                <Check className="mt-0.5 size-5 shrink-0 text-emerald-600 dark:text-emerald-500" />
                <span className="text-muted-foreground">{p}</span>
              </li>
            ))}
          </ul>
          <div className="mt-8">
            <CtaButton href="/login" variant="outline" size="md">
              {COPY.cta[locale]}
              <ArrowRight className="size-4" />
            </CtaButton>
          </div>
        </div>

        <div className="relative">
          <div
            aria-hidden
            className="absolute -inset-4 -z-10 rounded-[28px] bg-gradient-to-tr from-emerald-500/10 via-transparent to-blue-500/10 blur-2xl"
          />
          <div className="overflow-hidden rounded-2xl border border-border/70 bg-card shadow-2xl shadow-foreground/10 ring-1 ring-foreground/5">
            <div className="flex items-center gap-2 border-b border-border/60 bg-muted/40 px-4 py-3">
              <span className="size-2.5 rounded-full bg-rose-400/80" />
              <span className="size-2.5 rounded-full bg-amber-400/80" />
              <span className="size-2.5 rounded-full bg-emerald-400/80" />
              <span className="ml-3 font-mono text-[11px] text-muted-foreground">
                {M.ledger[locale]}
              </span>
            </div>

            <div className="p-5">
              <div className="flex items-end justify-between rounded-xl border border-border/60 bg-gradient-to-br from-emerald-500/10 to-transparent p-4">
                <div>
                  <div className="font-mono text-[10px] uppercase tracking-[0.16em] text-muted-foreground">
                    {M.balance[locale]}
                  </div>
                  <div className="mt-1 text-3xl font-semibold tracking-tight tabular-nums">
                    $12,480<span className="text-lg text-muted-foreground">.50</span>
                  </div>
                </div>
                <div className="text-right">
                  <div className="font-mono text-[10px] uppercase tracking-[0.16em] text-muted-foreground">
                    {M.recon[locale]}
                  </div>
                  <div className="mt-1 inline-flex items-center gap-1 text-sm font-medium text-emerald-600 dark:text-emerald-500">
                    <Check className="size-4" /> {M.reconciled[locale]}
                  </div>
                </div>
              </div>

              <div className="mt-4 space-y-1">
                {ROWS.map((r) => (
                  <div
                    key={r.t.en}
                    className="flex items-center justify-between rounded-lg px-2 py-2.5 text-sm hover:bg-muted/50"
                  >
                    <div className="min-w-0">
                      <div className="truncate font-medium">{r.t[locale]}</div>
                      <div className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
                        {r.k[locale]}
                      </div>
                    </div>
                    <div className={`font-mono tabular-nums ${r.c}`}>{r.v}</div>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
