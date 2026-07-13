import { PageHeader } from "@/components/admin/page-header";
import { AdminLedger } from "@/components/admin-ledger";

export default function AdminLedgerPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Billing & IAM"
        title={{ zh: "财务流水", en: "Ledger" }}
        description={{
          zh: "全平台钱包流水与退款申请。仅显示最近记录,用于对账与审计。",
          en: "Platform-wide wallet transactions and refund requests. Shows only recent records, for reconciliation and audit.",
        }}
      />
      <AdminLedger />
    </div>
  );
}
