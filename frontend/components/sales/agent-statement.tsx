"use client";

import { useCallback, useEffect, useState } from "react";
import { Wallet, TrendingUp, HandCoins, Scale } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { MetricCard } from "@/components/metric-card";
import { useT } from "@/components/locale-provider";

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
  const t = useT();
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
        setError(e instanceof ApiError ? e.message : t("sales.statement.loadFailed"));
      }
    } finally {
      setLoading(false);
    }
  }, [month, t]);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <div className="space-y-6">
      <Card className="flex flex-wrap items-center gap-3 p-4">
        <label htmlFor="statement-month" className="text-sm font-medium">
          {t("sales.statement.monthLabel")}
        </label>
        <Input
          id="statement-month"
          type="month"
          value={month}
          onChange={(e) => e.target.value && setMonth(e.target.value)}
          className="w-40 font-mono"
        />
        {loading && <span className="text-xs text-muted-foreground">{t("common.loading")}</span>}
      </Card>

      {error && (
        <Card className="p-5 text-sm text-muted-foreground">
          {t("table.loadFailed")}
          {error}
        </Card>
      )}

      {!error && !data && <div className="h-64 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />}

      {data && (
        <>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <MetricCard
              label={t("sales.statement.debtLabel")}
              value={usd(data.debt)}
              sub={t("sales.statement.debtSub")}
              icon={Scale}
            />
            <MetricCard
              label={t("sales.statement.marginLabel")}
              value={usd(data.margin)}
              sub={t("sales.statement.marginSub")}
              icon={TrendingUp}
            />
            <MetricCard
              label={t("sales.statement.rebateLabel")}
              value={usd(data.rebate)}
              sub={t("sales.statement.rebateSub").replace("{rate}", () => (data.rebate_rate * 100).toFixed(2))}
              icon={HandCoins}
            />
            <MetricCard
              hero
              label={t("sales.statement.netLabel")}
              value={usd(data.net)}
              sub={`margin + rebate − debt · ${data.period}`}
              icon={Wallet}
            />
          </div>

          <Card className="p-5">
            <div className="mb-3 text-sm font-medium">{t("sales.statement.rawComponentsTitle")}</div>
            <dl className="grid grid-cols-1 gap-x-8 gap-y-3 text-sm sm:grid-cols-2">
              <div className="flex items-center justify-between border-b pb-2">
                <dt className="text-muted-foreground">{t("sales.statement.dt.retailDirect")}</dt>
                <dd className="font-mono tabular-nums">{usd(data.retail_direct)}</dd>
              </div>
              <div className="flex items-center justify-between border-b pb-2">
                <dt className="text-muted-foreground">{t("sales.statement.dt.costDirect")}</dt>
                <dd className="font-mono tabular-nums">{usd(data.cost_direct)}</dd>
              </div>
              <div className="flex items-center justify-between border-b pb-2">
                <dt className="text-muted-foreground">{t("sales.statement.dt.retailSubtree")}</dt>
                <dd className="font-mono tabular-nums">{usd(data.retail_subtree)}</dd>
              </div>
              <div className="flex items-center justify-between border-b pb-2">
                <dt className="text-muted-foreground">{t("sales.statement.dt.rebateRate")}</dt>
                <dd className="font-mono tabular-nums">{(data.rebate_rate * 100).toFixed(2)}%</dd>
              </div>
            </dl>
          </Card>
        </>
      )}
    </div>
  );
}
