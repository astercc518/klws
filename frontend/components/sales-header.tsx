"use client";

import { LogOut, Briefcase } from "lucide-react";
import { Button } from "@/components/ui/button";
import { logout } from "@/lib/api";

export function SalesHeader() {
  async function handleLogout() {
    await logout().catch(() => {});
    window.location.href = "/login";
  }

  return (
    <header className="sticky top-0 z-20 flex h-16 items-center justify-between border-b bg-background/80 px-6 backdrop-blur">
      <div className="flex items-center gap-2">
        <Briefcase className="size-4 text-muted-foreground" />
        <span className="font-mono text-[11px] uppercase tracking-[0.18em] text-muted-foreground">
          Sales · 名下客户管理
        </span>
      </div>
      <Button variant="ghost" size="sm" onClick={handleLogout} className="text-muted-foreground">
        <LogOut className="size-4" />
        退出登录
      </Button>
    </header>
  );
}
