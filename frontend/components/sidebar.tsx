"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { LayoutDashboard, Smartphone, Send, Receipt, Contact } from "lucide-react";
import { cn } from "@/lib/utils";
import { useLocale } from "@/components/locale-provider";

const NAV = [
  { href: "/dashboard", label: "概览", en: "Dashboard", icon: LayoutDashboard },
  { href: "/dashboard/accounts", label: "账号池", en: "Accounts", icon: Smartphone },
  { href: "/dashboard/contacts", label: "联系人", en: "Contacts", icon: Contact },
  { href: "/dashboard/campaigns", label: "群发任务", en: "Campaigns", icon: Send },
  { href: "/dashboard/billing", label: "账单流水", en: "Billing", icon: Receipt },
];

export function Sidebar() {
  const pathname = usePathname();
  const { locale } = useLocale();
  const en = locale === "en";

  return (
    <aside className="flex h-full w-64 shrink-0 flex-col border-r bg-sidebar">
      {/* Brand */}
      <div className="flex h-16 items-center gap-3 border-b px-6">
        <div className="flex size-7 items-center justify-center rounded-[5px] bg-foreground text-background">
          <span className="font-mono text-sm font-bold leading-none">w</span>
        </div>
        <div className="leading-tight">
          <div className="text-sm font-semibold tracking-tight">wadist</div>
          <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
            Dispatch Console
          </div>
        </div>
      </div>

      {/* Nav */}
      <nav className="flex-1 px-3 py-5">
        <div className="px-3 pb-2 font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
          Operations
        </div>
        <ul className="space-y-0.5">
          {NAV.map((item) => {
            const active = pathname === item.href;
            const Icon = item.icon;
            return (
              <li key={item.href}>
                <Link
                  href={item.href}
                  className={cn(
                    "group flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors",
                    active
                      ? "bg-foreground text-background"
                      : "text-muted-foreground hover:bg-accent hover:text-foreground",
                  )}
                >
                  <Icon className="size-4 shrink-0" strokeWidth={active ? 2.25 : 1.75} />
                  <span className="font-medium">{en ? item.en : item.label}</span>
                  {!en && (
                    <span
                      className={cn(
                        "ml-auto font-mono text-[10px] uppercase tracking-wider",
                        active ? "text-background/60" : "text-muted-foreground/50",
                      )}
                    >
                      {item.en}
                    </span>
                  )}
                </Link>
              </li>
            );
          })}
        </ul>
      </nav>

      {/* Footer */}
      <div className="border-t px-6 py-4">
        <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
          build · mvp-e2
        </div>
      </div>
    </aside>
  );
}
