import { AgentDownline } from "@/components/sales/agent-downline";

export default function SalesDownlinePage() {
  return (
    <div className="mx-auto max-w-7xl space-y-6">
      <div>
        <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
          Downline & Credit
        </div>
        <h1 className="mt-1 text-2xl font-semibold tracking-tight">下线与额度</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          信用额度总览、直属下级代理与直属客户汇总，支持批量额度划拨。下级代理名下的客户暂无独立查询接口，如需完整多级下线视图请联系管理员。
        </p>
      </div>

      <AgentDownline />
    </div>
  );
}
