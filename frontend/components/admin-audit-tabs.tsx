"use client";

import { useState } from "react";
import { cn } from "@/lib/utils";
import { AdminAuditLog } from "@/components/admin-audit-log";
import { AdminCampaigns } from "@/components/admin-campaigns";

type Tab = "audit" | "tasks";

const TABS: { key: Tab; label: string }[] = [
  { key: "audit", label: "审计日志" },
  { key: "tasks", label: "任务监控" },
];

export function AdminAuditTabs() {
  const [tab, setTab] = useState<Tab>("audit");
  return (
    <div className="space-y-4">
      <div className="inline-flex rounded-lg border bg-muted/40 p-0.5">
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={cn(
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
              tab === t.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>
      {tab === "audit" ? <AdminAuditLog /> : <AdminCampaigns />}
    </div>
  );
}
