import { cn } from "@/lib/utils";

/** The four canonical status tones used across every admin table.
 *  positive=green, negative=red, warning=amber, neutral=gray. Use these
 *  consistently so a green dot always means the same thing site-wide. */
export type StatusTone = "positive" | "negative" | "warning" | "neutral";

const TONE: Record<StatusTone, { wrap: string; dot: string }> = {
  positive: {
    wrap: "text-emerald-700 ring-emerald-600/20 bg-emerald-500/10 dark:text-emerald-400",
    dot: "bg-emerald-500",
  },
  negative: {
    wrap: "text-rose-700 ring-rose-600/20 bg-rose-500/10 dark:text-rose-400",
    dot: "bg-rose-500",
  },
  warning: {
    wrap: "text-amber-700 ring-amber-600/20 bg-amber-500/10 dark:text-amber-400",
    dot: "bg-amber-500",
  },
  neutral: {
    wrap: "text-muted-foreground ring-foreground/10 bg-muted",
    dot: "bg-muted-foreground/50",
  },
};

export function StatusBadge({
  tone,
  children,
  className,
}: {
  tone: StatusTone;
  children: React.ReactNode;
  className?: string;
}) {
  const t = TONE[tone];
  return (
    <span
      className={cn(
        "inline-flex h-5.5 w-fit items-center gap-1.5 rounded-full px-2 text-xs font-medium ring-1 ring-inset",
        t.wrap,
        className,
      )}
    >
      <span className={cn("size-1.5 shrink-0 rounded-full", t.dot)} />
      {children}
    </span>
  );
}
