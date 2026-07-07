import { PageHeader } from "@/components/admin/page-header";
import { AdminAuditLog } from "@/components/admin-audit-log";

export default function AdminAuditPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="God View · Risk"
        title="风控与审计"
        description="平台操作审计日志。群发任务监控已迁移至「发送任务」。"
      />
      <AdminAuditLog />
    </div>
  );
}
