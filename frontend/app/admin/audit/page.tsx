import { PageHeader } from "@/components/admin/page-header";
import { AdminAuditTabs } from "@/components/admin-audit-tabs";

export default function AdminAuditPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="God View · Risk"
        title="风控与审计"
        description="平台操作审计日志与全租户群发任务监控。"
      />
      <AdminAuditTabs />
    </div>
  );
}
