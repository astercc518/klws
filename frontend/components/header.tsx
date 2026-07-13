"use client";

import { LogOut } from "lucide-react";
import { Button } from "@/components/ui/button";
import { logout } from "@/lib/api";
import { ThemeToggle } from "@/components/theme-toggle";
import { LanguageToggle } from "@/components/language-toggle";
import { useT } from "@/components/locale-provider";

// Mock session context until /auth/me is wired in task two.
const MOCK_TENANT = { name: "Acme Corp", status: "Active", email: "ops@acme.com" };

export function Header() {
  const t = useT();
  async function handleLogout() {
    // logout() clears the token; the api layer redirects to /login on 401.
    await logout().catch(() => {});
    window.location.href = "/login";
  }

  return (
    <header className="sticky top-0 z-20 flex h-16 items-center justify-between border-b bg-background/80 px-6 backdrop-blur">
      {/* Tenant identity + live status */}
      <div className="flex items-center gap-3">
        <span className="text-sm font-semibold tracking-tight">{MOCK_TENANT.name}</span>
        <span className="inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs">
          <span className="relative flex size-1.5">
            <span className="absolute inline-flex size-full animate-ping rounded-full bg-emerald-500 opacity-75 motion-reduce:animate-none" />
            <span className="relative inline-flex size-1.5 rounded-full bg-emerald-500" />
          </span>
          <span className="font-mono text-[10px] uppercase tracking-[0.15em] text-muted-foreground">
            {MOCK_TENANT.status}
          </span>
        </span>
      </div>

      {/* Account + sign out */}
      <div className="flex items-center gap-2">
        <span className="hidden font-mono text-xs text-muted-foreground sm:inline">
          {MOCK_TENANT.email}
        </span>
        <LanguageToggle />
        <ThemeToggle />
        <Button variant="ghost" size="sm" onClick={handleLogout} className="text-muted-foreground">
          <LogOut className="size-4" />
          {t("menu.logout")}
        </Button>
      </div>
    </header>
  );
}
