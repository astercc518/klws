import { cn } from "@/lib/utils";
import { Logo } from "@/components/landing/logo";

/** Brand lockup: klws mark + wordmark. */
export function Wordmark({
  className,
  subdued = false,
}: {
  className?: string;
  subdued?: boolean;
}) {
  return (
    <span className={cn("flex items-center gap-2.5", className)}>
      <Logo className="size-7" />
      <span className="leading-tight">
        <span className="block text-[15px] font-semibold tracking-tight">
          klws
        </span>
        <span
          className={cn(
            "block font-mono text-[9px] uppercase tracking-[0.18em] text-muted-foreground",
            subdued && "text-muted-foreground/70",
          )}
        >
          WhatsApp Growth
        </span>
      </span>
    </span>
  );
}
