import type { Locale } from "@/lib/i18n";
import { common } from "./common";

/** Every message namespace. Later phases push their dicts here
 *  (e.g. admin, dashboard, sales). */
const namespaces: Record<Locale, Record<string, string>>[] = [common];

/** All namespaces merged per locale into a single flat lookup. */
export const messages: Record<Locale, Record<string, string>> = {
  zh: Object.assign({}, ...namespaces.map((n) => n.zh)),
  en: Object.assign({}, ...namespaces.map((n) => n.en)),
};
