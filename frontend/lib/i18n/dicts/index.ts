import type { Locale } from "@/lib/i18n";
import { common } from "./common";
import { admin } from "./admin";
import { dashboard } from "./dashboard";
import { sales } from "./sales";
import { auth } from "./auth";

/** Every message namespace. */
const namespaces: Record<Locale, Record<string, string>>[] = [
  common,
  admin,
  dashboard,
  sales,
  auth,
];

/** All namespaces merged per locale into a single flat lookup. */
export const messages: Record<Locale, Record<string, string>> = {
  zh: Object.assign({}, ...namespaces.map((n) => n.zh)),
  en: Object.assign({}, ...namespaces.map((n) => n.en)),
};
