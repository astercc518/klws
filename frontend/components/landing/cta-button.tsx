import Link from "next/link";
import { cn } from "@/lib/utils";

type Variant = "primary" | "outline" | "ghost" | "onColor";
type Size = "md" | "lg";

const VARIANTS: Record<Variant, string> = {
  primary:
    "bg-brand-600 text-white hover:bg-brand-700 shadow-sm shadow-brand-600/25",
  outline:
    "border border-border bg-background text-foreground hover:bg-muted",
  ghost: "text-foreground hover:bg-muted",
  // For use on a saturated/colored background (e.g. the final CTA band).
  onColor: "bg-white text-brand-700 hover:bg-white/90 shadow-sm",
};

const SIZES: Record<Size, string> = {
  md: "h-9 px-4 text-sm",
  lg: "h-11 px-6 text-sm",
};

export function CtaButton({
  href,
  children,
  variant = "primary",
  size = "lg",
  className,
}: {
  href: string;
  children: React.ReactNode;
  variant?: Variant;
  size?: Size;
  className?: string;
}) {
  return (
    <Link
      href={href}
      className={cn(
        "inline-flex items-center justify-center gap-2 rounded-full font-medium tracking-tight whitespace-nowrap transition-colors outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 focus-visible:ring-offset-2 focus-visible:ring-offset-background",
        VARIANTS[variant],
        SIZES[size],
        className,
      )}
    >
      {children}
    </Link>
  );
}
