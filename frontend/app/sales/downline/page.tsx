import { PageHeader } from "@/components/admin/page-header";
import { AgentDownline } from "@/components/sales/agent-downline";

export default function SalesDownlinePage() {
  return (
    <div className="mx-auto max-w-7xl space-y-6">
      <PageHeader
        eyebrow="Downline & Credit"
        title={{ zh: "下线与额度", en: "Downline & credit" }}
        description={{
          zh: "信用额度总览、直属下级代理与直属客户汇总，支持批量额度划拨。下级代理名下的客户暂无独立查询接口，如需完整多级下线视图请联系管理员。",
          en: "Credit limit overview, direct sub-agents, and direct customers, with support for bulk credit transfers. Customers under a sub-agent don't yet have a dedicated lookup — contact an admin for the full multi-level downline view.",
        }}
        className="pb-0"
      />

      <AgentDownline />
    </div>
  );
}
