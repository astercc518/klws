"use client";

import { Button } from "@/components/ui/button";
import { useLocale, useT } from "@/components/locale-provider";

/** Ghost icon-button that flips zh <-> en. Shows the language you'd switch TO. */
export function LanguageToggle() {
  const { locale, toggle } = useLocale();
  const t = useT();
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      aria-label={t("toggle.language")}
      onClick={toggle}
      className="font-mono text-xs font-semibold text-muted-foreground"
    >
      {locale === "zh" ? "EN" : "中"}
    </Button>
  );
}
