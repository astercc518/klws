import { PageHeader } from "@/components/admin/page-header";
import { AdminDevices } from "@/components/admin-devices";

export default function AdminDevicesPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Infrastructure"
        title="节点与发信设备"
        description="全网 WhatsApp 账号状态:在线、离线、被风控封禁。"
      />
      <AdminDevices />
    </div>
  );
}
