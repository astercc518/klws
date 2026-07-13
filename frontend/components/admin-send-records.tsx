"use client";

import { useCallback, useEffect, useState } from "react";
import { ListChecks, Send, CheckCheck, XCircle } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatCard, MetricCardGroup } from "@/components/admin/stat-card";
import { useT } from "@/components/locale-provider";

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

// Filter tab keys → dict label key, resolved via t() at render time.
const STATE_TABS: { key: string; labelKey: string }[] = [
  { key: "", labelKey: "admin.sendrec.stateTab.all" },
  { key: "sent", labelKey: "admin.sendrec.stateTab.sent" },
  { key: "failed", labelKey: "admin.sendrec.stateTab.failed" },
  { key: "pending", labelKey: "admin.sendrec.stateTab.pending" },
  { key: "skipped", labelKey: "admin.sendrec.stateTab.skipped" },
];

export function AdminSendRecords() {
  const t = useT();
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
        setError(e instanceof ApiError ? e.message : t("admin.sendrec.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, state, t]);

  useEffect(() => {
    load();
  }, [load]);

  const tenantLabel = (r: RecipientRow) => r.tenant_name ?? `#${r.tenant_id}`;

  const columns: Column<RecipientRow>[] = [
    { key: "phone", header: t("admin.sendrec.col.phone"), cell: (r) => <span className="font-mono text-sm">{r.phone}</span> },
    {
      key: "tenant",
      header: t("admin.sendrec.col.tenant"),
      cell: (r) => <span className="text-sm text-muted-foreground">{tenantLabel(r)}</span>,
    },
    {
      key: "campaign",
      header: t("admin.sendrec.col.campaign"),
      cell: (r) => <span className="font-mono text-sm text-muted-foreground">#{r.campaign_id}</span>,
    },
    {
      key: "state",
      header: t("admin.sendrec.col.state"),
      cell: (r) => <Badge variant={stateVariant[r.state]}>{r.state}</Badge>,
    },
    {
      key: "last_error",
      header: t("admin.sendrec.col.failReason"),
      cell: (r) => (
        <span className="block max-w-xs truncate text-xs text-muted-foreground" title={r.last_error ?? undefined}>
          {r.last_error ?? "—"}
        </span>
      ),
    },
    {
      key: "updated_at",
      header: t("admin.sendrec.col.updatedAt"),
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.updated_at}</span>,
    },
  ];

  return (
    <div className="space-y-6">
      {stats && (
        <MetricCardGroup>
          <StatCard
            accent="brand"
            label={t("admin.sendrec.stat.totalLabel")}
            value={nf.format(stats.total)}
            sub={t("admin.sendrec.stat.totalSub")}
            icon={ListChecks}
          />
          <StatCard
            accent="emerald"
            label={t("admin.sendrec.stat.sentLabel")}
            value={nf.format(stats.sent)}
            sub={t("admin.sendrec.stat.sentSub")}
            icon={Send}
          />
          <StatCard
            accent="blue"
            label={t("admin.sendrec.stat.deliveredLabel")}
            value={nf.format(stats.delivered)}
            sub={t("admin.sendrec.stat.deliveredSub")}
            icon={CheckCheck}
          />
          <StatCard
            accent="rose"
            label={t("admin.sendrec.stat.failedLabel")}
            value={nf.format(stats.failed)}
            sub={t("admin.sendrec.stat.failedSub")}
            icon={XCircle}
          />
        </MetricCardGroup>
      )}

      <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
        {STATE_TABS.map((tab) => (
          <button
            key={tab.key || "all"}
            onClick={() => {
              setState(tab.key);
              setPage(0);
            }}
            className={
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
              (state === tab.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
            }
          >
            {t(tab.labelKey)}
          </button>
        ))}
      </div>

      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(r) => r.id}
        emptyState={t("admin.sendrec.emptyState")}
        search={{ placeholder: t("admin.sendrec.searchPlaceholder"), accessor: () => "" }}
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
