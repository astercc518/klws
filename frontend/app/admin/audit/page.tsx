import { PageHeader } from "@/components/admin/page-header";
import { AdminCampaigns } from "@/components/admin-campaigns";

export default function AdminAuditPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="God View · Risk"
        title="风控与任务监控"
        description="全平台所有租户的群发任务。可强制终止运行中的任务,并恢复被风控自动熔断的任务。"
      />
      <AdminCampaigns />
    </div>
  );
}
