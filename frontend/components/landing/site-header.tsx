"use client";

import { useState } from "react";
import Link from "next/link";
import { Menu, X } from "lucide-react";
import type { Locale } from "@/lib/i18n";
import { localePath } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { ThemeToggle } from "@/components/theme-toggle";
import { Wordmark } from "@/components/landing/wordmark";
import { CtaButton } from "@/components/landing/cta-button";

const NAV: { href: string; label: Record<Locale, string> }[] = [
  { href: "#features", label: { zh: "功能特性", en: "Features" } },
  { href: "#use-cases", label: { zh: "解决方案", en: "Solutions" } },
  { href: "#pricing", label: { zh: "定价方案", en: "Pricing" } },
  { href: "#faq", label: { zh: "常见问题", en: "FAQ" } },
  { href: "/docs", label: { zh: "API 文档", en: "API Docs" } },
];

const T = {
  login: { zh: "登录", en: "Log in" },
  try: { zh: "免费试用", en: "Try free" },
  menu: { zh: "打开菜单", en: "Open menu" },
};

function LangSwitch({ locale }: { locale: Locale }) {
  return (
    <div className="flex items-center rounded-lg border border-border/70 p-0.5 text-xs font-medium">
      <Link
        href={localePath("zh")}
        aria-label="切换到中文"
        className={cn(
          "rounded-[6px] px-2 py-1 transition-colors",
          locale === "zh"
            ? "bg-foreground text-background"
            : "text-muted-foreground hover:text-foreground",
        )}
      >
        中
      </Link>
      <Link
        href={localePath("en")}
        aria-label="Switch to English"
        className={cn(
          "rounded-[6px] px-2 py-1 transition-colors",
          locale === "en"
            ? "bg-foreground text-background"
            : "text-muted-foreground hover:text-foreground",
        )}
      >
        EN
      </Link>
    </div>
  );
}

export function SiteHeader({ locale }: { locale: Locale }) {
  const [open, setOpen] = useState(false);
  const docsHref = locale === "en" ? "/en/docs" : "/docs";
  const hrefFor = (href: string) => (href === "/docs" ? docsHref : href);

  return (
    <header className="sticky top-0 z-50 border-b border-border/60 bg-background/80 backdrop-blur supports-[backdrop-filter]:bg-background/65">
      <div className="mx-auto flex h-16 max-w-7xl items-center justify-between gap-4 px-5 sm:px-8">
        <Link href={localePath(locale)} aria-label="klws">
          <Wordmark />
        </Link>

        <nav className="hidden items-center gap-7 lg:flex">
          {NAV.map((item) => (
            <Link
              key={item.href}
              href={hrefFor(item.href)}
              className="text-sm font-medium text-muted-foreground transition-colors hover:text-foreground"
            >
              {item.label[locale]}
            </Link>
          ))}
        </nav>

        <div className="flex items-center gap-2">
          <LangSwitch locale={locale} />
          <ThemeToggle />
          <Link
            href="/login"
            className="hidden rounded-lg px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground sm:inline-flex"
          >
            {T.login[locale]}
          </Link>
          <CtaButton href="/login" size="md" className="hidden sm:inline-flex">
            {T.try[locale]}
          </CtaButton>
          <button
            type="button"
            aria-label={T.menu[locale]}
            aria-expanded={open}
            onClick={() => setOpen((v) => !v)}
            className="inline-flex size-9 items-center justify-center rounded-lg text-muted-foreground hover:bg-muted hover:text-foreground lg:hidden"
          >
            {open ? <X className="size-5" /> : <Menu className="size-5" />}
          </button>
        </div>
      </div>

      {open && (
        <nav className="border-t border-border/60 px-5 py-4 lg:hidden">
          <ul className="space-y-1">
            {NAV.map((item) => (
              <li key={item.href}>
                <Link
                  href={hrefFor(item.href)}
                  onClick={() => setOpen(false)}
                  className="block rounded-lg px-3 py-2.5 text-sm font-medium text-muted-foreground hover:bg-muted hover:text-foreground"
                >
                  {item.label[locale]}
                </Link>
              </li>
            ))}
            <li className="flex gap-2 pt-3">
              <Link
                href="/login"
                onClick={() => setOpen(false)}
                className="inline-flex h-10 flex-1 items-center justify-center rounded-xl border border-border text-sm font-medium"
              >
                {T.login[locale]}
              </Link>
              <CtaButton href="/login" size="md" className="h-10 flex-1">
                {T.try[locale]}
              </CtaButton>
            </li>
          </ul>
        </nav>
      )}
    </header>
  );
}
