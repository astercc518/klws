import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import type { Accent } from "@/components/admin/stat-card";

// Solid circle palette for identity monograms — same lively colored avatars the
// landing pipeline mock uses, so a customer row here feels like that surface.
const SOLID = [
  "bg-brand-600",
  "bg-emerald-600",
  "bg-teal-600",
  "bg-blue-600",
  "bg-rose-500",
  "bg-amber-500",
];

const TILE: Record<Accent, string> = {
  brand: "bg-brand-500/10 text-brand-600 dark:text-brand-400",
  emerald: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
  blue: "bg-blue-500/10 text-blue-600 dark:text-blue-400",
  rose: "bg-rose-500/10 text-rose-600 dark:text-rose-400",
  amber: "bg-amber-500/10 text-amber-600 dark:text-amber-400",
  neutral: "bg-muted text-muted-foreground",
};

function hue(text: string): string {
  let h = 0;
  for (let i = 0; i < text.length; i++) h = (h * 31 + text.charCodeAt(i)) >>> 0;
  return SOLID[h % SOLID.length];
}

/** RowAvatar: pass `text` for a colored identity monogram (color is stable per
 *  string), or `icon`+`accent` for a soft tinted resource tile. */
export function RowAvatar({
  text,
  icon: Icon,
  accent = "neutral",
  className,
}: {
  text?: string;
  icon?: LucideIcon;
  accent?: Accent;
  className?: string;
}) {
  if (Icon && !text) {
    return (
      <span
        className={cn(
          "flex size-8 shrink-0 items-center justify-center rounded-lg",
          TILE[accent],
          className,
        )}
        aria-hidden
      >
        <Icon className="size-4" strokeWidth={1.9} />
      </span>
    );
  }
  const initial = (text ?? "?").trim().charAt(0).toUpperCase() || "?";
  return (
    <span
      className={cn(
        "flex size-8 shrink-0 items-center justify-center rounded-full text-xs font-semibold text-white",
        hue(text ?? "?"),
        className,
      )}
      aria-hidden
    >
      {initial}
    </span>
  );
}
