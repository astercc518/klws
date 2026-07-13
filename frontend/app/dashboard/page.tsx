import { Card } from "@/components/ui/card";
import { PageHeader } from "@/components/admin/page-header";
import { DashboardMetrics } from "@/components/dashboard-metrics";
import { SendTrendChart } from "@/components/send-trend-chart";
import { NewCampaignDialog } from "@/components/new-campaign-dialog";

export default function DashboardPage() {
  return (
    <div className="mx-auto max-w-7xl space-y-6">
      <PageHeader
        eyebrow="Overview"
        title={{ zh: "数据大盘", en: "Dashboard" }}
        actions={<NewCampaignDialog />}
        className="pb-0"
      />

      {/* Metric row — live data from GET /tenant/stats */}
      <DashboardMetrics />

      {/* Trend chart */}
      <Card className="p-6">
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-sm font-semibold tracking-tight">发送趋势</h2>
            <p className="font-mono text-[10px] uppercase tracking-[0.15em] text-muted-foreground">
              Last 7 days · success vs failed · 示例数据
            </p>
          </div>
        </div>
        <div className="mt-6">
          <SendTrendChart />
        </div>
      </Card>
    </div>
  );
}
