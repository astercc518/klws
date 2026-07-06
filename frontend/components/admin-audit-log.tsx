"use client";

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";

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
  const [rows, setRows] = useState<AuditRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const data = await api.get<{ rows: AuditRow[]; total: number }>("/admin/audit?limit=200");
      setRows(data.rows);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const columns: Column<AuditRow>[] = [
    { key: "occurred_at", header: "时间", cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.occurred_at}</span> },
    { key: "actor", header: "操作人", cell: (r) => <span className="text-sm">{r.actor_email ?? (r.actor_id ? `#${r.actor_id}` : "系统")}</span> },
    { key: "action", header: "动作", cell: (r) => <Badge variant={actionVariant(r.action)}>{r.action}</Badge> },
    { key: "target", header: "对象", cell: (r) => <span className="text-sm text-muted-foreground">{r.resource_type ? `${r.resource_type}${r.resource_id ? ` #${r.resource_id}` : ""}` : "—"}</span> },
    { key: "tenant", header: "租户", cell: (r) => <span className="text-sm text-muted-foreground">{r.tenant_name ?? (r.tenant_id ? `#${r.tenant_id}` : "—")}</span> },
    {
      key: "details",
      header: "详情",
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
      search={{ placeholder: "搜索动作/操作人/对象…", accessor: (r) => `${r.action} ${r.actor_email ?? ""} ${r.resource_type ?? ""} ${r.tenant_name ?? ""}` }}
      emptyState="暂无审计记录"
    />
  );
}
