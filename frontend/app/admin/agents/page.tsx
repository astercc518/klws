import { PageHeader } from "@/components/admin/page-header";
import { AdminAgentTree } from "@/components/admin-agent-tree";

export default function AdminAgentsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Agent Distribution"
        title={{ zh: "代理树总览", en: "Agent tree overview" }}
        description={{
          zh: "全平台代理层级、信用额度与返佣比例。可改条款或调整上级归属。",
          en: "Platform-wide agent hierarchy, credit limits, and commission rates. Edit terms or reassign an agent's parent.",
        }}
      />
      <AdminAgentTree />
    </div>
  );
}
