import { PageHeader } from "@/components/admin/page-header";
import { AdminTenantsTable } from "@/components/admin-tenants-table";

export default function AdminTenantsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Billing & IAM"
        title="租户与财务"
        description="所有客户与销售账号一览。对客户行可直接充值或配置发信单价。"
      />
      <AdminTenantsTable />
    </div>
  );
}
