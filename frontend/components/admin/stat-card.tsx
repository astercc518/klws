import type { LucideIcon } from "lucide-react";
import { ArrowUpRight, ArrowDownRight, Minus } from "lucide-react";
import Link from "next/link";
import { cn } from "@/lib/utils";

/** Accent hues — same family the landing Features/Stats use, so a metric tile
 *  here reads as the same product surface as a feature card there. */
export type Accent = "brand" | "emerald" | "blue" | "rose" | "amber" | "neutral";

const TILE: Record<Accent, string> = {
  brand: "bg-brand-500/10 text-brand-600 dark:text-brand-400",
  emerald: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
  blue: "bg-blue-500/10 text-blue-600 dark:text-blue-400",
  rose: "bg-rose-500/10 text-rose-600 dark:text-rose-400",
  amber: "bg-amber-500/10 text-amber-600 dark:text-amber-400",
  neutral: "bg-muted text-muted-foreground",
};

export interface Trend {
  /** Display string for the change, e.g. "12.4%" or "+1.2k". */
  value: string;
  direction: "up" | "down" | "flat";
  /** Color intent. Defaults from direction (up→positive, down→negative). */
  intent?: "positive" | "negative" | "neutral";
}

export interface StatCardProps {
  label: string;
  value: string;
  /** Secondary context line under the value (e.g. denominator, total). */
  sub?: string;
  icon: LucideIcon;
  accent?: Accent;
  trend?: Trend;
  /** When set, the whole tile becomes a link to this route and shows a
   *  drill-down arrow (only if no trend chip occupies the top-right). */
  href?: string;
}

const TREND_ICON = { up: ArrowUpRight, down: ArrowDownRight, flat: Minus } as const;

function trendIntent(t: Trend): "positive" | "negative" | "neutral" {
  if (t.intent) return t.intent;
  if (t.direction === "up") return "positive";
  if (t.direction === "down") return "negative";
  return "neutral";
}

export function StatCard({ label, value, sub, icon: Icon, accent = "neutral", trend, href }: StatCardProps) {
  const intent = trend ? trendIntent(trend) : "neutral";
  const TrendIcon = trend ? TREND_ICON[trend.direction] : null;

  const inner = (
    <>
      <div className="flex items-start justify-between gap-3">
        <span className={cn("flex size-10 items-center justify-center rounded-xl", TILE[accent])}>
          <Icon className="size-5" strokeWidth={1.9} />
        </span>
        {trend && TrendIcon ? (
          <span
            className={cn(
              "inline-flex items-center gap-0.5 rounded-full px-2 py-0.5 font-mono text-xs font-medium tabular-nums ring-1 ring-inset",
              intent === "positive"
                ? "text-emerald-700 ring-emerald-600/20 dark:text-emerald-400"
                : intent === "negative"
                  ? "text-rose-700 ring-rose-600/20 dark:text-rose-400"
                  : "text-muted-foreground ring-foreground/10",
            )}
          >
            <TrendIcon className="size-3" strokeWidth={2.25} />
            {trend.value}
          </span>
        ) : href ? (
          <ArrowUpRight className="size-4 text-muted-foreground/50 transition-colors group-hover:text-foreground" />
        ) : null}
      </div>

      <div className="mt-4 font-mono text-3xl font-semibold tabular-nums tracking-tight">{value}</div>
      <div className="mt-1 text-sm font-medium text-foreground/90">{label}</div>
      {sub && <div className="mt-0.5 font-mono text-xs tabular-nums text-muted-foreground">{sub}</div>}
    </>
  );

  const base = "rounded-2xl border border-border/70 bg-card p-5 shadow-sm ring-1 ring-foreground/5 transition-shadow hover:shadow-md";

  if (href) {
    return (
      <Link href={href} className={cn(base, "group block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring")}>
        {inner}
      </Link>
    );
  }
  return <div className={base}>{inner}</div>;
}

/** Responsive grid wrapper for a row of StatCards. Defaults to 4 columns. */
export function MetricCardGroup({
  children,
  columns = 4,
  className,
}: {
  children: React.ReactNode;
  columns?: 2 | 3 | 4;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "grid grid-cols-1 gap-4 sm:grid-cols-2",
        columns === 3 && "lg:grid-cols-3",
        columns === 4 && "xl:grid-cols-4",
        className,
      )}
    >
      {children}
    </div>
  );
}
