"use client";

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { useT } from "@/components/locale-provider";

interface AuditRow {
  id: number;
  occurred_at: string;
  tenant_id: number | null;
  tenant_name: string | null;
  actor_id: number | null;
  actor_email: string | null;
  action: string;
  resource_type: string | null;
  resource_id: number | null;
  details: unknown;
}

// Coarse color families by action prefix (finance/user/risk/campaign/…).
function actionVariant(action: string): "default" | "secondary" | "outline" {
  if (action.startsWith("finance.")) return "default";
  if (action.startsWith("risk.") || action.startsWith("campaign.")) return "secondary";
  return "outline";
}

export function AdminAuditLog() {
  const t = useT();
  const [rows, setRows] = useState<AuditRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const data = await api.get<{ rows: AuditRow[]; total: number }>("/admin/audit?limit=200");
      setRows(data.rows);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("admin.audit.loadFailed"));
      }
    }
  }, [t]);

  useEffect(() => {
    load();
  }, [load]);

  const columns: Column<AuditRow>[] = [
    {
      key: "occurred_at",
      header: t("admin.audit.col.time"),
      title: t("admin.audit.col.time"),
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.occurred_at}</span>,
    },
    {
      key: "actor",
      header: t("admin.audit.col.actor"),
      title: t("admin.audit.col.actor"),
      hideable: false,
      cell: (r) => (
        <span className="text-sm">{r.actor_email ?? (r.actor_id ? `#${r.actor_id}` : t("admin.audit.systemActor"))}</span>
      ),
    },
    {
      key: "action",
      header: t("admin.audit.col.action"),
      title: t("admin.audit.col.action"),
      cell: (r) => <Badge variant={actionVariant(r.action)}>{r.action}</Badge>,
    },
    {
      key: "target",
      header: t("admin.audit.col.target"),
      title: t("admin.audit.col.target"),
      cell: (r) => (
        <span className="text-sm text-muted-foreground">
          {r.resource_type ? `${r.resource_type}${r.resource_id ? ` #${r.resource_id}` : ""}` : "—"}
        </span>
      ),
    },
    {
      key: "tenant",
      header: t("admin.audit.col.tenant"),
      title: t("admin.audit.col.tenant"),
      cell: (r) => (
        <span className="text-sm text-muted-foreground">{r.tenant_name ?? (r.tenant_id ? `#${r.tenant_id}` : "—")}</span>
      ),
    },
    {
      key: "details",
      header: t("admin.audit.col.details"),
      title: t("admin.audit.col.details"),
      cell: (r) => {
        const s = JSON.stringify(r.details ?? {});
        return <span className="font-mono text-xs text-muted-foreground" title={s}>{s.length > 48 ? s.slice(0, 47) + "…" : s}</span>;
      },
    },
  ];

  return (
    <ProDataTable
      data={rows}
      error={error}
      columns={columns}
      getRowKey={(r) => r.id}
      storageKey="admin-audit-log"
      search={{
        placeholder: t("admin.audit.searchPlaceholder"),
        accessor: (r) => `${r.action} ${r.actor_email ?? ""} ${r.resource_type ?? ""} ${r.tenant_name ?? ""}`,
      }}
      emptyState={t("admin.audit.emptyState")}
    />
  );
}
