import { PageHeader } from "@/components/admin/page-header";
import { AdminSetupWizard } from "@/components/admin-setup-wizard";

export default function AdminSetupPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Setup"
        title="配置流程"
        description="从资费套餐到发送路由的开通引导，六步完成一个客户的接入。"
      />
      <AdminSetupWizard />
    </div>
  );
}
