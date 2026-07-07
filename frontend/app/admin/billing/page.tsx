import { PageHeader } from "@/components/admin/page-header";
import { AdminBilling } from "@/components/admin-billing";

export default function AdminBillingPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Billing & IAM"
        title="账单统计"
        description="平台资金大盘与租户账单。数据源为钱包流水,可导出对账。"
      />
      <AdminBilling />
    </div>
  );
}
