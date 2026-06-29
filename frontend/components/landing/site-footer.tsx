import Link from "next/link";
import type { Locale } from "@/lib/i18n";
import { Wordmark } from "@/components/landing/wordmark";

const COLUMNS: { title: Record<Locale, string>; links: { label: Record<Locale, string>; href: string }[] }[] = [
  {
    title: { zh: "产品", en: "Product" },
    links: [
      { label: { zh: "功能特性", en: "Features" }, href: "#features" },
      { label: { zh: "解决方案", en: "Solutions" }, href: "#use-cases" },
      { label: { zh: "定价方案", en: "Pricing" }, href: "#pricing" },
      { label: { zh: "API 文档", en: "API Docs" }, href: "/docs" },
      { label: { zh: "登录控制台", en: "Log in" }, href: "/login" },
    ],
  },
  {
    title: { zh: "解决方案", en: "Solutions" },
    links: [
      { label: { zh: "营销获客", en: "Marketing" }, href: "#use-cases" },
      { label: { zh: "销售转化", en: "Sales" }, href: "#use-cases" },
      { label: { zh: "客户服务", en: "Support" }, href: "#use-cases" },
      { label: { zh: "集成生态", en: "Integrations" }, href: "#" },
    ],
  },
  {
    title: { zh: "公司", en: "Company" },
    links: [
      { label: { zh: "关于我们", en: "About" }, href: "#" },
      { label: { zh: "联系销售", en: "Contact sales" }, href: "#" },
      { label: { zh: "服务状态", en: "Status" }, href: "#" },
      { label: { zh: "博客", en: "Blog" }, href: "#" },
    ],
  },
  {
    title: { zh: "法律", en: "Legal" },
    links: [
      { label: { zh: "服务条款", en: "Terms" }, href: "#" },
      { label: { zh: "隐私政策", en: "Privacy" }, href: "#" },
      { label: { zh: "合规使用", en: "Acceptable use" }, href: "#" },
      { label: { zh: "数据处理", en: "Data processing" }, href: "#" },
    ],
  },
];

const T = {
  tagline: {
    zh: "企业级 WhatsApp 并发营销引擎，为跨境营销量身打造。",
    en: "Enterprise WhatsApp growth engine, built for cross-border marketing.",
  },
  rights: { zh: "© 2026 klws.cc · All rights reserved.", en: "© 2026 klws.cc · All rights reserved." },
  disclaimer: {
    zh: "页面所示数据与品牌名称均为示例，用于展示产品能力。",
    en: "All figures and brand names shown are illustrative, for product demonstration.",
  },
};

export function SiteFooter({ locale }: { locale: Locale }) {
  const hrefFor = (href: string) =>
    href === "/docs" && locale === "en" ? "/en/docs" : href;
  return (
    <footer className="border-t border-border/60 bg-muted/30">
      <div className="mx-auto max-w-7xl px-5 py-16 sm:px-8">
        <div className="grid gap-10 lg:grid-cols-[1.4fr_1fr_1fr_1fr_1fr]">
          <div>
            <Wordmark />
            <p className="mt-4 max-w-xs text-sm text-muted-foreground">
              {T.tagline[locale]}
            </p>
          </div>
          {COLUMNS.map((col) => (
            <div key={col.title.en}>
              <div className="text-sm font-semibold">{col.title[locale]}</div>
              <ul className="mt-4 space-y-2.5">
                {col.links.map((link) => (
                  <li key={link.label.en}>
                    <Link
                      href={hrefFor(link.href)}
                      className="text-sm text-muted-foreground transition-colors hover:text-foreground"
                    >
                      {link.label[locale]}
                    </Link>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>

        <div className="mt-14 flex flex-col items-center justify-between gap-4 border-t border-border/60 pt-8 sm:flex-row">
          <p className="font-mono text-xs text-muted-foreground">{T.rights[locale]}</p>
          <p className="text-xs text-muted-foreground/70">{T.disclaimer[locale]}</p>
        </div>
      </div>
    </footer>
  );
}
