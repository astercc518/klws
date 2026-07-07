"use client";

import { useEffect, useMemo, useState } from "react";
import { Search, ChevronLeft, ChevronRight } from "lucide-react";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";

export interface Column<T> {
  key: string;
  header: React.ReactNode;
  cell: (row: T) => React.ReactNode;
  align?: "left" | "right";
  headClassName?: string;
  cellClassName?: string;
}

/** Server-driven mode: the parent owns paging/search and fetches each page. */
export interface ServerMode {
  total: number;
  page: number; // 0-based
  pageSize: number;
  onPageChange: (page: number) => void;
  query: string;
  onQueryChange: (q: string) => void;
  loading?: boolean;
}

interface ProDataTableProps<T> {
  data: T[] | null;
  columns: Column<T>[];
  getRowKey: (row: T) => string | number;
  /** Client-mode search box; ignored when `server` is set. */
  search?: { placeholder?: string; accessor: (row: T) => string };
  toolbar?: React.ReactNode;
  pageSize?: number;
  emptyState?: React.ReactNode;
  error?: string | null;
  rowActions?: (row: T) => React.ReactNode;
  /** When set, the table is server-driven: no local filter/slice. */
  server?: ServerMode;
  /** When set, clicking a row calls this (rows become cursor-pointer). */
  onRowClick?: (row: T) => void;
}

const alignClass = (a?: "left" | "right") => (a === "right" ? "text-right" : "text-left");

export function ProDataTable<T>({
  data,
  columns,
  getRowKey,
  search,
  toolbar,
  pageSize = 10,
  emptyState,
  error,
  rowActions,
  server,
  onRowClick,
}: ProDataTableProps<T>) {
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);

  // Server mode: debounce the search input, then notify the parent.
  const [serverInput, setServerInput] = useState(server?.query ?? "");
  useEffect(() => {
    if (!server) return;
    const id = setTimeout(() => {
      if (serverInput !== server.query) server.onQueryChange(serverInput);
    }, 300);
    return () => clearTimeout(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [serverInput]);

  const filtered = useMemo(() => {
    if (!data) return [];
    if (server) return data; // server already returns the current page
    const q = query.trim().toLowerCase();
    if (!q || !search) return data;
    return data.filter((row) => search.accessor(row).toLowerCase().includes(q));
  }, [data, query, search, server]);

  const effPageSize = server ? server.pageSize : pageSize;
  const total = server ? server.total : filtered.length;
  const pageCount = Math.max(1, Math.ceil(total / effPageSize));
  const current = server ? server.page : Math.min(page, pageCount - 1);
  const start = current * effPageSize;
  const rows = server ? filtered : filtered.slice(start, start + effPageSize);
  const colSpan = columns.length + (rowActions ? 1 : 0);
  const showSkeleton = data === null || (server?.loading ?? false);

  function changeQuery(v: string) {
    if (server) {
      setServerInput(v);
    } else {
      setQuery(v);
      setPage(0);
    }
  }
  const queryValue = server ? serverInput : query;

  function goTo(p: number) {
    if (server) server.onPageChange(p);
    else setPage(p);
  }

  return (
    <Card className="gap-0 p-0">
      {(search || toolbar) && (
        <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2.5">
          {search && (
            <div className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-lg border bg-muted/40 px-2.5 text-muted-foreground sm:max-w-xs">
              <Search className="size-3.5 shrink-0" />
              <input
                type="search"
                value={queryValue}
                onChange={(e) => changeQuery(e.target.value)}
                placeholder={search.placeholder ?? "搜索…"}
                className="min-w-0 flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground/70"
              />
            </div>
          )}
          {toolbar && <div className="ml-auto flex items-center gap-2">{toolbar}</div>}
        </div>
      )}

      <Table>
        <TableHeader>
          <TableRow className="bg-muted/30 hover:bg-muted/30">
            {columns.map((c) => (
              <TableHead
                key={c.key}
                className={cn(
                  "h-9 font-mono text-[11px] uppercase tracking-wider text-muted-foreground",
                  alignClass(c.align),
                  c.headClassName,
                )}
              >
                {c.header}
              </TableHead>
            ))}
            {rowActions && <TableHead className="w-12" />}
          </TableRow>
        </TableHeader>
        <TableBody>
          {error ? (
            <TableRow className="hover:bg-transparent">
              <TableCell colSpan={colSpan} className="py-12 text-center text-sm text-muted-foreground">
                加载失败:{error}
              </TableCell>
            </TableRow>
          ) : showSkeleton ? (
            Array.from({ length: 5 }).map((_, i) => (
              <TableRow key={i} className="hover:bg-transparent">
                <TableCell colSpan={colSpan} className="py-2">
                  <div className="h-6 animate-pulse rounded bg-muted motion-reduce:animate-none" />
                </TableCell>
              </TableRow>
            ))
          ) : rows.length === 0 ? (
            <TableRow className="hover:bg-transparent">
              <TableCell colSpan={colSpan} className="py-12 text-center text-sm text-muted-foreground">
                {queryValue.trim()
                  ? `没有匹配「${queryValue.trim()}」的结果`
                  : (emptyState ?? "暂无数据")}
              </TableCell>
            </TableRow>
          ) : (
            rows.map((row) => (
              <TableRow
                key={getRowKey(row)}
                className={onRowClick ? "cursor-pointer" : undefined}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
              >
                {columns.map((c) => (
                  <TableCell
                    key={c.key}
                    className={cn("py-2.5", alignClass(c.align), c.cellClassName)}
                  >
                    {c.cell(row)}
                  </TableCell>
                ))}
                {rowActions && (
                  <TableCell className="py-2.5 text-right" onClick={(e) => e.stopPropagation()}>
                    {rowActions(row)}
                  </TableCell>
                )}
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>

      {!showSkeleton && !error && total > 0 && (
        <div className="flex items-center justify-between gap-3 border-t px-3 py-2.5">
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {start + 1}–{Math.min(start + effPageSize, total)} / {total}
          </span>
          {pageCount > 1 && (
            <div className="flex items-center gap-1">
              <Button
                variant="outline"
                size="icon-sm"
                aria-label="上一页"
                disabled={current === 0}
                onClick={() => goTo(current - 1)}
              >
                <ChevronLeft className="size-4" />
              </Button>
              <span className="px-1 font-mono text-xs tabular-nums text-muted-foreground">
                {current + 1} / {pageCount}
              </span>
              <Button
                variant="outline"
                size="icon-sm"
                aria-label="下一页"
                disabled={current >= pageCount - 1}
                onClick={() => goTo(current + 1)}
              >
                <ChevronRight className="size-4" />
              </Button>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}
