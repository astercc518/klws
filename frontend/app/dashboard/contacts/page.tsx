"use client";

import { useState } from "react";
import { ContactsList } from "@/components/dashboard/contacts-list";
import { ContactsTagsSegments } from "@/components/dashboard/contacts-tags-segments";
import { SuppressionList } from "@/components/dashboard/suppression-list";

const TABS = [
  { key: "contacts", label: "联系人" },
  { key: "tags", label: "标签与分段" },
  { key: "suppression", label: "黑名单" },
] as const;
type TabKey = (typeof TABS)[number]["key"];

export default function DashboardContactsPage() {
  const [tab, setTab] = useState<TabKey>("contacts");

  return (
    <div className="mx-auto max-w-7xl space-y-6">
      <div>
        <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
          Contacts
        </div>
        <h1 className="mt-1 text-2xl font-semibold tracking-tight">联系人</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          管理联系人库、标签与分段,并维护退订黑名单——建群发时可直接从分段选取收件人。
        </p>
      </div>

      <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={
              "rounded-md px-3.5 py-1.5 text-sm font-medium transition-colors " +
              (tab === t.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
            }
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "contacts" && <ContactsList />}
      {tab === "tags" && <ContactsTagsSegments />}
      {tab === "suppression" && <SuppressionList />}
    </div>
  );
}
