import { PageHeader } from "@/components/admin/page-header";
import { AdminLedger } from "@/components/admin-ledger";

export default function AdminLedgerPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Billing & IAM"
        title="财务流水"
        description="全平台钱包流水与退款申请。仅显示最近记录,用于对账与审计。"
      />
      <AdminLedger />
    </div>
  );
}
