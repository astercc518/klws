"use client";

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Input } from "@/components/ui/input";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatusBadge, type StatusTone } from "@/components/admin/status-badge";
import { useT } from "@/components/locale-provider";

interface AdminContactRow {
  id: number;
  tenant_id: number;
  phone: string;
  country_code: string;
  status: "active" | "unsubscribed" | "invalid";
  created_at: string;
}

const STATUS_TONE: Record<AdminContactRow["status"], StatusTone> = {
  active: "positive",
  unsubscribed: "warning",
  invalid: "negative",
};
// Status → dict key (not the label text), resolved via t() at render time.
const STATUS_LABEL_KEY: Record<AdminContactRow["status"], string> = {
  active: "admin.contacts.status.active",
  unsubscribed: "admin.contacts.status.unsubscribed",
  invalid: "admin.contacts.status.invalid",
};

const PAGE_SIZE = 20;

// AdminContacts: read-only, cross-tenant contact library view for the admin
// console. Backed by GET /admin/contacts (s.systemPool(), BYPASSRLS) — there
// is deliberately no create/edit/delete here; admins can look but not touch a
// customer's contacts (governance boundary), so this component never calls a
// write endpoint.
export function AdminContacts() {
  const t = useT();
  const [rows, setRows] = useState<AdminContactRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [tenantId, setTenantId] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (status) params.set("status", status);
    if (tenantId.trim()) params.set("tenant_id", tenantId.trim());
    try {
      const d = await api.get<{ rows: AdminContactRow[]; total: number }>(
        `/admin/contacts?${params.toString()}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : t("admin.contacts.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, status, tenantId, t]);

  useEffect(() => {
    load();
  }, [load]);

  const columns: Column<AdminContactRow>[] = [
    { key: "phone", header: t("admin.contacts.col.phone"), cell: (r) => <span className="font-mono text-sm">{r.phone}</span> },
    {
      key: "tenant",
      header: t("admin.contacts.col.tenant"),
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">#{r.tenant_id}</span>,
    },
    {
      key: "country",
      header: t("admin.contacts.col.country"),
      cell: (r) => <span className="text-sm text-muted-foreground">{r.country_code || "—"}</span>,
    },
    {
      key: "status",
      header: t("admin.contacts.col.status"),
      cell: (r) => <StatusBadge tone={STATUS_TONE[r.status]}>{t(STATUS_LABEL_KEY[r.status])}</StatusBadge>,
    },
    {
      key: "created_at",
      header: t("admin.contacts.col.createdAt"),
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.created_at}</span>,
    },
  ];

  return (
    <ProDataTable
      data={rows}
      error={error}
      columns={columns}
      getRowKey={(r) => r.id}
      emptyState={t("admin.contacts.emptyState")}
      search={{ placeholder: t("admin.contacts.searchPlaceholder"), accessor: () => "" }}
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
      toolbar={
        <div className="flex items-center gap-2">
          <Input
            value={tenantId}
            onChange={(e) => {
              setTenantId(e.target.value);
              setPage(0);
            }}
            placeholder={t("admin.contacts.tenantIdPlaceholder")}
            className="h-8 w-24 font-mono text-sm"
            aria-label={t("admin.contacts.filterByTenantAria")}
          />
          <select
            value={status}
            onChange={(e) => {
              setStatus(e.target.value);
              setPage(0);
            }}
            className="h-8 rounded-md border bg-transparent px-2 text-sm"
            aria-label={t("admin.contacts.filterByStatusAria")}
          >
            <option value="">{t("admin.contacts.filter.allStatuses")}</option>
            <option value="active">{t(STATUS_LABEL_KEY.active)}</option>
            <option value="unsubscribed">{t(STATUS_LABEL_KEY.unsubscribed)}</option>
            <option value="invalid">{t(STATUS_LABEL_KEY.invalid)}</option>
          </select>
        </div>
      }
    />
  );
}
