import { PageHeader } from "@/components/admin/page-header";
import { AdminAgentTree } from "@/components/admin-agent-tree";

export default function AdminAgentsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Agent Distribution"
        title="代理树总览"
        description="全平台代理层级、信用额度与返佣比例。可改条款或调整上级归属。"
      />
      <AdminAgentTree />
    </div>
  );
}
