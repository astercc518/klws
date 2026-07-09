"use client";

import { createContext, useContext, useMemo } from "react";
import { useRouter } from "next/navigation";
import { LOCALE_COOKIE, type Locale } from "@/lib/i18n";
import { messages } from "@/lib/i18n/dicts";

interface LocaleContextValue {
  locale: Locale;
  setLocale: (l: Locale) => void;
  toggle: () => void;
  t: (key: string) => string;
}

const LocaleContext = createContext<LocaleContextValue | null>(null);

export function LocaleProvider({
  locale,
  children,
}: {
  locale: Locale;
  children: React.ReactNode;
}) {
  const router = useRouter();

  const value = useMemo<LocaleContextValue>(() => {
    const setLocale = (l: Locale) => {
      // Persist for the next request's SSR, then re-render server components so
      // the whole tree (including <html lang>) reflects the new locale.
      document.cookie = `${LOCALE_COOKIE}=${l}; path=/; max-age=31536000; samesite=lax`;
      router.refresh();
    };
    return {
      locale,
      setLocale,
      toggle: () => setLocale(locale === "zh" ? "en" : "zh"),
      t: (key: string) => {
        const val = messages[locale][key];
        if (val === undefined) {
          if (process.env.NODE_ENV !== "production") {
            console.warn(`[i18n] missing key "${key}" for locale "${locale}"`);
          }
          return key;
        }
        return val;
      },
    };
  }, [locale, router]);

  return <LocaleContext.Provider value={value}>{children}</LocaleContext.Provider>;
}

export function useLocale(): LocaleContextValue {
  const ctx = useContext(LocaleContext);
  if (!ctx) throw new Error("useLocale must be used within <LocaleProvider>");
  return ctx;
}

/** A translator bound to the active locale. */
export function useT(): (key: string) => string {
  return useLocale().t;
}
