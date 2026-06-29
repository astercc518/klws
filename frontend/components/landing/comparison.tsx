import { Check, X, Minus } from "lucide-react";
import type { Locale } from "@/lib/i18n";

const COPY = {
  eyebrow: { zh: "为什么选 klws", en: "Why klws" },
  heading: { zh: "一张表，看清差距", en: "One table, clear difference" },
  sub: {
    zh: "把发送、计费、合规与运维加在一起，klws 才是真正的总成本最优解。",
    en: "Add up sending, billing, compliance, and ops — klws is the real lowest-total-cost choice.",
  },
  colHead: { zh: "能力对比", en: "Capability" },
  tool: { zh: "普通群发工具", en: "Generic blaster" },
  diy: { zh: "自建团队", en: "In-house build" },
};

type Cell =
  | { kind: "yes"; note?: Record<Locale, string> }
  | { kind: "no" }
  | { kind: "partial"; note?: Record<Locale, string> }
  | { kind: "text"; text: Record<Locale, string>; strong?: boolean };

const basic: Record<Locale, string> = { zh: "基础", en: "Basic" };
const needs: Record<Locale, string> = { zh: "需自研", en: "DIY" };

const ROWS: { label: Record<Locale, string>; klws: Cell; tool: Cell; diy: Cell }[] = [
  { label: { zh: "动态防封代理矩阵", en: "Dynamic anti-ban proxy matrix" }, klws: { kind: "yes" }, tool: { kind: "partial", note: basic }, diy: { kind: "no" } },
  { label: { zh: "按条计费 / 失败秒退款", en: "Per-message billing / auto-refund" }, klws: { kind: "yes" }, tool: { kind: "no" }, diy: { kind: "partial", note: needs } },
  { label: { zh: "百万级并发调度", en: "Million-scale concurrency" }, klws: { kind: "yes" }, tool: { kind: "partial" }, diy: { kind: "no" } },
  { label: { zh: "Spintax 多变体混淆", en: "Spintax obfuscation" }, klws: { kind: "yes" }, tool: { kind: "partial" }, diy: { kind: "no" } },
  { label: { zh: "完整 API 与 100+ 集成", en: "Full API & 100+ integrations" }, klws: { kind: "yes" }, tool: { kind: "no" }, diy: { kind: "partial", note: needs } },
  { label: { zh: "多租户隔离与合规", en: "Multi-tenant isolation & compliance" }, klws: { kind: "yes" }, tool: { kind: "no" }, diy: { kind: "partial" } },
  { label: { zh: "上线时间", en: "Time to launch" }, klws: { kind: "text", text: { zh: "当天", en: "Same day" }, strong: true }, tool: { kind: "text", text: { zh: "数天", en: "Days" } }, diy: { kind: "text", text: { zh: "数月", en: "Months" } } },
  { label: { zh: "运维成本", en: "Ops cost" }, klws: { kind: "text", text: { zh: "免运维", en: "Zero ops" }, strong: true }, tool: { kind: "text", text: { zh: "中", en: "Medium" } }, diy: { kind: "text", text: { zh: "高", en: "High" } } },
];

function CellView({ cell, locale }: { cell: Cell; locale: Locale }) {
  if (cell.kind === "yes")
    return (
      <span className="inline-flex items-center justify-center gap-1.5">
        <Check className="size-5 text-emerald-600 dark:text-emerald-500" />
        {cell.note && <span className="text-xs text-muted-foreground">{cell.note[locale]}</span>}
      </span>
    );
  if (cell.kind === "no")
    return <X className="mx-auto size-5 text-muted-foreground/40" />;
  if (cell.kind === "partial")
    return (
      <span className="inline-flex items-center justify-center gap-1.5">
        <Minus className="size-5 text-amber-500" />
        {cell.note && <span className="text-xs text-muted-foreground">{cell.note[locale]}</span>}
      </span>
    );
  return (
    <span
      className={
        cell.strong
          ? "text-sm font-semibold text-emerald-600 dark:text-emerald-500"
          : "text-sm text-muted-foreground"
      }
    >
      {cell.text[locale]}
    </span>
  );
}

export function Comparison({ locale }: { locale: Locale }) {
  return (
    <section className="border-t border-border/60 bg-muted/30">
      <div className="mx-auto max-w-5xl px-5 py-20 sm:px-8 sm:py-28">
        <div className="mx-auto max-w-2xl text-center">
          <div className="text-sm font-semibold text-brand-600 dark:text-brand-400">
            {COPY.eyebrow[locale]}
          </div>
          <h2 className="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">
            {COPY.heading[locale]}
          </h2>
          <p className="mt-4 text-pretty text-muted-foreground">{COPY.sub[locale]}</p>
        </div>

        <div className="mt-12 overflow-x-auto">
          <table className="w-full min-w-[640px] border-separate border-spacing-0">
            <thead>
              <tr>
                <th className="w-2/5 px-5 py-4 text-left text-sm font-medium text-muted-foreground">
                  {COPY.colHead[locale]}
                </th>
                <th className="rounded-t-2xl bg-brand-500/10 px-5 py-4 text-center text-sm font-semibold text-brand-700 ring-1 ring-brand-500/30 dark:text-brand-400">
                  klws
                </th>
                <th className="px-5 py-4 text-center text-sm font-medium text-muted-foreground">
                  {COPY.tool[locale]}
                </th>
                <th className="px-5 py-4 text-center text-sm font-medium text-muted-foreground">
                  {COPY.diy[locale]}
                </th>
              </tr>
            </thead>
            <tbody>
              {ROWS.map((row, i) => (
                <tr key={row.label.en}>
                  <td className="border-t border-border/60 px-5 py-4 text-sm font-medium">
                    {row.label[locale]}
                  </td>
                  <td
                    className={`border-t border-brand-500/20 bg-brand-500/10 px-5 py-4 text-center ring-1 ring-brand-500/30 ${
                      i === ROWS.length - 1 ? "rounded-b-2xl" : ""
                    }`}
                  >
                    <CellView cell={row.klws} locale={locale} />
                  </td>
                  <td className="border-t border-border/60 px-5 py-4 text-center">
                    <CellView cell={row.tool} locale={locale} />
                  </td>
                  <td className="border-t border-border/60 px-5 py-4 text-center">
                    <CellView cell={row.diy} locale={locale} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  );
}
