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
