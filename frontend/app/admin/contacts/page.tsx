import { PageHeader } from "@/components/admin/page-header";
import { AdminContacts } from "@/components/admin-contacts";

export default function AdminContactsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Send Center"
        title={{ zh: "联系人", en: "Contacts" }}
        description={{
          zh: "全平台联系人库(跨租户只读):手机号、所属租户、国家/地区与状态。可按租户、状态筛选,按手机号搜索。管理员在此仅可查看,不可编辑客户的联系人。",
          en: "Platform-wide contact directory (cross-tenant, read-only): phone number, owning tenant, country/region, and status. Filter by tenant or status, search by phone number. Admins can only view here — tenant contacts cannot be edited.",
        }}
      />
      <AdminContacts />
    </div>
  );
}
