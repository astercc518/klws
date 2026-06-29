import type { Metadata } from "next";
import { Geist, Geist_Mono, Noto_Sans_SC } from "next/font/google";
import { Toaster } from "@/components/ui/sonner";
import { ThemeProvider } from "@/components/theme-provider";
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

export const metadata: Metadata = {
  metadataBase: new URL("https://klws.cc"),
  title: {
    default: "klws · 企业级 WhatsApp 并发营销引擎",
    template: "%s · klws",
  },
  description:
    "金融级计费、防封号隔离、高到达率 —— 为跨境营销量身打造的 WhatsApp 并发发送引擎。",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="zh-CN"
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
          {children}
          <Toaster />
        </ThemeProvider>
      </body>
    </html>
  );
}
