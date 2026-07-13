"use client";

import { useCallback, useEffect, useState } from "react";
import { Users, Wallet, Layers, HandCoins } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { MetricCard } from "@/components/metric-card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { AgentAllocateDialog } from "@/components/sales/agent-allocate-dialog";

interface Customer {
  id: number;
  name: string;
  status: string;
  balance: number;
  frozen: number;
}

/** Subset of GET /sales/statement's response (see settlementJSON) — only the
 *  fields this rollup card renders. */
interface StatementSummary {
  period: string;
  retail_subtree: number;
}

const usd = (smallest: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(smallest / 100);
const nf = new Intl.NumberFormat("en-US");

function currentMonth(): string {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
}

/** AgentDownline: the agent's own "downline" view. There is no sales-facing
 *  endpoint that lists sub-agents or rolls up the whole subtree's customers
 *  (GET /sales/customers only returns tenants.sales_owner_id = me — see
 *  handleSalesCustomers), so this honestly shows:
 *   - the direct-customer list (also the allocation target picker), and
 *   - the whole-subtree settled consumption for the current month, sourced
 *     from GET /sales/statement's retail_subtree (the one number the backend
 *     does roll up across the full subtree).
 *  Credit limit / outstanding have no dedicated read endpoint either; only
 *  `available` is known, and only right after a successful single-item
 *  allocation (its response includes it) — shown honestly as unknown until then. */
export function AgentDownline() {
  const [rows, setRows] = useState<Customer[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [allocateOpen, setAllocateOpen] = useState(false);
  const [subtreeSummary, setSubtreeSummary] = useState<StatementSummary | null>(null);
  const [lastAvailable, setLastAvailable] = useState<number | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(await api.get<Customer[]>("/sales/customers"));
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);

  const loadSummary = useCallback(async () => {
    try {
      setSubtreeSummary(await api.get<StatementSummary>(`/sales/statement?month=${currentMonth()}`));
    } catch {
      // Non-fatal: the rollup card just shows "—" if this fails.
    }
  }, []);

  useEffect(() => {
    load();
    loadSummary();
  }, [load, loadSummary]);

  function toggle(id: number) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  if (error) return <Card className="p-5 text-sm text-muted-foreground">加载失败:{error}</Card>;
  if (!rows) return <div className="h-64 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />;

  const totalBalance = rows.reduce((s, r) => s + r.balance, 0);
  const selectedCustomers = rows.filter((r) => selected.has(r.id));

  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <MetricCard hero label="直属客户数" value={nf.format(rows.length)} sub="我负责的租户" icon={Users} />
        <MetricCard label="直属余额合计" value={usd(totalBalance)} sub="直属客户可用余额" icon={Wallet} />
        <MetricCard
          label="本月下线消费(结算)"
          value={subtreeSummary ? usd(subtreeSummary.retail_subtree) : "—"}
          sub={subtreeSummary ? `含整条下线 · ${subtreeSummary.period}` : "统计接口暂不可用"}
          icon={Layers}
        />
        <MetricCard
          label="可用划拨额度"
          value={lastAvailable != null ? usd(lastAvailable) : "—"}
          sub={lastAvailable != null ? "上次划拨返回值" : "需完成一次划拨后才可见"}
          icon={HandCoins}
        />
      </div>

      <Card className="mt-4 overflow-hidden p-0">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3">
          <div>
            <div className="text-sm font-medium">直属客户 · 划拨对象</div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              勾选一个或多个客户批量划拨额度。下级代理及其名下客户暂无独立查询接口，此处仅展示你直属的客户。
            </div>
          </div>
          <Button size="sm" disabled={selected.size === 0} onClick={() => setAllocateOpen(true)} className="gap-1.5">
            <HandCoins className="size-3.5" />
            批量划拨额度{selected.size > 0 && ` (${selected.size})`}
          </Button>
        </div>
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/40">
              <TableHead className="w-8" />
              <TableHead>客户</TableHead>
              <TableHead>状态</TableHead>
              <TableHead className="text-right">可用余额</TableHead>
              <TableHead className="text-right">冻结</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="py-10 text-center text-sm text-muted-foreground">
                  你名下还没有客户。请联系管理员为你分配，或先发展下级代理。
                </TableCell>
              </TableRow>
            ) : (
              rows.map((r) => (
                <TableRow key={r.id}>
                  <TableCell>
                    <input
                      type="checkbox"
                      aria-label={`选择 ${r.name}`}
                      checked={selected.has(r.id)}
                      onChange={() => toggle(r.id)}
                      className="size-3.5 accent-brand-600"
                    />
                  </TableCell>
                  <TableCell className="font-medium">{r.name}</TableCell>
                  <TableCell>
                    <Badge variant={r.status === "active" ? "secondary" : "outline"}>{r.status}</Badge>
                  </TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{usd(r.balance)}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums text-muted-foreground">
                    {usd(r.frozen)}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Card>

      <AgentAllocateDialog
        open={allocateOpen}
        customers={selectedCustomers}
        onClose={() => setAllocateOpen(false)}
        onDone={(available) => {
          if (available != null) setLastAvailable(available);
          setSelected(new Set());
          load();
        }}
      />
    </>
  );
}
