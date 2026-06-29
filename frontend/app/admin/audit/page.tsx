import { PageHeader } from "@/components/admin/page-header";
import { AdminCampaigns } from "@/components/admin-campaigns";

export default function AdminAuditPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="God View · Risk"
        title="风控与任务监控"
        description="全平台所有租户的群发任务。可对正在发送的任务强制终止。"
      />
      <AdminCampaigns />
    </div>
  );
}
