import { PageHeader } from "@/components/admin/page-header";
import { AdminAgentCostPricing } from "@/components/admin-agent-cost-pricing";

export default function AdminAgentCostPricingPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Agent Distribution"
        title="平台成本价"
        description="按国家设置平台每条发信成本价,用于代理差价与返佣计算。unit_cost 为必填,0 为合法值。"
      />
      <AdminAgentCostPricing />
    </div>
  );
}
