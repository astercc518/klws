"use client";

import { useEffect, useMemo, useState } from "react";
import { Search, ChevronLeft, ChevronRight, Columns3, Check } from "lucide-react";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { useT } from "@/components/locale-provider";

export interface Column<T> {
  key: string;
  header: React.ReactNode;
  cell: (row: T) => React.ReactNode;
  align?: "left" | "right";
  headClassName?: string;
  cellClassName?: string;
  /** Plain-text name shown in the column-visibility menu. Defaults to key. */
  title?: string;
  /** Set false to pin the column (not hideable). Default true. */
  hideable?: boolean;
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

/** Controlled row selection: the parent owns the selected-key set. */
export interface SelectionMode {
  /** Controlled set of selected row keys (from getRowKey). */
  selected: ReadonlySet<string | number>;
  onChange: (next: Set<string | number>) => void;
  /** Rendered on the right side of the bulk bar. */
  actions?: (keys: Array<string | number>) => React.ReactNode;
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
  /** When set, renders a checkbox column and a bulk action bar. */
  selection?: SelectionMode;
  /** When set, the toolbar gains a "Columns" menu that hides columns and
   *  persists the choice to `localStorage["pdt:{storageKey}:hidden"]`. */
  storageKey?: string;
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
  selection,
  storageKey,
}: ProDataTableProps<T>) {
  const t = useT();
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
  const showSkeleton = data === null || (server?.loading ?? false);

  // Column visibility (only active when storageKey is set).
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  useEffect(() => {
    if (!storageKey) return;
    // Always reset on storageKey change: a key with no stored record (or a
    // corrupted one) must show all columns, not inherit the previous key's set.
    let next = new Set<string>();
    try {
      const raw = localStorage.getItem(`pdt:${storageKey}:hidden`);
      if (raw) next = new Set(JSON.parse(raw) as string[]);
    } catch {} // 损坏的存储值静默忽略,等同默认全显
    // localStorage is browser-only, so this can't run during render/SSR.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setHidden(next);
  }, [storageKey]);

  function toggleColumn(key: string) {
    setHidden((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      if (storageKey) {
        try {
          localStorage.setItem(`pdt:${storageKey}:hidden`, JSON.stringify([...next]));
        } catch {}
      }
      return next;
    });
  }

  const visibleColumns = useMemo(
    () => (storageKey ? columns.filter((c) => !hidden.has(c.key)) : columns),
    [columns, hidden, storageKey],
  );

  const colSpan = visibleColumns.length + (rowActions ? 1 : 0) + (selection ? 1 : 0);

  // Current-page selection state (rows are the current page's rows).
  const pageKeys = rows.map((r) => getRowKey(r));
  const allPageSelected =
    !!selection && pageKeys.length > 0 && pageKeys.every((k) => selection.selected.has(k));
  const somePageSelected = !!selection && pageKeys.some((k) => selection.selected.has(k));

  function togglePage() {
    if (!selection) return;
    const next = new Set(selection.selected);
    if (allPageSelected) pageKeys.forEach((k) => next.delete(k));
    else pageKeys.forEach((k) => next.add(k));
    selection.onChange(next);
  }
  function toggleRow(key: string | number) {
    if (!selection) return;
    const next = new Set(selection.selected);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    selection.onChange(next);
  }

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
      {(search || toolbar || storageKey) && (
        <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2.5">
          {search && (
            <div className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-lg border bg-muted/40 px-2.5 text-muted-foreground sm:max-w-xs">
              <Search className="size-3.5 shrink-0" />
              <input
                type="search"
                value={queryValue}
                onChange={(e) => changeQuery(e.target.value)}
                placeholder={search.placeholder ?? t("table.searchPlaceholder")}
                className="min-w-0 flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground/70"
              />
            </div>
          )}
          {storageKey && (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<Button variant="outline" size="sm" className="ml-auto text-muted-foreground" />}
              >
                <Columns3 className="size-3.5" />
                {t("table.columns")}
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {columns
                  .filter((c) => c.hideable !== false)
                  .map((c) => (
                    <DropdownMenuItem key={c.key} closeOnClick={false} onClick={() => toggleColumn(c.key)}>
                      <span className="flex size-4 items-center justify-center">
                        {!hidden.has(c.key) && <Check className="size-3.5" />}
                      </span>
                      {c.title ?? c.key}
                    </DropdownMenuItem>
                  ))}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          {toolbar && (
            <div className={cn("flex items-center gap-2", !storageKey && "ml-auto")}>{toolbar}</div>
          )}
        </div>
      )}

      {selection && selection.selected.size > 0 && (
        <div className="flex flex-wrap items-center gap-2 border-b bg-accent/60 px-3 py-2">
          <span className="text-sm font-medium">
            {t("table.selected").replace("{n}", String(selection.selected.size))}
          </span>
          <Button
            variant="ghost"
            size="sm"
            className="text-muted-foreground"
            onClick={() => selection.onChange(new Set())}
          >
            {t("table.clearSelection")}
          </Button>
          <div className="ml-auto flex items-center gap-2">
            {selection.actions?.(Array.from(selection.selected))}
          </div>
        </div>
      )}

      <Table>
        <TableHeader>
          <TableRow className="bg-muted/30 hover:bg-muted/30">
            {selection && (
              <TableHead className="w-10">
                <input
                  type="checkbox"
                  aria-label={t("table.selectAll")}
                  className="size-3.5 accent-primary"
                  checked={allPageSelected}
                  ref={(el) => {
                    if (el) el.indeterminate = !allPageSelected && somePageSelected;
                  }}
                  onChange={togglePage}
                />
              </TableHead>
            )}
            {visibleColumns.map((c) => (
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
                {t("table.loadFailed")}{error}
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
                  ? t("table.noMatch").replace("{q}", queryValue.trim())
                  : (emptyState ?? t("table.empty"))}
              </TableCell>
            </TableRow>
          ) : (
            rows.map((row) => (
              <TableRow
                key={getRowKey(row)}
                className={onRowClick ? "cursor-pointer" : undefined}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
              >
                {selection && (
                  <TableCell className="w-10 py-2.5" onClick={(e) => e.stopPropagation()}>
                    <input
                      type="checkbox"
                      aria-label={t("table.selectRow")}
                      className="size-3.5 accent-primary"
                      checked={selection.selected.has(getRowKey(row))}
                      onChange={() => toggleRow(getRowKey(row))}
                    />
                  </TableCell>
                )}
                {visibleColumns.map((c) => (
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
                aria-label={t("table.prevPage")}
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
                aria-label={t("table.nextPage")}
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
