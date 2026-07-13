import { PageHeader } from "@/components/admin/page-header";
import { AgentStatement } from "@/components/sales/agent-statement";

export default function SalesStatementPage() {
  return (
    <div className="mx-auto max-w-7xl space-y-6">
      <PageHeader
        eyebrow="Monthly Statement"
        title={{ zh: "月结单", en: "Monthly statement" }}
        description={{
          zh: "按月查看欠款、差价、返佣与净额，可切换月份查看历史结算。",
          en: "View debt, margin, commission, and net by month — switch months to see historical settlements.",
        }}
        className="pb-0"
      />

      <AgentStatement />
    </div>
  );
}
