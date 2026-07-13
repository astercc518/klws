import { PageHeader } from "@/components/admin/page-header";
import { SalesConsole } from "@/components/sales-console";

export default function SalesPage() {
  return (
    <div className="mx-auto max-w-7xl space-y-6">
      <PageHeader
        eyebrow="My Book"
        title={{ zh: "我的客户", en: "My customers" }}
        description={{
          zh: "仅显示分配到你名下的客户。可查看余额并为其配置发信单价。",
          en: "Only customers assigned to you are shown. View their balance and configure their per-message rate.",
        }}
        className="pb-0"
      />

      <SalesConsole />
    </div>
  );
}
