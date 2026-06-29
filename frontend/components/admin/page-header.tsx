import { cn } from "@/lib/utils";

interface PageHeaderProps {
  /** Optional mono eyebrow above the title (e.g. "Billing & IAM"). */
  eyebrow?: string;
  title: string;
  /** Muted one-line description of what this page is for. */
  description?: string;
  /** Right-aligned action slot — typically the page's primary Button. */
  actions?: React.ReactNode;
  className?: string;
}

/** The standard page head: eyebrow + large title + description on the left,
 *  primary actions on the right. Used at the top of every admin page. */
export function PageHeader({ eyebrow, title, description, actions, className }: PageHeaderProps) {
  return (
    <div
      className={cn(
        "flex flex-col gap-4 pb-6 sm:flex-row sm:items-end sm:justify-between",
        className,
      )}
    >
      <div className="min-w-0 space-y-1">
        {eyebrow && (
          <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
            {eyebrow}
          </div>
        )}
        <h1 className="text-2xl font-semibold tracking-tight text-foreground">{title}</h1>
        {description && (
          <p className="max-w-2xl text-sm text-muted-foreground">{description}</p>
        )}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  );
}
