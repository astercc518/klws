import { PageHeader } from "@/components/admin/page-header";
import { AdminProxies } from "@/components/admin-proxies";

export default function AdminResourcesPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Infrastructure"
        title="代理网络池"
        description="全平台出口代理及其健康状态。"
      />
      <AdminProxies />
    </div>
  );
}
