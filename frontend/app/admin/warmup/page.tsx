import { PageHeader } from "@/components/admin/page-header";
import { AdminWarmup } from "@/components/admin-warmup";

export default function AdminWarmupPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Resources"
        title={{ zh: "养号中心", en: "Warmup Center" }}
        description={{
          zh: "新号养号全生命周期:阶段/车道/号龄/健康度/养号进度、手动控制与车道策略配置。",
          en: "New-account warmup lifecycle: stage/lane/age/health/progress, manual controls, and lane policy config.",
        }}
      />
      <AdminWarmup />
    </div>
  );
}
