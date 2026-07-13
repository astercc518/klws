import { PageHeader } from "@/components/admin/page-header";
import { AdminProxies } from "@/components/admin-proxies";

export default function AdminResourcesPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Infrastructure"
        title={{ zh: "代理网络池", en: "Proxy network pool" }}
        description={{
          zh: "全平台出口代理及其健康状态。",
          en: "Platform-wide egress proxies and their health status.",
        }}
      />
      <AdminProxies />
    </div>
  );
}
