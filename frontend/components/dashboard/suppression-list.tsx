"use client";

// suppression-list.tsx — blacklist / opt-out tab (Task 8, Step 4).
// suppression_list only ever returns {id, reason, added_at} — the backend
// stores a one-way blind index, not the phone, so there is nothing to show
// or search on beyond that. Append-only by compliance design: no delete/
// un-suppress endpoint exists, so this UI has none either.

import { useCallback, useEffect, useState } from "react";
import { Plus, ShieldBan } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";

interface SuppressionRow {
  id: number;
  reason: string;
  added_at: string;
}
const PAGE_SIZE = 20;

export function SuppressionList() {
  const [rows, setRows] = useState<SuppressionRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const d = await api.get<{ rows: SuppressionRow[]; total: number }>(
        `/suppression?limit=${PAGE_SIZE}&offset=${page * PAGE_SIZE}`,
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
  }, [page]);
  useEffect(() => {
    load();
  }, [load]);

  const columns: Column<SuppressionRow>[] = [
    { key: "id", header: "编号", cell: (r) => <span className="font-mono text-xs text-muted-foreground">#{r.id}</span> },
    { key: "reason", header: "原因", cell: (r) => <span className="text-sm">{r.reason || "—"}</span> },
    {
      key: "added_at",
      header: "加入时间",
      cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.added_at.slice(0, 19).replace("T", " ")}</span>,
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          出于合规要求,黑名单仅记录原因与加入时间,不展示号码本身;号码一旦加入不可移除。
        </p>
        <AddSuppressionDialog onDone={load} />
      </div>

      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(r) => r.id}
        emptyState="黑名单为空。"
        server={{
          total,
          page,
          pageSize: PAGE_SIZE,
          onPageChange: setPage,
          query: "",
          onQueryChange: () => {},
          loading,
        }}
      />
    </div>
  );
}

interface SuppressionReport {
  total: number;
  inserted: number;
  duplicates: number;
  invalid: number;
}

function parsePhones(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

// Single dialog covers both "add" (typed/pasted phones) and "bulk import" —
// both hit /suppression/import, which accepts freeform pasted text; a
// dedicated single-phone POST /suppression flow would just duplicate this UI.
function AddSuppressionDialog({ onDone }: { onDone: () => void }) {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const [country, setCountry] = useState("US");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [report, setReport] = useState<SuppressionReport | null>(null);

  const count = parsePhones(text).length;
  const valid = count > 0 && country.trim().length === 2 && !busy;

  function reset() {
    setText("");
    setReason("");
    setReport(null);
  }

  async function submit() {
    setBusy(true);
    try {
      const rep = await api.post<SuppressionReport>("/suppression/import", {
        country: country.trim().toUpperCase(),
        text,
        reason: reason.trim() || undefined,
      });
      setReport(rep);
      onDone();
    } catch (e) {
      toast.error("添加失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) reset();
      }}
    >
      <DialogTrigger render={<Button size="sm" className="gap-1.5" />}>
        <ShieldBan className="size-4" />
        添加黑名单
      </DialogTrigger>

      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>添加 / 导入黑名单</DialogTitle>
          <DialogDescription>
            每行一个号码,或用逗号分隔。加入后不可撤销,请谨慎核对。
          </DialogDescription>
        </DialogHeader>

        {report ? (
          <div className="space-y-3 py-1">
            <div className="grid grid-cols-4 gap-2 text-center">
              <div className="rounded-lg border bg-muted/30 py-3">
                <div className="font-mono text-xl font-semibold tabular-nums">{report.total}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">总数</div>
              </div>
              <div className="rounded-lg border bg-muted/30 py-3">
                <div className="font-mono text-xl font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">{report.inserted}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">新增</div>
              </div>
              <div className="rounded-lg border bg-muted/30 py-3">
                <div className="font-mono text-xl font-semibold tabular-nums text-amber-600 dark:text-amber-400">{report.duplicates}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">重复</div>
              </div>
              <div className="rounded-lg border bg-muted/30 py-3">
                <div className="font-mono text-xl font-semibold tabular-nums text-rose-600 dark:text-rose-400">{report.invalid}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">无效</div>
              </div>
            </div>
          </div>
        ) : (
          <div className="space-y-4 py-1">
            <div className="flex gap-3">
              <div className="space-y-2">
                <label htmlFor="sup-country" className="text-sm font-medium">国家 <span className="font-mono text-xs text-muted-foreground">ISO-2</span></label>
                <Input id="sup-country" value={country} onChange={(e) => setCountry(e.target.value)} maxLength={2} className="w-24 font-mono uppercase" />
              </div>
              <div className="flex-1 space-y-2">
                <label htmlFor="sup-reason" className="text-sm font-medium">原因 <span className="font-mono text-xs text-muted-foreground">可选</span></label>
                <Input id="sup-reason" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="用户投诉 / 主动退订…" />
              </div>
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <label htmlFor="sup-text" className="text-sm font-medium">号码列表</label>
                <span className="font-mono text-xs tabular-nums text-muted-foreground">{count} 个号码</span>
              </div>
              <Textarea
                id="sup-text"
                value={text}
                onChange={(e) => setText(e.target.value)}
                placeholder={"每行一个号码,或用逗号分隔\n+8613800000000\n+8613900000000"}
                className="h-32 resize-none font-mono text-sm"
              />
            </div>
          </div>
        )}

        <DialogFooter>
          {report ? (
            <>
              <Button variant="ghost" onClick={reset}>再添加一批</Button>
              <DialogClose render={<Button />}>完成</DialogClose>
            </>
          ) : (
            <>
              <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
              <Button onClick={submit} disabled={!valid}>
                <Plus className="size-4" />
                {busy ? "提交中…" : `确认添加 ${count} 条`}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
