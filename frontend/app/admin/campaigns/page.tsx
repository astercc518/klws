import { PageHeader } from "@/components/admin/page-header";
import { AdminCampaigns } from "@/components/admin-campaigns";

export default function AdminCampaignsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Send Center"
        title="发送任务"
        description="全租户群发任务监控:筛选、强制暂停/恢复、下钻收件人明细。"
      />
      <AdminCampaigns />
    </div>
  );
}
