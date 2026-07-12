"use client";

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Input } from "@/components/ui/input";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatusBadge, type StatusTone } from "@/components/admin/status-badge";

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
const STATUS_LABEL: Record<AdminContactRow["status"], string> = {
  active: "有效",
  unsubscribed: "已退订",
  invalid: "无效",
};

const PAGE_SIZE = 20;

// AdminContacts: read-only, cross-tenant contact library view for the admin
// console. Backed by GET /admin/contacts (s.systemPool(), BYPASSRLS) — there
// is deliberately no create/edit/delete here; admins can look but not touch a
// customer's contacts (governance boundary), so this component never calls a
// write endpoint.
export function AdminContacts() {
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
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, status, tenantId]);

  useEffect(() => {
    load();
  }, [load]);

  const columns: Column<AdminContactRow>[] = [
    { key: "phone", header: "手机号", cell: (r) => <span className="font-mono text-sm">{r.phone}</span> },
    { key: "tenant", header: "租户", cell: (r) => <span className="font-mono text-xs text-muted-foreground">#{r.tenant_id}</span> },
    { key: "country", header: "国家/地区", cell: (r) => <span className="text-sm text-muted-foreground">{r.country_code || "—"}</span> },
    {
      key: "status",
      header: "状态",
      cell: (r) => <StatusBadge tone={STATUS_TONE[r.status]}>{STATUS_LABEL[r.status]}</StatusBadge>,
    },
    { key: "created_at", header: "创建时间", cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.created_at}</span> },
  ];

  return (
    <ProDataTable
      data={rows}
      error={error}
      columns={columns}
      getRowKey={(r) => r.id}
      emptyState="没有符合条件的联系人。"
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
      toolbar={
        <div className="flex items-center gap-2">
          <Input
            value={tenantId}
            onChange={(e) => {
              setTenantId(e.target.value);
              setPage(0);
            }}
            placeholder="租户 ID"
            className="h-8 w-24 font-mono text-sm"
            aria-label="按租户 ID 筛选"
          />
          <select
            value={status}
            onChange={(e) => {
              setStatus(e.target.value);
              setPage(0);
            }}
            className="h-8 rounded-md border bg-transparent px-2 text-sm"
            aria-label="按状态筛选"
          >
            <option value="">全部状态</option>
            <option value="active">有效</option>
            <option value="unsubscribed">已退订</option>
            <option value="invalid">无效</option>
          </select>
        </div>
      }
    />
  );
}
