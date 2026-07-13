"use client";

// admin-agent-tree.tsx — agent hierarchy overview + terms/parent editing
// (module 9 / T8 step 2). Mirrors GET /admin/agents (agent_admin.go) and
// POST /admin/agents/:id/terms + /:id/parent (agent_api.go).
//
// Rendered as a flat table with a "上级" column rather than a nested tree
// widget — the backend already returns every agent with parent_id, so a flat
// table keeps the join (id → email) trivial and the reparent cycle/non-sales
// validation is enforced server-side anyway (400s are surfaced honestly).

import { useCallback, useEffect, useState } from "react";
import { Pencil, GitBranch } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

/** Mirrors agentListRow in internal/api/agent_admin.go. */
interface AgentRow {
  id: number;
  email: string;
  parent_id: number | null;
  credit_limit: number;
  commission_rate: number | null;
}

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);
const pct = (rate: number) => `${Math.round(rate * 10000) / 100}%`;

export function AdminAgentTree() {
  const [rows, setRows] = useState<AgentRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [termsTarget, setTermsTarget] = useState<AgentRow | null>(null);
  const [parentTarget, setParentTarget] = useState<AgentRow | null>(null);

  const load = useCallback(async () => {
    try {
      const d = await api.get<{ rows: AgentRow[] }>("/admin/agents");
      setRows(d.rows);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  if (error) return <Card className="p-5 text-sm text-muted-foreground">加载失败:{error}</Card>;
  if (!rows) return <div className="h-64 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />;

  const byId = new Map<number, AgentRow>(rows.map((r) => [r.id, r]));

  return (
    <div className="space-y-4">
      <Card className="overflow-hidden p-0">
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/40">
              <TableHead>代理</TableHead>
              <TableHead>上级</TableHead>
              <TableHead>信用额度</TableHead>
              <TableHead>返佣比例</TableHead>
              <TableHead className="w-12" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="py-10 text-center text-sm text-muted-foreground">
                  暂无代理账号。
                </TableCell>
              </TableRow>
            ) : (
              rows.map((r) => {
                const parent = r.parent_id != null ? byId.get(r.parent_id) : undefined;
                return (
                  <TableRow key={r.id}>
                    <TableCell className="font-mono text-sm">
                      #{r.id} {r.email}
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {r.parent_id == null ? (
                        <Badge variant="outline">顶层</Badge>
                      ) : (
                        (parent?.email ?? `#${r.parent_id}`)
                      )}
                    </TableCell>
                    <TableCell className="font-mono text-sm tabular-nums">{usd(r.credit_limit)}</TableCell>
                    <TableCell className="font-mono text-sm tabular-nums">
                      {r.commission_rate != null ? pct(r.commission_rate) : "—"}
                    </TableCell>
                    <TableCell>
                      <DropdownMenu>
                        <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label="操作" />}>
                          <Pencil className="size-4" />
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem onClick={() => setTermsTarget(r)}>
                            <Pencil className="size-4" />
                            改条款(额度/返佣)
                          </DropdownMenuItem>
                          <DropdownMenuItem onClick={() => setParentTarget(r)}>
                            <GitBranch className="size-4" />
                            改上级
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </Card>

      <TermsDialog target={termsTarget} onClose={() => setTermsTarget(null)} onDone={load} />
      <ParentDialog target={parentTarget} agents={rows} onClose={() => setParentTarget(null)} onDone={load} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Terms dialog → POST /admin/agents/:id/terms {commission_rate?, credit_limit?}
// ---------------------------------------------------------------------------

function TermsDialog({
  target,
  onClose,
  onDone,
}: {
  target: AgentRow | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const [creditLimit, setCreditLimit] = useState("");
  const [ratePct, setRatePct] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (target) {
      setCreditLimit((target.credit_limit / 100).toFixed(2));
      setRatePct(target.commission_rate != null ? String(Math.round(target.commission_rate * 10000) / 100) : "");
    }
  }, [target]);

  const creditTrim = creditLimit.trim();
  const creditCents = creditTrim === "" ? 0 : Math.round(parseFloat(creditLimit) * 100);
  const creditValid = creditTrim === "" || (!Number.isNaN(creditCents) && creditCents >= 0);

  const rateTrim = ratePct.trim();
  const rateRaw = rateTrim === "" ? 0 : Number(ratePct);
  const rateValid = rateTrim === "" || (!Number.isNaN(rateRaw) && rateRaw >= 0 && rateRaw <= 100);

  const valid = !busy && creditValid && rateValid;

  async function submit() {
    if (!target) return;
    const body: Record<string, unknown> = {};
    if (creditTrim !== "") body.credit_limit = creditCents;
    if (rateTrim !== "") body.commission_rate = rateRaw / 100;

    setBusy(true);
    try {
      await api.post(`/admin/agents/${target.id}/terms`, body);
      toast.success("条款已更新", { description: target.email });
      onClose();
      onDone();
    } catch (e) {
      toast.error("更新失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>改条款</DialogTitle>
          <DialogDescription>
            为 <span className="font-mono">{target?.email}</span> 设置信用额度与返佣比例。留空表示不修改该项。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-1">
          <div className="space-y-2">
            <label htmlFor="terms-credit" className="text-sm font-medium">
              信用额度 <span className="font-mono text-xs text-muted-foreground">USD</span>
            </label>
            <Input
              id="terms-credit"
              type="number"
              min="0"
              step="0.01"
              value={creditLimit}
              onChange={(e) => setCreditLimit(e.target.value)}
              placeholder="1000.00"
              className="font-mono"
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="terms-rate" className="text-sm font-medium">
              返佣比例(%) <span className="font-mono text-xs text-muted-foreground">0-100</span>
            </label>
            <Input
              id="terms-rate"
              type="number"
              min={0}
              max={100}
              step={0.1}
              value={ratePct}
              onChange={(e) => setRatePct(e.target.value)}
              placeholder="例如 10"
              className="font-mono"
            />
          </div>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Reparent dialog → POST /admin/agents/:id/parent {parent_id}
// Backend rejects cycles / non-sales targets with 400 — surfaced via toast.
// ---------------------------------------------------------------------------

function ParentDialog({
  target,
  agents,
  onClose,
  onDone,
}: {
  target: AgentRow | null;
  agents: AgentRow[];
  onClose: () => void;
  onDone: () => void;
}) {
  const [parentId, setParentId] = useState<string>("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (target) setParentId(target.parent_id != null ? String(target.parent_id) : "");
  }, [target]);

  const valid = !busy;
  const options = agents.filter((a) => a.id !== target?.id);

  async function submit() {
    if (!target) return;
    setBusy(true);
    try {
      await api.post(`/admin/agents/${target.id}/parent`, {
        parent_id: parentId === "" ? null : Number(parentId),
      });
      toast.success("上级已更新", { description: target.email });
      onClose();
      onDone();
    } catch (e) {
      toast.error("改上级失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={target != null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>改上级</DialogTitle>
          <DialogDescription>
            为 <span className="font-mono">{target?.email}</span> 选择新的上级代理,或设为顶层。会产生环路或指向非代理账号的请求会被后端拒绝。
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2 py-1">
          <label htmlFor="parent-select" className="text-sm font-medium">上级代理</label>
          <select
            id="parent-select"
            value={parentId}
            onChange={(e) => setParentId(e.target.value)}
            className="h-9 w-full rounded-lg border bg-transparent px-2.5 text-sm outline-none"
          >
            <option value="">(设为顶层 · 无上级)</option>
            {options.map((a) => (
              <option key={a.id} value={String(a.id)}>
                #{a.id} {a.email}
              </option>
            ))}
          </select>
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
          <Button onClick={submit} disabled={!valid}>{busy ? "提交中…" : "确认修改"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
