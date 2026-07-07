import { PageHeader } from "@/components/admin/page-header";
import { AdminIamTabs } from "@/components/admin-iam-tabs";

export default function AdminUsersPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="IAM"
        title="用户与租户"
        description="全站账号(管理员/销售/客户)与租户财务集中管理:新建/编辑/启用禁用/充值/定价。"
      />
      <AdminIamTabs />
    </div>
  );
}
