"use client";

import { useEffect, useState } from "react";
import { AdminSidebar } from "@/components/admin-sidebar";
import { AdminHeader } from "@/components/admin-header";

const COLLAPSE_KEY = "wadist_admin_sidebar_collapsed";

/** The admin chrome root: owns the sidebar collapse state (persisted) and lays
 *  out the fixed sidebar, sticky glass header, and scrolling content well. */
export function AdminShell({ children }: { children: React.ReactNode }) {
  const [collapsed, setCollapsed] = useState(false);

  // Restore the persisted choice after mount to avoid an SSR/CSR mismatch.
  useEffect(() => {
    setCollapsed(window.localStorage.getItem(COLLAPSE_KEY) === "1");
  }, []);

  function toggle() {
    setCollapsed((c) => {
      const next = !c;
      window.localStorage.setItem(COLLAPSE_KEY, next ? "1" : "0");
      return next;
    });
  }

  return (
    <div className="flex h-screen overflow-hidden bg-background">
      <AdminSidebar collapsed={collapsed} onToggle={toggle} />
      <div className="flex min-w-0 flex-1 flex-col">
        <AdminHeader />
        <main className="relative flex-1 overflow-y-auto px-5 py-6 lg:px-8 lg:py-8">
          {/* Same spatial texture as the landing hero: dot-grid + a soft brand
              glow at the top, masked so it never competes with content. */}
          <div
            aria-hidden
            className="bg-dot-grid pointer-events-none absolute inset-0 -z-10 [mask-image:radial-gradient(ellipse_80%_50%_at_50%_0%,#000_30%,transparent_85%)]"
          />
          <div
            aria-hidden
            className="pointer-events-none absolute -top-32 left-1/2 -z-10 h-[320px] w-[760px] -translate-x-1/2 rounded-full bg-brand-500/8 blur-3xl dark:bg-brand-500/10"
          />
          {children}
        </main>
      </div>
    </div>
  );
}
