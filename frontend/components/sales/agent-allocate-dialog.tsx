"use client";

import { useEffect, useState } from "react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

interface AllocateCustomer {
  id: number;
  name: string;
}

/** Mirrors allocOneResult in internal/api/agent_api.go. */
interface AllocateResult {
  tenant_id: number;
  status: "ok" | "error";
  alloc_id?: number;
  error?: string;
}

interface BatchResponse {
  results: AllocateResult[];
}

/** Single-item shape returned by POST /sales/allocate when items.length === 1
 *  (see handleAgentAllocate) — distinct from the batch {results:[...]} shape. */
interface SingleResponse {
  alloc_id: number;
  tenant_id: number;
  amount: number;
  available: number;
}

/** AgentAllocateDialog: batch-allocates credit to one or more downline
 *  customers via POST /sales/allocate {items:[{tenant_id,amount}]}. The
 *  backend returns a single raw result when items.length===1 and a
 *  {results:[...]} report otherwise (see handleAgentAllocate) — this dialog
 *  normalizes both into the same per-row AllocateResult display, and never
 *  claims a batch succeeded/failed as a whole: each row shows its own outcome. */
export function AgentAllocateDialog({
  open,
  customers,
  onClose,
  onDone,
}: {
  open: boolean;
  customers: AllocateCustomer[];
  onClose: () => void;
  /** Called after a submit attempt. `available` is only known when the
   *  request was a single item that succeeded (the batch shape does not
   *  report a running available balance per item). */
  onDone: (available: number | null) => void;
}) {
  const [amounts, setAmounts] = useState<Record<number, string>>({});
  const [busy, setBusy] = useState(false);
  const [results, setResults] = useState<AllocateResult[] | null>(null);

  useEffect(() => {
    if (open) {
      setAmounts({});
      setResults(null);
    }
  }, [open]);

  const items = customers
    .map((c) => ({ tenant_id: c.id, amount: Math.round(parseFloat(amounts[c.id] ?? "") * 100) }))
    .filter((it) => Number.isFinite(it.amount) && it.amount > 0);

  const valid = customers.length > 0 && items.length === customers.length && !busy;

  async function submit() {
    setBusy(true);
    setResults(null);
    try {
      const data = await api.post<BatchResponse | SingleResponse>("/sales/allocate", { items });
      if ("results" in data) {
        setResults(data.results);
        const okCount = data.results.filter((r) => r.status === "ok").length;
        if (okCount === data.results.length) {
          toast.success("批量划拨完成", { description: `${okCount}/${data.results.length} 笔成功` });
        } else {
          toast.warning("部分划拨失败", { description: `${okCount}/${data.results.length} 笔成功，详见下方明细` });
        }
        onDone(null);
      } else {
        setResults([{ tenant_id: data.tenant_id, status: "ok", alloc_id: data.alloc_id }]);
        toast.success("划拨成功", { description: `划拨后可用额度 $${(data.available / 100).toFixed(2)}` });
        onDone(data.available);
      }
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : "请重试";
      // A single-item request that fails (402 over-limit / 403 out-of-subtree)
      // throws instead of returning a {results:[...]} report — surface it the
      // same way so the row still shows an honest per-item outcome.
      if (items.length === 1) {
        setResults([{ tenant_id: items[0].tenant_id, status: "error", error: msg }]);
      }
      toast.error("划拨失败", { description: msg });
      onDone(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>批量划拨额度</DialogTitle>
          <DialogDescription>
            为选中的 {customers.length} 个客户分别填写划拨金额(USD)。每个客户单独结算，允许部分成功、部分失败。
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-80 space-y-3 overflow-y-auto py-1">
          {customers.map((c) => {
            const r = results?.find((x) => x.tenant_id === c.id);
            return (
              <div key={c.id} className="flex items-center gap-3">
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{c.name}</span>
                <Input
                  type="number"
                  min="0"
                  step="0.01"
                  placeholder="0.00"
                  value={amounts[c.id] ?? ""}
                  disabled={busy}
                  onChange={(e) => setAmounts((prev) => ({ ...prev, [c.id]: e.target.value }))}
                  className="w-28 font-mono"
                />
                {r && (
                  <Badge
                    variant={r.status === "ok" ? "secondary" : "destructive"}
                    className="max-w-[9rem] shrink-0 truncate"
                    title={r.status === "ok" ? `alloc #${r.alloc_id}` : r.error}
                  >
                    {r.status === "ok" ? `#${r.alloc_id}` : (r.error ?? "失败")}
                  </Badge>
                )}
              </div>
            );
          })}
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>关闭</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? "提交中…" : `确认划拨 (${customers.length})`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
