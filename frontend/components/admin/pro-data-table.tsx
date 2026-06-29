"use client";

import { useMemo, useState } from "react";
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

interface ProDataTableProps<T> {
  data: T[] | null;
  columns: Column<T>[];
  getRowKey: (row: T) => string | number;
  /** Enables the toolbar search box; `accessor` builds the haystack per row. */
  search?: { placeholder?: string; accessor: (row: T) => string };
  /** Extra toolbar controls on the right (filter dropdowns, import buttons). */
  toolbar?: React.ReactNode;
  pageSize?: number;
  /** Shown when data loaded but is empty (or filtered to nothing). */
  emptyState?: React.ReactNode;
  /** Shown in place of the body while data is null. */
  error?: string | null;
  rowActions?: (row: T) => React.ReactNode;
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
}: ProDataTableProps<T>) {
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);

  const filtered = useMemo(() => {
    if (!data) return [];
    const q = query.trim().toLowerCase();
    if (!q || !search) return data;
    return data.filter((row) => search.accessor(row).toLowerCase().includes(q));
  }, [data, query, search]);

  const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize));
  const current = Math.min(page, pageCount - 1);
  const start = current * pageSize;
  const rows = filtered.slice(start, start + pageSize);
  const colSpan = columns.length + (rowActions ? 1 : 0);

  function changeQuery(v: string) {
    setQuery(v);
    setPage(0); // re-filtering invalidates the current page offset
  }

  return (
    <Card className="gap-0 p-0">
      {/* Toolbar */}
      {(search || toolbar) && (
        <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2.5">
          {search && (
            <div className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-lg border bg-muted/40 px-2.5 text-muted-foreground sm:max-w-xs">
              <Search className="size-3.5 shrink-0" />
              <input
                type="search"
                value={query}
                onChange={(e) => changeQuery(e.target.value)}
                placeholder={search.placeholder ?? "搜索…"}
                className="min-w-0 flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground/70"
              />
            </div>
          )}
          {toolbar && <div className="ml-auto flex items-center gap-2">{toolbar}</div>}
        </div>
      )}

      {/* Body */}
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
          ) : !data ? (
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
                {query.trim()
                  ? `没有匹配「${query.trim()}」的结果`
                  : (emptyState ?? "暂无数据")}
              </TableCell>
            </TableRow>
          ) : (
            rows.map((row) => (
              <TableRow key={getRowKey(row)}>
                {columns.map((c) => (
                  <TableCell
                    key={c.key}
                    className={cn("py-2.5", alignClass(c.align), c.cellClassName)}
                  >
                    {c.cell(row)}
                  </TableCell>
                ))}
                {rowActions && (
                  <TableCell className="py-2.5 text-right">{rowActions(row)}</TableCell>
                )}
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>

      {/* Pagination footer */}
      {data && filtered.length > 0 && (
        <div className="flex items-center justify-between gap-3 border-t px-3 py-2.5">
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {start + 1}–{Math.min(start + pageSize, filtered.length)} / {filtered.length}
          </span>
          {pageCount > 1 && (
            <div className="flex items-center gap-1">
              <Button
                variant="outline"
                size="icon-sm"
                aria-label="上一页"
                disabled={current === 0}
                onClick={() => setPage(current - 1)}
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
                onClick={() => setPage(current + 1)}
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
