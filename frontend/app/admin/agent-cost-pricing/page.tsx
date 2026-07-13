import { PageHeader } from "@/components/admin/page-header";
import { AdminAgentCostPricing } from "@/components/admin-agent-cost-pricing";

export default function AdminAgentCostPricingPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Agent Distribution"
        title={{ zh: "平台成本价", en: "Platform cost pricing" }}
        description={{
          zh: "按国家设置平台每条发信成本价,用于代理差价与返佣计算。unit_cost 为必填,0 为合法值。",
          en: "Set the platform's per-message cost by country, used to calculate agent margin and commission. unit_cost is required; 0 is a valid value.",
        }}
      />
      <AdminAgentCostPricing />
    </div>
  );
}
