import { Card } from "@/components/ui/card";
import { PageHeader } from "@/components/admin/page-header";
import { DashboardMetrics } from "@/components/dashboard-metrics";
import { SendTrendChart } from "@/components/send-trend-chart";
import { NewCampaignDialog } from "@/components/new-campaign-dialog";
import { pick } from "@/lib/i18n";
import { getLocale } from "@/lib/server-locale";

export default async function DashboardPage() {
  const locale = await getLocale();

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
            <h2 className="text-sm font-semibold tracking-tight">
              {pick({ zh: "发送趋势", en: "Send trend" }, locale)}
            </h2>
            <p className="font-mono text-[10px] uppercase tracking-[0.15em] text-muted-foreground">
              Last 7 days · success vs failed · {pick({ zh: "示例数据", en: "sample data" }, locale)}
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
