import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { ThemeToggle } from "@/components/theme-toggle";
import { LanguageToggle } from "@/components/language-toggle";
import { Logo } from "@/components/landing/logo";
import { BroadcastDemo } from "@/components/auth/broadcast-demo";

/**
 * Split-screen auth layout: a branded (WhatsApp-green) panel on the left with a
 * live, interactive broadcast demo (the product thesis), and the form on the
 * right. Shared by login / register / forgot / reset.
 */
export function AuthShell({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle: string;
  children: React.ReactNode;
}) {
  return (
    <main className="grid min-h-screen lg:grid-cols-[1.05fr_1fr]">
      {/* ── left: brand + live broadcast demo (desktop only) ── */}
      <aside className="relative hidden flex-col justify-between overflow-hidden bg-gradient-to-br from-brand-500 via-brand-600 to-brand-700 p-12 lg:flex">
        {/* grid texture */}
        <div
          aria-hidden
          className="pointer-events-none absolute inset-0 [background-image:linear-gradient(rgba(255,255,255,0.06)_1px,transparent_1px),linear-gradient(90deg,rgba(255,255,255,0.06)_1px,transparent_1px)] [background-size:30px_30px] [mask-image:radial-gradient(ellipse_80%_80%_at_50%_40%,#000,transparent)]"
        />
        {/* soft drifting glows */}
        <div
          aria-hidden
          className="animate-auth-drift pointer-events-none absolute -top-24 -left-16 size-80 rounded-full bg-white/15 blur-3xl"
        />
        <div
          aria-hidden
          className="animate-auth-drift pointer-events-none absolute -right-20 bottom-0 size-96 rounded-full bg-black/15 blur-3xl"
          style={{ animationDelay: "-8s" }}
        />

        {/* brand */}
        <div className="relative flex items-center gap-2.5">
          <Logo
            variant="white"
            className="size-8 [filter:drop-shadow(0_2px_6px_rgba(0,0,0,0.25))]"
          />
          <span className="text-xl font-semibold tracking-tight text-white">klws</span>
        </div>

        {/* broadcast demo + headline */}
        <div className="relative flex flex-col items-center text-center">
          <BroadcastDemo />
          <h2 className="mt-8 text-2xl leading-tight font-semibold tracking-tight text-balance text-white">
            把 WhatsApp 变成你的增长引擎
          </h2>
          <p className="mt-2.5 max-w-xs text-sm text-emerald-50/80">
            百万级并发触达 · 多租户隔离 · 实时投递回执
          </p>
        </div>

        {/* footer */}
        <div className="relative flex items-center gap-4 font-mono text-[11px] tracking-wide text-white/60">
          <span>© 2026 klws.cc</span>
          <span className="size-1 rounded-full bg-white/30" />
          <span>Multi-tenant · RLS-isolated</span>
        </div>
      </aside>

      {/* ── right: form ── */}
      <div className="relative flex flex-col items-center justify-center px-6 py-12 sm:px-10">
        <div className="absolute top-4 right-4 flex items-center gap-1">
          <LanguageToggle />
          <ThemeToggle />
        </div>

        <div className="w-full max-w-sm">
          {/* mobile brand (left panel is hidden) */}
          <div className="mb-8 flex items-center gap-2.5 lg:hidden">
            <Logo className="size-8" />
            <span className="text-lg font-semibold tracking-tight">klws</span>
          </div>

          <h1 className="text-3xl font-bold tracking-tight">{title}</h1>
          <p className="mt-2 text-sm text-muted-foreground">{subtitle}</p>

          {children}

          <Link
            href="/"
            className="mt-8 inline-flex items-center gap-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
          >
            <ArrowLeft className="size-4" />
            返回首页
          </Link>
        </div>
      </div>
    </main>
  );
}
