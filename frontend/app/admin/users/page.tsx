import { PageHeader } from "@/components/admin/page-header";
import { AdminUsersTable } from "@/components/admin-users-table";

export default function AdminUsersPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="IAM"
        title={{ zh: "用户管理", en: "User management" }}
        description={{
          zh: "全站账号:新建、启用/禁用、重置密码。",
          en: "All accounts site-wide: create, enable/disable, reset password.",
        }}
      />
      <AdminUsersTable />
    </div>
  );
}
