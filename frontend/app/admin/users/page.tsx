import { PageHeader } from "@/components/admin/page-header";
import { AdminUsersTable } from "@/components/admin-users-table";

export default function AdminUsersPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="IAM"
        title="用户管理"
        description="全站账号:新建、启用/禁用、重置密码。客户账号绑定到对应租户。"
      />
      <AdminUsersTable />
    </div>
  );
}
