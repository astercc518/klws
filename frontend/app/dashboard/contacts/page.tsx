"use client";

import { useState } from "react";
import { PageHeader } from "@/components/admin/page-header";
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
      <PageHeader
        eyebrow="Contacts"
        title={{ zh: "联系人", en: "Contacts" }}
        description={{
          zh: "管理联系人库、标签与分段,并维护退订黑名单——建群发时可直接从分段选取收件人。",
          en: "Manage your contact directory, tags, and segments, and maintain the opt-out suppression list — pick recipients directly from a segment when creating a campaign.",
        }}
        className="pb-0"
      />

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
