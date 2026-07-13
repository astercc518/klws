import { AgentSubAgentForm } from "@/components/sales/agent-sub-agent-form";

export default function SalesSubAgentsPage() {
  return (
    <div className="mx-auto max-w-7xl space-y-6">
      <div>
        <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
          Sub-Agents
        </div>
        <h1 className="mt-1 text-2xl font-semibold tracking-tight">发展下级代理</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          创建挂在你名下的下级代理账号，用于进一步分销与额度划拨。
        </p>
      </div>

      <AgentSubAgentForm />
    </div>
  );
}
