export type Locale = "zh" | "en";

export const LOCALES: Locale[] = ["zh", "en"];

export const DEFAULT_LOCALE: Locale = "zh";

/** Path to the landing page for a given locale. zh lives at "/", en at "/en". */
export function localePath(locale: Locale): string {
  return locale === "zh" ? "/" : "/en";
}

export function otherLocale(locale: Locale): Locale {
  return locale === "zh" ? "en" : "zh";
}

/** A string that differs by locale. */
export type I18nText = Record<Locale, string>;

/** Pick the active-locale value from a bilingual record. */
export function pick<T>(record: Record<Locale, T>, locale: Locale): T {
  return record[locale];
}

/** Cookie that persists the UI locale (frontend-only; no backend involvement). */
export const LOCALE_COOKIE = "locale";

/** Runtime guard narrowing an unknown cookie value to a Locale. */
export function isLocale(v: unknown): v is Locale {
  return v === "zh" || v === "en";
}

/** First-visit locale from an Accept-Language header value. The highest-priority
 *  tag decides: a `zh*` tag → "zh", anything else → "en"; a null/empty header →
 *  DEFAULT_LOCALE. Server-side only (the header is not available on the client). */
export function localeFromAcceptLanguage(header: string | null): Locale {
  if (!header) return DEFAULT_LOCALE;
  const first = header.split(",")[0]?.trim().toLowerCase() ?? "";
  return first.startsWith("zh") ? "zh" : "en";
}
