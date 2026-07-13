import { PageHeader } from "@/components/admin/page-header";
import { AdminSendRecords } from "@/components/admin-send-records";

export default function AdminSendRecordsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Send Center"
        title={{ zh: "发送记录", en: "Send records" }}
        description={{
          zh: "全平台逐条发送明细:手机号、所属任务/租户、发送状态与失败原因。可按状态、手机号筛选。",
          en: "Platform-wide per-message send detail: phone number, owning campaign/tenant, send status, and failure reason. Filter by status or phone number.",
        }}
      />
      <AdminSendRecords />
    </div>
  );
}
