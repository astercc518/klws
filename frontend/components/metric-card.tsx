import type { LucideIcon } from "lucide-react";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

interface MetricCardProps {
  label: string;
  value: string;
  sub?: string;
  icon: LucideIcon;
  /** Inverted black card — reserved for the single hero metric (balance). */
  hero?: boolean;
  /** Optional leading status node (e.g. a live pulse dot). */
  status?: React.ReactNode;
}

export function MetricCard({ label, value, sub, icon: Icon, hero, status }: MetricCardProps) {
  return (
    <Card
      className={cn(
        "gap-0 p-5",
        hero && "border-foreground bg-foreground text-background",
      )}
    >
      <div className="flex items-center justify-between">
        <span
          className={cn(
            "font-mono text-[10px] uppercase tracking-[0.18em]",
            hero ? "text-background/60" : "text-muted-foreground",
          )}
        >
          {label}
        </span>
        <Icon className={cn("size-4", hero ? "text-background/70" : "text-muted-foreground")} strokeWidth={1.75} />
      </div>

      <div className="mt-4 flex items-baseline gap-2">
        <span className="font-mono text-3xl font-semibold tabular-nums tracking-tight">
          {value}
        </span>
        {status}
      </div>

      {sub && (
        <div
          className={cn(
            "mt-1.5 font-mono text-xs tabular-nums",
            hero ? "text-background/55" : "text-muted-foreground",
          )}
        >
          {sub}
        </div>
      )}
    </Card>
  );
}
