"use client";

import { useCallback, useEffect, useState } from "react";
import { ListChecks, Send, CheckCheck, XCircle } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatCard, MetricCardGroup } from "@/components/admin/stat-card";

interface RecipientRow {
  id: number;
  campaign_id: number;
  tenant_id: number;
  tenant_name: string | null;
  phone: string;
  state: "pending" | "sent" | "failed" | "skipped";
  last_error: string | null;
  updated_at: string;
  delivered_at: string | null;
  read_at: string | null;
}
interface RecipientStats {
  total: number;
  sent: number;
  delivered: number;
  read: number;
  failed: number;
  pending: number;
  skipped: number;
}

const stateVariant: Record<RecipientRow["state"], "default" | "secondary" | "outline" | "destructive"> = {
  sent: "secondary",
  failed: "destructive",
  pending: "outline",
  skipped: "outline",
};
const nf = new Intl.NumberFormat("en-US");
const PAGE_SIZE = 15;

const STATE_TABS: { key: string; label: string }[] = [
  { key: "", label: "全部" },
  { key: "sent", label: "已发送" },
  { key: "failed", label: "失败" },
  { key: "pending", label: "待发" },
  { key: "skipped", label: "跳过" },
];

export function AdminSendRecords() {
  const [rows, setRows] = useState<RecipientRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<RecipientStats | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [state, setState] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (state) params.set("state", state);
    try {
      const d = await api.get<{ rows: RecipientRow[]; total: number; stats: RecipientStats }>(
        `/admin/recipients?${params.toString()}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setStats(d.stats);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, state]);

  useEffect(() => {
    load();
  }, [load]);

  const tenantLabel = (r: RecipientRow) => r.tenant_name ?? `#${r.tenant_id}`;

  const columns: Column<RecipientRow>[] = [
    { key: "phone", header: "手机号", cell: (r) => <span className="font-mono text-sm">{r.phone}</span> },
    { key: "tenant", header: "租户", cell: (r) => <span className="text-sm text-muted-foreground">{tenantLabel(r)}</span> },
    { key: "campaign", header: "任务", cell: (r) => <span className="font-mono text-sm text-muted-foreground">#{r.campaign_id}</span> },
    {
      key: "state",
      header: "状态",
      cell: (r) => <Badge variant={stateVariant[r.state]}>{r.state}</Badge>,
    },
    {
      key: "last_error",
      header: "失败原因",
      cell: (r) => (
        <span className="block max-w-xs truncate text-xs text-muted-foreground" title={r.last_error ?? undefined}>
          {r.last_error ?? "—"}
        </span>
      ),
    },
    { key: "updated_at", header: "更新时间", cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.updated_at}</span> },
  ];

  return (
    <div className="space-y-6">
      {stats && (
        <MetricCardGroup>
          <StatCard accent="brand" label="记录总数" value={nf.format(stats.total)} sub="全平台" icon={ListChecks} />
          <StatCard accent="emerald" label="已发送" value={nf.format(stats.sent)} sub="含待送达/已送达" icon={Send} />
          <StatCard accent="blue" label="已送达" value={nf.format(stats.delivered)} sub="回执确认" icon={CheckCheck} />
          <StatCard accent="rose" label="失败" value={nf.format(stats.failed)} sub="发送失败" icon={XCircle} />
        </MetricCardGroup>
      )}

      <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
        {STATE_TABS.map((t) => (
          <button
            key={t.key || "all"}
            onClick={() => {
              setState(t.key);
              setPage(0);
            }}
            className={
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
              (state === t.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
            }
          >
            {t.label}
          </button>
        ))}
      </div>

      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(r) => r.id}
        emptyState="暂无发送记录。任务开始发送后这里会实时出现每条明细。"
        search={{ placeholder: "搜索手机号…", accessor: () => "" }}
        server={{
          total,
          page,
          pageSize: PAGE_SIZE,
          onPageChange: setPage,
          query,
          onQueryChange: (q) => {
            setQuery(q);
            setPage(0);
          },
          loading,
        }}
      />
    </div>
  );
}
