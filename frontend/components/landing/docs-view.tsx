import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import type { Locale } from "@/lib/i18n";
import { localePath } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { buttonVariants } from "@/components/ui/button";
import { SiteHeader } from "@/components/landing/site-header";
import { SiteFooter } from "@/components/landing/site-footer";
import { HtmlLang } from "@/components/landing/html-lang";

const T = {
  eyebrow: { zh: "Developer · API Reference", en: "Developer · API Reference" },
  heading: { zh: "API 文档即将上线", en: "API docs are coming soon" },
  body: {
    zh: "完整的发送、计费与回执 API 参考正在整理中。需要抢先接入？登录控制台后即可在「开发者」面板生成密钥。",
    en: "The full send, billing, and receipt API reference is in the works. Want early access? Log in and generate a key from the Developers panel.",
  },
  home: { zh: "返回首页", en: "Back to home" },
  login: { zh: "登录控制台", en: "Log in" },
};

export function DocsView({ locale }: { locale: Locale }) {
  return (
    <div className="flex min-h-screen flex-col">
      <HtmlLang locale={locale} />
      <SiteHeader locale={locale} />
      <main className="flex flex-1 items-center justify-center px-6 py-24">
        <div className="max-w-md text-center">
          <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
            {T.eyebrow[locale]}
          </div>
          <h1 className="mt-3 text-3xl font-semibold tracking-tight">
            {T.heading[locale]}
          </h1>
          <p className="mt-4 text-pretty text-muted-foreground">{T.body[locale]}</p>
          <div className="mt-8 flex items-center justify-center gap-3">
            <Link
              href={localePath(locale)}
              className={cn(buttonVariants({ variant: "outline", size: "lg" }), "h-10")}
            >
              <ArrowLeft className="size-4" />
              {T.home[locale]}
            </Link>
            <Link
              href="/login"
              className={cn(buttonVariants({ size: "lg" }), "h-10")}
            >
              {T.login[locale]}
            </Link>
          </div>
        </div>
      </main>
      <SiteFooter locale={locale} />
    </div>
  );
}
