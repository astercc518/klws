import { PageHeader } from "@/components/admin/page-header";
import { AdminCommissions } from "@/components/admin-commissions";

export default function AdminCommissionsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Commission"
        title="销售佣金"
        description="每个销售名下租户当月消耗产生的应得佣金汇总。比例可在用户(销售)或租户编辑里配置。"
      />
      <AdminCommissions />
    </div>
  );
}
