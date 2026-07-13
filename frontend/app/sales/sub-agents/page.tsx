import { PageHeader } from "@/components/admin/page-header";
import { AgentSubAgentForm } from "@/components/sales/agent-sub-agent-form";

export default function SalesSubAgentsPage() {
  return (
    <div className="mx-auto max-w-7xl space-y-6">
      <PageHeader
        eyebrow="Sub-Agents"
        title={{ zh: "发展下级代理", en: "Grow sub-agents" }}
        description={{
          zh: "创建挂在你名下的下级代理账号，用于进一步分销与额度划拨。",
          en: "Create sub-agent accounts under you, for further distribution and credit allocation.",
        }}
        className="pb-0"
      />

      <AgentSubAgentForm />
    </div>
  );
}
