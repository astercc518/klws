"use client";

import { LogOut, Briefcase } from "lucide-react";
import { Button } from "@/components/ui/button";
import { logout } from "@/lib/api";
import { ThemeToggle } from "@/components/theme-toggle";
import { LanguageToggle } from "@/components/language-toggle";
import { useT } from "@/components/locale-provider";

export function SalesHeader() {
  const t = useT();
  async function handleLogout() {
    await logout().catch(() => {});
    window.location.href = "/login";
  }

  return (
    <header className="sticky top-0 z-20 flex h-16 items-center justify-between border-b bg-background/80 px-6 backdrop-blur">
      <div className="flex items-center gap-2">
        <Briefcase className="size-4 text-muted-foreground" />
        <span className="font-mono text-[11px] uppercase tracking-[0.18em] text-muted-foreground">
          Sales · {t("sales.tagline")}
        </span>
      </div>
      <div className="flex items-center gap-2">
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
