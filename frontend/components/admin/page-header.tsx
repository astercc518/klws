"use client";

import { cn } from "@/lib/utils";
import { pick, type I18nText } from "@/lib/i18n";
import { useLocale } from "@/components/locale-provider";

interface PageHeaderProps {
  /** Optional mono eyebrow above the title (e.g. "Billing & IAM"). Not translated. */
  eyebrow?: string;
  /** Plain string, or a { zh, en } record resolved against the active locale. */
  title: string | I18nText;
  /** Muted one-line description; plain string or { zh, en } record. */
  description?: string | I18nText;
  /** Right-aligned action slot — typically the page's primary Button. */
  actions?: React.ReactNode;
  className?: string;
}

/** The standard page head: eyebrow + large title + description on the left,
 *  primary actions on the right. Used at the top of every admin page. */
export function PageHeader({ eyebrow, title, description, actions, className }: PageHeaderProps) {
  const { locale } = useLocale();
  const resolve = (v: string | I18nText) => (typeof v === "string" ? v : pick(v, locale));

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
        <h1 className="text-2xl font-semibold tracking-tight text-foreground">
          {resolve(title)}
        </h1>
        {description && (
          <p className="max-w-2xl text-sm text-muted-foreground">{resolve(description)}</p>
        )}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  );
}
