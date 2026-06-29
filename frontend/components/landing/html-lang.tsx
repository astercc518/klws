"use client";

import { useEffect } from "react";
import type { Locale } from "@/lib/i18n";

/** Sets <html lang> to match the page locale (root layout defaults to zh-CN). */
export function HtmlLang({ locale }: { locale: Locale }) {
  useEffect(() => {
    document.documentElement.lang = locale === "en" ? "en" : "zh-CN";
  }, [locale]);
  return null;
}
