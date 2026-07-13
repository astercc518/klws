"use client";

// admin-agent-settlements.tsx — monthly settlement overview + close
// (module 9 / T8 step 3). Mirrors GET /admin/agent/settlements?month= and
// POST /admin/agent/settlements/close?month= (agent_admin.go, MONEY-CRITICAL).
//
// The overview table is always a LIVE recomputation (handleAdminSettlementOverview
// calls agentSettlement fresh every time, it does not read agent_settlements).
// "关账" persists that month's figures once; re-closing an already-closed
// month is a no-op that returns the FROZEN persisted row, not a fresh
// recomputation — the close-result panel below is shown separately from the
// live overview table so a re-close never silently overwrites what's on
// screen with numbers that look "live" when they're actually just an echo of
// what was already stored.

import { useCallback, useEffect, useState } from "react";
import { CalendarCheck, Lock } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

/** Mirrors settlementJSON in internal/api/agent_api.go. */
interface SettlementRow {
  agent_id: number;
  period: string;
  retail_direct: number;
  cost_direct: number;
  retail_subtree: number;
  rebate_rate: number;
  debt: number;
  margin: number;
  rebate: number;
  net: number;
}
interface OverviewResponse {
  period: string;
  agents: SettlementRow[];
}

interface ClosedRow {
  agent_id: number;
  period: string;
  debt: number;
  margin: number;
  rebate: number;
  net: number;
  newly_closed: boolean;
}
interface CloseResponse {
  period: string;
  closed: ClosedRow[];
}

interface AgentRow {
  id: number;
  email: string;
}

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

function currentMonth(): string {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
}

export function AdminAgentSettlements() {
  const [month, setMonth] = useState(currentMonth());
  const [data, setData] = useState<OverviewResponse | null>(null);
  const [agents, setAgents] = useState<AgentRow[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [closing, setClosing] = useState(false);
  const [closeResult, setCloseResult] = useState<CloseResponse | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setCloseResult(null);
    try {
      const [overview, agentList] = await Promise.all([
        api.get<OverviewResponse>(`/admin/agent/settlements?month=${month}`),
        api.get<{ rows: AgentRow[] }>("/admin/agents"),
      ]);
      setData(overview);
      setAgents(agentList.rows);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    } finally {
      setLoading(false);
    }
  }, [month]);

  useEffect(() => {
    load();
  }, [load]);

  async function closeMonth() {
    setClosing(true);
    try {
      const r = await api.post<CloseResponse>(`/admin/agent/settlements/close?month=${month}`);
      setCloseResult(r);
      const newlyCount = r.closed.filter((c) => c.newly_closed).length;
      if (newlyCount === 0) {
        toast.info("本月已关账", { description: `${r.period} 此前已关闭,以下为已持久化的结果` });
      } else {
        toast.success("关账完成", {
          description: `${r.period} · 新关闭 ${newlyCount}/${r.closed.length} 个代理`,
        });
      }
    } catch (e) {
      toast.error("关账失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setClosing(false);
    }
  }

  const emailOf = (id: number) => agents.find((a) => a.id === id)?.email ?? `#${id}`;

  return (
    <div className="space-y-6">
      <Card className="flex flex-wrap items-center gap-3 p-4">
        <label htmlFor="settlement-month" className="text-sm font-medium">
          结算月份
        </label>
        <Input
          id="settlement-month"
          type="month"
          value={month}
          onChange={(e) => e.target.value && setMonth(e.target.value)}
          className="w-40 font-mono"
        />
        {loading && <span className="text-xs text-muted-foreground">加载中…</span>}
        <Button size="sm" className="ml-auto gap-1.5" disabled={closing} onClick={closeMonth}>
          <Lock className="size-3.5" />
          {closing ? "关账中…" : "关账"}
        </Button>
      </Card>

      {error && <Card className="p-5 text-sm text-muted-foreground">加载失败:{error}</Card>}

      {closeResult && (
        <Card className="p-5">
          <div className="mb-3 flex items-center gap-2 text-sm font-medium">
            <CalendarCheck className="size-4" />
            {closeResult.period} 关账结果
          </div>
          <Table>
            <TableHeader>
              <TableRow className="bg-muted/40">
                <TableHead>代理</TableHead>
                <TableHead>状态</TableHead>
                <TableHead className="text-right">欠款</TableHead>
                <TableHead className="text-right">差价</TableHead>
                <TableHead className="text-right">返佣</TableHead>
                <TableHead className="text-right">净额</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {closeResult.closed.map((c) => (
                <TableRow key={c.agent_id}>
                  <TableCell className="font-mono text-sm">{emailOf(c.agent_id)}</TableCell>
                  <TableCell>
                    <Badge variant={c.newly_closed ? "secondary" : "outline"}>
                      {c.newly_closed ? "本次新关闭" : "此前已关闭"}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{usd(c.debt)}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{usd(c.margin)}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{usd(c.rebate)}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums font-semibold">{usd(c.net)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}

      <Card className="overflow-hidden p-0">
        <div className="border-b px-4 py-3 text-sm font-medium">
          {data?.period ?? month} 结算总览
          <span className="ml-2 font-mono text-xs font-normal text-muted-foreground">实时计算,非持久化数值</span>
        </div>
        <Table>
          <TableHeader>
            <TableRow className="bg-muted/40">
              <TableHead>代理</TableHead>
              <TableHead className="text-right">直属零售</TableHead>
              <TableHead className="text-right">直属成本</TableHead>
              <TableHead className="text-right">下线零售</TableHead>
              <TableHead className="text-right">返佣比例</TableHead>
              <TableHead className="text-right">欠款</TableHead>
              <TableHead className="text-right">差价</TableHead>
              <TableHead className="text-right">返佣</TableHead>
              <TableHead className="text-right">净额</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {!data || data.agents.length === 0 ? (
              <TableRow>
                <TableCell colSpan={9} className="py-10 text-center text-sm text-muted-foreground">
                  {data ? "本月暂无代理结算数据。" : "加载中…"}
                </TableCell>
              </TableRow>
            ) : (
              data.agents.map((a) => (
                <TableRow key={a.agent_id}>
                  <TableCell className="font-mono text-sm">{emailOf(a.agent_id)}</TableCell>
                  <TableCell className="text-right font-mono text-sm tabular-nums">{usd(a.retail_direct)}</TableCell>
                  <TableCell className="text-right font-mono text-sm tabular-nums">{usd(a.cost_direct)}</TableCell>
                  <TableCell className="text-right font-mono text-sm tabular-nums">{usd(a.retail_subtree)}</TableCell>
                  <TableCell className="text-right font-mono text-sm tabular-nums">
                    {Math.round(a.rebate_rate * 10000) / 100}%
                  </TableCell>
                  <TableCell className="text-right font-mono text-sm tabular-nums">{usd(a.debt)}</TableCell>
                  <TableCell className="text-right font-mono text-sm tabular-nums">{usd(a.margin)}</TableCell>
                  <TableCell className="text-right font-mono text-sm tabular-nums">{usd(a.rebate)}</TableCell>
                  <TableCell className="text-right font-mono text-sm font-semibold tabular-nums">{usd(a.net)}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Card>
    </div>
  );
}
