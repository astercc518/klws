import { PageHeader } from "@/components/admin/page-header";
import { AdminAgentSettlements } from "@/components/admin-agent-settlements";

export default function AdminAgentSettlementsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Agent Distribution"
        title="结算总览"
        description="按月查看全平台代理的欠款/差价/返佣/净额,并可关账。关账幂等:重复关闭同一月份返回已持久化的原始数值。"
      />
      <AdminAgentSettlements />
    </div>
  );
}
