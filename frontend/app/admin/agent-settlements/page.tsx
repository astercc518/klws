import { PageHeader } from "@/components/admin/page-header";
import { AdminAgentSettlements } from "@/components/admin-agent-settlements";

export default function AdminAgentSettlementsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Agent Distribution"
        title={{ zh: "结算总览", en: "Settlement overview" }}
        description={{
          zh: "按月查看全平台代理的欠款/差价/返佣/净额,并可关账。关账幂等:重复关闭同一月份返回已持久化的原始数值。",
          en: "View every agent's monthly debt / margin / commission / net across the platform, and close the books. Closing is idempotent — re-closing the same month returns the originally persisted figures.",
        }}
      />
      <AdminAgentSettlements />
    </div>
  );
}
