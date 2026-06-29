import { SalesConsole } from "@/components/sales-console";

export default function SalesPage() {
  return (
    <div className="mx-auto max-w-7xl space-y-6">
      <div>
        <div className="font-mono text-[10px] uppercase tracking-[0.18em] text-muted-foreground">
          My Book
        </div>
        <h1 className="mt-1 text-2xl font-semibold tracking-tight">我的客户</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          仅显示分配到你名下的客户。可查看余额并为其配置发信单价。
        </p>
      </div>

      <SalesConsole />
    </div>
  );
}
