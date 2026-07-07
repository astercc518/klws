import {
  LayoutDashboard,
  UserCog,
  Building2,
  Globe,
  Smartphone,
  ScrollText,
  SlidersHorizontal,
  ReceiptText,
  Send,
  ListChecks,
  PieChart,
  type LucideIcon,
} from "lucide-react";

/** A single navigable destination in the admin console. */
export interface NavItem {
  href: string;
  /** Primary (Chinese) label shown in the sidebar and breadcrumb leaf. */
  label: string;
  /** Utility (English) label — set in mono caps as a secondary marker. */
  en: string;
  icon: LucideIcon;
}

/** Sidebar sections. Grouping mirrors how the platform is operated: who pays,
 *  what sends, and what gets watched — not the order routes were built. */
export interface NavGroup {
  /** Section heading shown above its items (hidden when collapsed). */
  title: string;
  items: NavItem[];
}

export const NAV_GROUPS: NavGroup[] = [
  {
    title: "概览",
    items: [{ href: "/admin", label: "全局大盘", en: "Overview", icon: LayoutDashboard }],
  },
  {
    title: "IAM 与账单",
    items: [
      { href: "/admin/tenants", label: "租户与财务", en: "Tenants", icon: Building2 },
      { href: "/admin/users", label: "用户管理", en: "Users", icon: UserCog },
      { href: "/admin/ledger", label: "财务流水", en: "Ledger", icon: ReceiptText },
      { href: "/admin/billing", label: "账单统计", en: "Billing", icon: PieChart },
    ],
  },
  {
    title: "资源大厅",
    items: [
      { href: "/admin/resources", label: "代理网络池", en: "Proxies", icon: Globe },
      { href: "/admin/devices", label: "节点与设备", en: "Devices", icon: Smartphone },
    ],
  },
  {
    title: "发送中心",
    items: [
      { href: "/admin/campaigns", label: "发送任务", en: "Campaigns", icon: Send },
      { href: "/admin/send-records", label: "发送记录", en: "Send Records", icon: ListChecks },
    ],
  },
  {
    title: "策略中心",
    items: [
      { href: "/admin/settings/risk", label: "风控策略", en: "Risk", icon: SlidersHorizontal },
    ],
  },
  {
    title: "系统审计",
    items: [{ href: "/admin/audit", label: "风控与审计", en: "Audit", icon: ScrollText }],
  },
];

/** Flat list of every item, for breadcrumb / active-route resolution. */
export const NAV_ITEMS: NavItem[] = NAV_GROUPS.flatMap((g) => g.items);

/** Returns the group + item that own a pathname. `/admin` matches exactly;
 *  deeper routes match by longest href prefix so nested pages still resolve. */
export function resolveRoute(pathname: string): { group: NavGroup; item: NavItem } | null {
  for (const group of NAV_GROUPS) {
    for (const item of group.items) {
      const match = item.href === "/admin" ? pathname === "/admin" : pathname.startsWith(item.href);
      if (match) return { group, item };
    }
  }
  return null;
}
