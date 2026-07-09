"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Search, Bell, LogOut, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ThemeToggle } from "@/components/theme-toggle";
import { LanguageToggle } from "@/components/language-toggle";
import { useLocale, useT } from "@/components/locale-provider";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { resolveRoute } from "@/components/admin/nav";
import { AdminAvatar, AdminIdentity } from "@/components/admin/admin-avatar";
import { useMe } from "@/components/admin/use-me";
import { logout } from "@/lib/api";

function useCrumbs() {
  const pathname = usePathname();
  const { locale } = useLocale();
  const t = useT();
  const resolved = resolveRoute(pathname);
  const en = locale === "en";
  // Root crumb + the section (when it adds information) + the current page.
  const crumbs: { label: string; href?: string }[] = [
    { label: t("header.brand"), href: "/admin" },
  ];
  if (resolved) {
    const onOverview = resolved.item.href === "/admin";
    if (!onOverview) crumbs.push({ label: en ? resolved.group.en : resolved.group.title });
    crumbs.push({ label: en ? resolved.item.en : resolved.item.label });
  }
  return crumbs;
}

export function AdminHeader() {
  const me = useMe();
  const t = useT();
  const crumbs = useCrumbs();

  async function handleLogout() {
    await logout().catch(() => {});
    window.location.href = "/login";
  }

  return (
    <header className="sticky top-0 z-20 flex h-16 shrink-0 items-center gap-4 border-b border-border/60 bg-background/80 px-5 backdrop-blur supports-[backdrop-filter]:bg-background/65 lg:px-8">
      {/* Breadcrumbs */}
      <nav aria-label={t("header.breadcrumb")} className="flex min-w-0 items-center gap-1.5 text-sm">
        {crumbs.map((c, i) => {
          const last = i === crumbs.length - 1;
          return (
            <span key={i} className="flex min-w-0 items-center gap-1.5">
              {i > 0 && (
                <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/40" />
              )}
              {c.href && !last ? (
                <Link
                  href={c.href}
                  className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
                >
                  {c.label}
                </Link>
              ) : (
                <span
                  className={
                    last ? "truncate font-medium text-foreground" : "shrink-0 text-muted-foreground"
                  }
                >
                  {c.label}
                </span>
              )}
            </span>
          );
        })}
      </nav>

      {/* Global search — placeholder per spec; opens nothing yet. */}
      <div className="ml-auto hidden md:block">
        <div className="flex h-8 w-56 items-center gap-2 rounded-lg border bg-muted/40 px-2.5 text-muted-foreground lg:w-72">
          <Search className="size-3.5 shrink-0" />
          <input
            type="search"
            placeholder={t("header.searchPlaceholder")}
            aria-label={t("header.searchAria")}
            className="min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground/70"
          />
          <kbd className="hidden shrink-0 rounded border bg-background px-1.5 font-mono text-[10px] text-muted-foreground lg:inline-block">
            ⌘K
          </kbd>
        </div>
      </div>

      {/* Notifications */}
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={t("header.notifications")}
        className="relative ml-auto text-muted-foreground md:ml-0"
      >
        <Bell className="size-4" />
        <span className="absolute right-1.5 top-1.5 size-1.5 rounded-full bg-rose-500 ring-2 ring-background" />
      </Button>

      <LanguageToggle />
      <ThemeToggle />

      <div className="h-5 w-px bg-border" />

      {/* User menu */}
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <button
              type="button"
              aria-label={t("header.accountMenu")}
              className="rounded-full outline-none transition-opacity hover:opacity-80 focus-visible:ring-3 focus-visible:ring-ring/50"
            />
          }
        >
          <AdminAvatar me={me} />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-60">
          <DropdownMenuLabel className="p-2">
            <AdminIdentity me={me} />
          </DropdownMenuLabel>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onClick={handleLogout}>
            <LogOut className="size-4" />
            {t("menu.logout")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </header>
  );
}
