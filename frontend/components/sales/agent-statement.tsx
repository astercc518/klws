"use client";

import { useCallback, useEffect, useState } from "react";
import { Wallet, TrendingUp, HandCoins, Scale } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { MetricCard } from "@/components/metric-card";

/** Mirrors settlementJSON in internal/api/agent_api.go. */
interface Statement {
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

const usd = (smallest: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(smallest / 100);

function currentMonth(): string {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}`;
}

export function AgentStatement() {
  const [month, setMonth] = useState(currentMonth());
  const [data, setData] = useState<Statement | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const s = await api.get<Statement>(`/sales/statement?month=${month}`);
      setData(s);
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

  return (
    <div className="space-y-6">
      <Card className="flex flex-wrap items-center gap-3 p-4">
        <label htmlFor="statement-month" className="text-sm font-medium">
          结算月份
        </label>
        <Input
          id="statement-month"
          type="month"
          value={month}
          onChange={(e) => e.target.value && setMonth(e.target.value)}
          className="w-40 font-mono"
        />
        {loading && <span className="text-xs text-muted-foreground">加载中…</span>}
      </Card>

      {error && <Card className="p-5 text-sm text-muted-foreground">加载失败:{error}</Card>}

      {!error && !data && <div className="h-64 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />}

      {data && (
        <>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <MetricCard label="欠款(debt)" value={usd(data.debt)} sub="直属客户结算消费 · 按成本价" icon={Scale} />
            <MetricCard label="差价(margin)" value={usd(data.margin)} sub="直属零售 − 直属成本" icon={TrendingUp} />
            <MetricCard
              label="返佣(rebate)"
              value={usd(data.rebate)}
              sub={`整条下线零售 × ${(data.rebate_rate * 100).toFixed(2)}%`}
              icon={HandCoins}
            />
            <MetricCard
              hero
              label="净额(net)"
              value={usd(data.net)}
              sub={`margin + rebate − debt · ${data.period}`}
              icon={Wallet}
            />
          </div>

          <Card className="p-5">
            <div className="mb-3 text-sm font-medium">原始分量</div>
            <dl className="grid grid-cols-1 gap-x-8 gap-y-3 text-sm sm:grid-cols-2">
              <div className="flex items-center justify-between border-b pb-2">
                <dt className="text-muted-foreground">直属零售消费 (retail_direct)</dt>
                <dd className="font-mono tabular-nums">{usd(data.retail_direct)}</dd>
              </div>
              <div className="flex items-center justify-between border-b pb-2">
                <dt className="text-muted-foreground">直属成本消费 (cost_direct)</dt>
                <dd className="font-mono tabular-nums">{usd(data.cost_direct)}</dd>
              </div>
              <div className="flex items-center justify-between border-b pb-2">
                <dt className="text-muted-foreground">整条下线零售消费 (retail_subtree)</dt>
                <dd className="font-mono tabular-nums">{usd(data.retail_subtree)}</dd>
              </div>
              <div className="flex items-center justify-between border-b pb-2">
                <dt className="text-muted-foreground">返佣比例 (rebate_rate)</dt>
                <dd className="font-mono tabular-nums">{(data.rebate_rate * 100).toFixed(2)}%</dd>
              </div>
            </dl>
          </Card>
        </>
      )}
    </div>
  );
}
