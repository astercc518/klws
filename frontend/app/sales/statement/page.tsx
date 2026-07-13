import { AgentStatement } from "@/components/sales/agent-statement";

export default function SalesStatementPage() {
  return (
    <div className="mx-auto max-w-7xl space-y-6">
      <div>
        <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
          Monthly Statement
        </div>
        <h1 className="mt-1 text-2xl font-semibold tracking-tight">月结单</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          按月查看欠款、差价、返佣与净额，可切换月份查看历史结算。
        </p>
      </div>

      <AgentStatement />
    </div>
  );
}
