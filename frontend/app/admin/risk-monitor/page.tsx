import { PageHeader } from "@/components/admin/page-header";
import { AdminRiskMonitor } from "@/components/admin-risk-monitor";

export default function AdminRiskMonitorPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="God View · Risk"
        title={{ zh: "风控监控", en: "Risk monitor" }}
        description={{
          zh: "实时风控看板:封号率、隔离账号、健康分分布、发送到达情况,以及命中风控规则的异常账号明细。",
          en: "Real-time risk dashboard: ban rate, quarantined accounts, health-score distribution, delivery throughput, and the anomalous accounts that tripped a risk rule.",
        }}
      />
      <AdminRiskMonitor />
    </div>
  );
}
