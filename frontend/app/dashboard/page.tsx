import { Card } from "@/components/ui/card";
import { DashboardMetrics } from "@/components/dashboard-metrics";
import { SendTrendChart } from "@/components/send-trend-chart";
import { NewCampaignDialog } from "@/components/new-campaign-dialog";

export default function DashboardPage() {
  return (
    <div className="mx-auto max-w-7xl space-y-6">
      {/* Page heading + primary action */}
      <div className="flex items-end justify-between gap-4">
        <div>
          <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
            Overview
          </div>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight">数据大盘</h1>
        </div>
        <NewCampaignDialog />
      </div>

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
