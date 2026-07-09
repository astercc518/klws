import {
  LayoutDashboard,
  Users,
  Globe,
  Smartphone,
  ScrollText,
  SlidersHorizontal,
  ReceiptText,
  Send,
  ListChecks,
  PieChart,
  BookUser,
  Network,
  Coins,
  CalendarCheck,
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
  /** English section heading, used when locale is "en". */
  en: string;
  items: NavItem[];
}

export const NAV_GROUPS: NavGroup[] = [
  {
    title: "概览",
    en: "Overview",
    items: [{ href: "/admin", label: "运行信息", en: "Overview", icon: LayoutDashboard }],
  },
  {
    title: "客户与财务",
    en: "Customers & Finance",
    items: [
      { href: "/admin/users", label: "客户", en: "Customers", icon: Users },
      { href: "/admin/ledger", label: "财务流水", en: "Ledger", icon: ReceiptText },
      { href: "/admin/billing", label: "账单统计", en: "Billing", icon: PieChart },
    ],
  },
  {
    title: "资源",
    en: "Resources",
    items: [
      { href: "/admin/resources", label: "代理网络池", en: "Proxies", icon: Globe },
      { href: "/admin/devices", label: "节点与设备", en: "Devices", icon: Smartphone },
    ],
  },
  {
    title: "发送中心",
    en: "Sending",
    items: [
      { href: "/admin/campaigns", label: "发送任务", en: "Campaigns", icon: Send },
      { href: "/admin/send-records", label: "发送记录", en: "Send Records", icon: ListChecks },
      { href: "/admin/contacts", label: "联系人", en: "Contacts", icon: BookUser },
    ],
  },
  {
    title: "代理分销",
    items: [
      // Flat sibling routes (not nested under /admin/agents/*): both this
      // file's resolveRoute (first-match wins) and admin-sidebar's per-item
      // `pathname.startsWith(item.href)` active-highlight would otherwise
      // treat "/admin/agents/cost-pricing" as also matching "/admin/agents",
      // double-highlighting two nav items at once.
      { href: "/admin/agents", label: "代理树", en: "Agent Tree", icon: Network },
      { href: "/admin/agent-cost-pricing", label: "成本价", en: "Cost Pricing", icon: Coins },
      { href: "/admin/agent-settlements", label: "结算总览", en: "Settlements", icon: CalendarCheck },
    ],
  },
  {
    title: "策略中心",
    en: "Policy",
    items: [
      { href: "/admin/settings/risk", label: "风控策略", en: "Risk", icon: SlidersHorizontal },
    ],
  },
  {
    title: "系统审计",
    en: "Audit",
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
