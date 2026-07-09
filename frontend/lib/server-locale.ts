import { cookies, headers } from "next/headers";
import {
  LOCALE_COOKIE,
  isLocale,
  localeFromAcceptLanguage,
  type Locale,
} from "@/lib/i18n";

/** Resolve the active locale on the server. Next 16's cookies()/headers() are
 *  async. An explicit locale cookie wins; first-time visitors (no cookie) fall
 *  back to their browser's Accept-Language so the very first render is already
 *  in their language — no client-side flash. */
export async function getLocale(): Promise<Locale> {
  const cookieStore = await cookies();
  const cookieVal = cookieStore.get(LOCALE_COOKIE)?.value;
  if (isLocale(cookieVal)) return cookieVal;

  const headerStore = await headers();
  return localeFromAcceptLanguage(headerStore.get("accept-language"));
}
