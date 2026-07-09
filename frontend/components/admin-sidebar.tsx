"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { cn } from "@/lib/utils";
import { NAV_GROUPS } from "@/components/admin/nav";
import { AdminIdentity } from "@/components/admin/admin-avatar";
import { useMe } from "@/components/admin/use-me";
import { Logo } from "@/components/landing/logo";
import { useLocale, useT } from "@/components/locale-provider";

export function AdminSidebar({
  collapsed,
  onToggle,
}: {
  collapsed: boolean;
  onToggle: () => void;
}) {
  const pathname = usePathname();
  const me = useMe();
  const { locale } = useLocale();
  const t = useT();
  const en = locale === "en";

  return (
    <aside
      className={cn(
        "flex h-full shrink-0 flex-col border-r bg-background transition-[width] duration-200 ease-out",
        collapsed ? "w-[68px]" : "w-64",
      )}
    >
      {/* Brand — same klws mark + wordmark as the marketing site */}
      <div className="flex h-16 shrink-0 items-center gap-2.5 border-b border-border/60 px-4">
        <Logo className="size-8 shrink-0" />
        {!collapsed && (
          <div className="min-w-0 leading-tight">
            <div className="truncate text-[15px] font-semibold tracking-tight">klws</div>
            <div className="truncate font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
              Admin Console
            </div>
          </div>
        )}
      </div>

      {/* Grouped navigation */}
      <nav className="flex-1 overflow-y-auto px-3 py-4">
        {NAV_GROUPS.map((group) => (
          <div key={group.title} className="mb-5 last:mb-0">
            {collapsed ? (
              <div className="mx-2 mb-2 h-px bg-border first:hidden" />
            ) : (
              <div className="px-2 pb-1.5 font-mono text-[10px] uppercase tracking-[0.16em] text-muted-foreground/70">
                {en ? group.en : group.title}
              </div>
            )}
            <ul className="space-y-0.5">
              {group.items.map((item) => {
                const active =
                  item.href === "/admin"
                    ? pathname === "/admin"
                    : pathname.startsWith(item.href);
                const Icon = item.icon;
                return (
                  <li key={item.href}>
                    <Link
                      href={item.href}
                      title={collapsed ? (en ? item.en : item.label) : undefined}
                      aria-current={active ? "page" : undefined}
                      className={cn(
                        "group relative flex items-center rounded-md text-sm transition-colors",
                        collapsed ? "justify-center px-0 py-2" : "gap-3 px-2.5 py-2",
                        active
                          ? "bg-brand-500/10 font-medium text-brand-700 dark:text-brand-400"
                          : "text-muted-foreground hover:bg-muted/50 hover:text-foreground",
                      )}
                    >
                      {/* Active highlight bar — brand green */}
                      <span
                        className={cn(
                          "absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-r-full bg-brand-600 transition-opacity dark:bg-brand-500",
                          active ? "opacity-100" : "opacity-0",
                        )}
                      />
                      <Icon
                        className="size-4 shrink-0"
                        strokeWidth={active ? 2.25 : 1.75}
                      />
                      {!collapsed && (
                        <>
                          <span className="truncate">{en ? item.en : item.label}</span>
                          {!en && (
                            <span
                              className={cn(
                                "ml-auto font-mono text-[10px] uppercase tracking-wider",
                                active ? "text-muted-foreground" : "text-muted-foreground/45",
                              )}
                            >
                              {item.en}
                            </span>
                          )}
                        </>
                      )}
                    </Link>
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </nav>

      {/* Footer: identity + collapse toggle */}
      <div className="shrink-0 border-t p-3">
        <div className={cn("flex items-center", collapsed ? "justify-center" : "gap-2")}>
          {!collapsed && <AdminIdentity me={me} className="flex-1" />}
          <button
            type="button"
            onClick={onToggle}
            aria-label={collapsed ? t("sidebar.expand") : t("sidebar.collapse")}
            className="flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            {collapsed ? (
              <PanelLeftOpen className="size-4" />
            ) : (
              <PanelLeftClose className="size-4" />
            )}
          </button>
        </div>
      </div>
    </aside>
  );
}
