import { PageHeader } from "@/components/admin/page-header";
import { AdminRiskSettings } from "@/components/admin-risk-settings";

export default function AdminRiskSettingsPage() {
  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader
        eyebrow="God View · Risk"
        title={{ zh: "风控策略中心", en: "Risk policy center" }}
        description={{
          zh: "集中管理全局防封策略:发信节奏、设备过载保护与封号率自动熔断阈值。",
          en: "Centrally manage global anti-ban policy: send pacing, device overload protection, and the ban-rate auto-circuit-breaker threshold.",
        }}
      />
      <AdminRiskSettings />
    </div>
  );
}
