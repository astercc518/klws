"use client";

import { useState } from "react";
import { AdminUsersTable } from "@/components/admin-users-table";
import { AdminTenantsTable } from "@/components/admin-tenants-table";

const TABS = [
  { key: "users", label: "用户" },
  { key: "tenants", label: "租户" },
] as const;

type TabKey = (typeof TABS)[number]["key"];

export function AdminIamTabs() {
  const [tab, setTab] = useState<TabKey>("users");

  return (
    <div className="space-y-6">
      <div className="inline-flex rounded-lg border bg-muted/40 p-0.5">
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
              (tab === t.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
            }
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "users" ? <AdminUsersTable /> : <AdminTenantsTable />}
    </div>
  );
}
