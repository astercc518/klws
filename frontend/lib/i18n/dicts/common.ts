import type { Locale } from "@/lib/i18n";

/** Chrome strings shared across the logged-in shell (header, account menu,
 *  toggles, sidebar affordances). Keys are dot-namespaced. */
export const common: Record<Locale, Record<string, string>> = {
  zh: {
    "header.brand": "管理后台",
    "header.searchPlaceholder": "搜索客户、设备、代理…",
    "header.searchAria": "全局搜索",
    "header.notifications": "通知",
    "header.accountMenu": "账户菜单",
    "header.breadcrumb": "面包屑",
    "menu.changePassword": "修改密码",
    "menu.logout": "退出登录",
    "toggle.theme": "切换主题",
    "toggle.language": "切换语言",
    "sidebar.expand": "展开侧边栏",
    "sidebar.collapse": "折叠侧边栏",
  },
  en: {
    "header.brand": "Admin",
    "header.searchPlaceholder": "Search customers, devices, proxies…",
    "header.searchAria": "Global search",
    "header.notifications": "Notifications",
    "header.accountMenu": "Account menu",
    "header.breadcrumb": "Breadcrumb",
    "menu.changePassword": "Change password",
    "menu.logout": "Log out",
    "toggle.theme": "Toggle theme",
    "toggle.language": "Switch language",
    "sidebar.expand": "Expand sidebar",
    "sidebar.collapse": "Collapse sidebar",
  },
};
