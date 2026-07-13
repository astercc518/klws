import { PageHeader } from "@/components/admin/page-header";
import { AdminDevices } from "@/components/admin-devices";

export default function AdminDevicesPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Infrastructure"
        title={{ zh: "节点与发信设备", en: "Nodes & sending devices" }}
        description={{
          zh: "全网 WhatsApp 账号状态:在线、离线、被风控封禁。",
          en: "WhatsApp account status platform-wide: online, offline, or banned by risk controls.",
        }}
      />
      <AdminDevices />
    </div>
  );
}
