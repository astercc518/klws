import { PageHeader } from "@/components/admin/page-header";
import { AdminReports } from "@/components/admin-reports";

export default function AdminReportsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="God View · Reports"
        title={{ zh: "报表中心", en: "Reports" }}
        description={{
          zh: "指标历史趋势(读取快照存储)与租户消耗排行,支持按指标/粒度/时间范围筛选并导出 CSV。",
          en: "Historical metric trends (read from snapshot storage) and tenant consumption ranking — filter by metric, granularity, and time range, and export to CSV.",
        }}
      />
      <AdminReports />
    </div>
  );
}
