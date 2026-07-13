"use client";

import { useCallback, useEffect, useState } from "react";
import { Users, Wallet, ShieldCheck, HandCoins } from "lucide-react";
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

/** One direct sub-agent, from GET /sales/overview's sub_agents (see
 *  subAgentRow in agent_api.go). */
interface SubAgent {
  id: number;
  email: string;
  credit_limit: number;
}

/** Mirrors handleAgentOverview's response shape (agent_api.go). `outstanding`
 *  is server-derived as credit_limit-available, so the three figures are
 *  always internally consistent by construction. */
interface Overview {
  credit_limit: number;
  outstanding: number;
  available: number;
  sub_agents: SubAgent[];
  direct_customer_count: number;
}

const usd = (smallest: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(smallest / 100);
const nf = new Intl.NumberFormat("en-US");

/** AgentDownline: the agent's own "downline" view. Credit limit / outstanding
 *  / available and the direct sub-agent list now come from GET /sales/overview
 *  (see handleAgentOverview) — the direct-customer table below remains
 *  separately sourced from GET /sales/customers (handleSalesCustomers) since
 *  overview does not enumerate customers, only counts them. There is still no
 *  endpoint for the FULL subtree's customers (only direct sub-agents are
 *  listed), so downline agents beyond one level are not shown here. */
export function AgentDownline() {
  const [rows, setRows] = useState<Customer[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [allocateOpen, setAllocateOpen] = useState(false);
  const [overview, setOverview] = useState<Overview | null>(null);
  const [overviewError, setOverviewError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(await api.get<Customer[]>("/sales/customers"));
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);

  const loadOverview = useCallback(async () => {
    try {
      setOverview(await api.get<Overview>("/sales/overview"));
      setOverviewError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setOverviewError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);

  useEffect(() => {
    load();
    loadOverview();
  }, [load, loadOverview]);

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

  const selectedCustomers = rows.filter((r) => selected.has(r.id));

  return (
    <>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <MetricCard hero label="信用额度" value={overview ? usd(overview.credit_limit) : "—"} sub="平台授予的总额度" icon={ShieldCheck} />
        <MetricCard
          label="已用/欠款"
          value={overview ? usd(overview.outstanding) : "—"}
          sub="credit_limit − available"
          icon={HandCoins}
        />
        <MetricCard
          label="可用划拨额度"
          value={overview ? usd(overview.available) : overviewError ? "加载失败" : "—"}
          sub="可继续划拨给下线的额度"
          icon={Wallet}
        />
        <MetricCard
          label="直属客户数"
          value={overview ? nf.format(overview.direct_customer_count) : nf.format(rows.length)}
          sub="我负责的租户"
          icon={Users}
        />
      </div>

      {overview && overview.sub_agents.length > 0 && (
        <Card className="mt-4 overflow-hidden p-0">
          <div className="border-b px-4 py-3 text-sm font-medium">下级代理</div>
          <Table>
            <TableHeader>
              <TableRow className="bg-muted/40">
                <TableHead>代理</TableHead>
                <TableHead className="text-right">信用额度</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {overview.sub_agents.map((a) => (
                <TableRow key={a.id}>
                  <TableCell className="font-mono text-sm">
                    #{a.id} {a.email}
                  </TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{usd(a.credit_limit)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}

      <Card className="mt-4 overflow-hidden p-0">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3">
          <div>
            <div className="text-sm font-medium">直属客户 · 划拨对象</div>
            <div className="mt-0.5 text-xs text-muted-foreground">
              勾选一个或多个客户批量划拨额度。下级代理的名下客户暂无独立查询接口，此处仅展示你直属的客户。
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
        onDone={() => {
          setSelected(new Set());
          load();
          loadOverview();
        }}
      />
    </>
  );
}
