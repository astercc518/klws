import { PageHeader } from "@/components/admin/page-header";
import { AdminBilling } from "@/components/admin-billing";

export default function AdminBillingPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Billing & IAM"
        title={{ zh: "账单统计", en: "Billing overview" }}
        description={{
          zh: "平台资金大盘与租户账单。数据源为钱包流水,可导出对账。",
          en: "Platform-wide finance dashboard and tenant billing, sourced from wallet ledger entries — exportable for reconciliation.",
        }}
      />
      <AdminBilling />
    </div>
  );
}
