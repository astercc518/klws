import { PageHeader } from "@/components/admin/page-header";
import { AdminAuditLog } from "@/components/admin-audit-log";

export default function AdminAuditPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="God View · Risk"
        title={{ zh: "风控与审计", en: "Risk & audit" }}
        description={{
          zh: "平台操作审计日志。群发任务监控已迁移至「发送任务」。",
          en: "Platform operation audit log. Campaign monitoring has moved to “Campaigns.”",
        }}
      />
      <AdminAuditLog />
    </div>
  );
}
