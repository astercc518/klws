import { PageHeader } from "@/components/admin/page-header";
import { AdminMetrics } from "@/components/admin-metrics";

export default function AdminOverviewPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="God View"
        title={{ zh: "全局大盘", en: "Global overview" }}
        description={{
          zh: "全平台财务、资源与风控的实时概览。",
          en: "Real-time overview of platform-wide finance, resources, and risk.",
        }}
      />
      <AdminMetrics />
    </div>
  );
}
