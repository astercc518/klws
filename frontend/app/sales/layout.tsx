import { SalesSidebar } from "@/components/sales-sidebar";
import { SalesHeader } from "@/components/sales-header";

export default function SalesLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <div className="flex h-screen overflow-hidden">
      <SalesSidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <SalesHeader />
        <main className="flex-1 overflow-y-auto bg-muted/30 px-6 py-6 lg:px-8">{children}</main>
      </div>
    </div>
  );
}
