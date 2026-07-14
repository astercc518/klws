import { PageHeader } from "@/components/admin/page-header";
import { AdminInstances } from "@/components/admin-instances";

export default function AdminInstancesPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Resources"
        title={{ zh: "实例", en: "Instances" }}
        description={{
          zh: "全平台 Evolution 实例路由表:号码配对状态、所属节点与代理、批量登出/删除,以及各节点容量。",
          en: "Platform-wide Evolution instance routing table: pairing status, owning node and proxy, bulk logout/delete, and per-node capacity.",
        }}
      />
      <AdminInstances />
    </div>
  );
}
