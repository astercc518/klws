import { PageHeader } from "@/components/admin/page-header";
import { AdminRiskSettings } from "@/components/admin-risk-settings";

export default function AdminRiskSettingsPage() {
  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader
        eyebrow="God View · Risk"
        title="风控策略中心"
        description="集中管理全局防封策略:发信节奏、设备过载保护与封号率自动熔断阈值。"
      />
      <AdminRiskSettings />
    </div>
  );
}
