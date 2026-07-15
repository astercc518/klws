"use client";

import { useCallback, useEffect, useState } from "react";
import { Download } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { useT } from "@/components/locale-provider";

interface LedgerRow {
  id: number;
  tenant_id: number;
  tenant_name: string | null;
  kind: string;
  delta_balance: number;
  delta_frozen: number;
  balance_after: number;
  frozen_after: number;
  created_at: string;
}
interface RefundRow {
  id: number;
  tenant_id: number;
  amount: number;
  reason: string;
  state: string;
}

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

function Money({ cents }: { cents: number }) {
  const cls = cents < 0 ? "text-rose-600 dark:text-rose-400" : "text-emerald-600 dark:text-emerald-400";
  return <span className={`font-mono tabular-nums ${cls}`}>{usd(cents)}</span>;
}

const refundStateVariant: Record<string, "default" | "secondary" | "outline"> = {
  approved: "default",
  pending: "secondary",
  rejected: "outline",
};

const KIND_TABS: { key: string; labelKey: string }[] = [
  { key: "", labelKey: "admin.ledger.tab.all" },
  { key: "topup", labelKey: "admin.ledger.tab.topup" },
  { key: "settle", labelKey: "admin.ledger.tab.settle" },
  { key: "refund", labelKey: "admin.ledger.tab.refund" },
  { key: "adjust", labelKey: "admin.ledger.tab.adjust" },
];
const PAGE_SIZE = 15;

export function AdminLedger() {
  const t = useT();
  const [rows, setRows] = useState<LedgerRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [refunds, setRefunds] = useState<RefundRow[] | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [kind, setKind] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (kind) params.set("kind", kind);
    if (query.trim()) params.set("tenant_id", query.trim());
    try {
      const d = await api.get<{ rows: LedgerRow[]; total: number; refunds: RefundRow[] }>(
        `/admin/finance/ledger?${params.toString()}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setRefunds(d.refunds);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("admin.ledger.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, kind, t]);

  useEffect(() => {
    load();
  }, [load]);

  async function exportCsv() {
    const params = new URLSearchParams();
    if (kind) params.set("kind", kind);
    if (query.trim()) params.set("tenant_id", query.trim());
    try {
      await api.download(`/admin/finance/ledger/export?${params.toString()}`, "ledger.csv");
    } catch (e) {
      toast.error(t("admin.ledger.exportFailedTitle"), {
        description: e instanceof ApiError ? e.message : t("admin.ledger.retry"),
      });
    }
  }

  const tenantLabel = (r: LedgerRow) => r.tenant_name ?? `#${r.tenant_id}`;

  const ledgerCols: Column<LedgerRow>[] = [
    {
      key: "kind",
      header: t("admin.ledger.col.kind"),
      title: t("admin.ledger.col.kind"),
      hideable: false,
      cell: (r) => <Badge variant="outline">{r.kind}</Badge>,
    },
    { key: "tenant", header: t("admin.ledger.col.tenant"), title: t("admin.ledger.col.tenant"), cell: (r) => <span className="text-sm text-muted-foreground">{tenantLabel(r)}</span> },
    { key: "delta_balance", header: t("admin.ledger.col.deltaBalance"), title: t("admin.ledger.col.deltaBalance"), align: "right", cell: (r) => <Money cents={r.delta_balance} /> },
    { key: "delta_frozen", header: t("admin.ledger.col.deltaFrozen"), title: t("admin.ledger.col.deltaFrozen"), align: "right", cell: (r) => <span className="font-mono tabular-nums text-muted-foreground">{usd(r.delta_frozen)}</span> },
    { key: "balance_after", header: t("admin.ledger.col.balanceAfter"), title: t("admin.ledger.col.balanceAfter"), align: "right", cell: (r) => <span className="font-mono tabular-nums">{usd(r.balance_after)}</span> },
    { key: "created_at", header: t("admin.ledger.col.time"), title: t("admin.ledger.col.time"), cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.created_at}</span> },
  ];

  const refundCols: Column<RefundRow>[] = [
    {
      key: "tenant",
      header: t("admin.ledger.col.tenant"),
      title: t("admin.ledger.col.tenant"),
      hideable: false,
      cell: (r) => <span className="text-sm text-muted-foreground">#{r.tenant_id}</span>,
    },
    { key: "amount", header: t("admin.ledger.col.amount"), title: t("admin.ledger.col.amount"), align: "right", cell: (r) => <span className="font-mono tabular-nums">{usd(r.amount)}</span> },
    { key: "reason", header: t("admin.ledger.col.reason"), title: t("admin.ledger.col.reason"), cell: (r) => <span className="text-sm">{r.reason}</span> },
    { key: "state", header: t("admin.ledger.col.state"), title: t("admin.ledger.col.state"), cell: (r) => <Badge variant={refundStateVariant[r.state] ?? "outline"}>{r.state}</Badge> },
  ];

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
            {KIND_TABS.map((tab) => (
              <button
                key={tab.key || "all"}
                onClick={() => {
                  setKind(tab.key);
                  setPage(0);
                }}
                className={
                  "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
                  (kind === tab.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
                }
              >
                {t(tab.labelKey)}
              </button>
            ))}
          </div>
          <Button variant="outline" size="sm" className="gap-1.5" onClick={exportCsv}>
            <Download className="size-4" />
            {t("admin.ledger.exportCsv")}
          </Button>
        </div>

        <ProDataTable
          data={rows}
          error={error}
          columns={ledgerCols}
          getRowKey={(r) => `l${r.id}`}
          storageKey="admin-ledger"
          emptyState={t("admin.ledger.emptyLedger")}
          search={{ placeholder: t("admin.ledger.searchPlaceholder"), accessor: () => "" }}
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
        <p className="font-mono text-xs text-muted-foreground">{t("admin.ledger.searchHint")}</p>
      </section>

      <section className="space-y-2">
        <p className="font-mono text-xs uppercase tracking-wider text-muted-foreground">{t("admin.ledger.refundCaption")}</p>
        <ProDataTable
          data={refunds}
          error={error}
          columns={refundCols}
          getRowKey={(r) => `r${r.id}`}
          storageKey="admin-ledger-refunds"
          search={{ placeholder: t("admin.ledger.refundSearchPlaceholder"), accessor: (r) => `${r.tenant_id} ${r.reason}` }}
          emptyState={t("admin.ledger.emptyRefunds")}
        />
      </section>
    </div>
  );
}
