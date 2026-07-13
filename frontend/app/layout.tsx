import type { Metadata } from "next";
import { Geist, Geist_Mono, Noto_Sans_SC } from "next/font/google";
import { Toaster } from "@/components/ui/sonner";
import { ThemeProvider } from "@/components/theme-provider";
import { LocaleProvider } from "@/components/locale-provider";
import { getLocale } from "@/lib/server-locale";
import { pick } from "@/lib/i18n";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

// Geist carries no CJK glyphs; Noto Sans SC fills them in via per-glyph fallback
// so Latin/numerals stay on Geist while Chinese text renders properly.
const notoSansSC = Noto_Sans_SC({
  variable: "--font-noto-sans-sc",
  weight: ["400", "500", "600", "700"],
  preload: false,
});

// Default <title>/description for cookie-locale routes (dashboard/admin/sales/
// auth) that don't set their own metadata. Path-routed landing pages (app/page.tsx,
// app/en/page.tsx, app/docs, app/en/docs) override this with their own static
// metadata, so this only needs to track the request's cookie locale via
// getLocale() — unlike <html lang> (see app/en's known limitation), metadata can
// be resolved per-request here since generateMetadata runs on the server too.
export async function generateMetadata(): Promise<Metadata> {
  const locale = await getLocale();
  return {
    metadataBase: new URL("https://klws.cc"),
    title: {
      default: pick(
        { zh: "klws · 企业级 WhatsApp 并发营销引擎", en: "klws · Enterprise WhatsApp Growth Engine" },
        locale,
      ),
      template: "%s · klws",
    },
    description: pick(
      {
        zh: "金融级计费、防封号隔离、高到达率 —— 为跨境营销量身打造的 WhatsApp 并发发送引擎。",
        en: "Financial-grade billing, ban-proof isolation, and high deliverability — the WhatsApp concurrency engine built for cross-border marketing.",
      },
      locale,
    ),
  };
}

export default async function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  const locale = await getLocale();
  return (
    <html
      lang={locale === "zh" ? "zh-CN" : "en"}
      suppressHydrationWarning
      className={`${geistSans.variable} ${geistMono.variable} ${notoSansSC.variable} h-full antialiased`}
    >
      <body className="min-h-full flex flex-col">
        <ThemeProvider
          attribute="class"
          defaultTheme="system"
          enableSystem
          disableTransitionOnChange
        >
          <LocaleProvider locale={locale}>
            {children}
            <Toaster />
          </LocaleProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}
