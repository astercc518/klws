import type { Locale } from "@/lib/i18n";

/** Admin console copy (pages under app/admin/**). Skeleton for now — filled
 *  incrementally as pages migrate off hardcoded Chinese strings. */
export const admin: Record<Locale, Record<string, string>> = {
  zh: {
    // components/admin-devices.tsx — status → tone label
    "admin.devices.status.banned": "封禁",
    "admin.devices.status.flagged": "标记",
    "admin.devices.status.online": "在线",
    "admin.devices.status.loggedOut": "已登出",
    "admin.devices.status.offline": "离线",

    // admin-devices.tsx — table columns
    "admin.devices.col.jid": "账号 JID",
    "admin.devices.col.phone": "手机号",
    "admin.devices.col.tenant": "租户",
    "admin.devices.col.tags": "标签",
    "admin.devices.col.network": "网络环境",
    "admin.devices.col.status": "状态",
    "admin.devices.col.node": "所属节点",
    "admin.devices.notBound": "未绑定",

    // admin-devices.tsx — stat cards
    "admin.devices.stat.totalLabel": "设备总数",
    "admin.devices.stat.totalSub": "全网 WA 账号",
    "admin.devices.stat.onlineSub": "已接管并可发送",
    "admin.devices.stat.riskyLabel": "封禁 / 标记",
    "admin.devices.stat.riskySub": "被风控命中",
    "admin.devices.stat.loggedOutSub": "需重新扫码接入",

    // admin-devices.tsx — table search/empty/filters
    "admin.devices.searchPlaceholder": "搜索 JID / 手机号 / 标签…",
    "admin.devices.emptyState": "设备池为空。可批量录入元数据,或在节点侧扫码接入。",
    "admin.devices.filterByStatusAria": "按状态筛选",
    "admin.devices.filterByOnlineAria": "按在线状态筛选",
    "admin.devices.filter.allStatuses": "全部状态",
    "admin.devices.filter.active": "活跃",
    "admin.devices.filter.all": "全部",

    // admin-devices.tsx — row actions
    "admin.devices.actionsAria": "操作",
    "admin.devices.action.configureNetwork": "配置网络",
    "admin.devices.action.edit": "编辑",
    "admin.devices.action.delete": "删除",

    // admin-devices.tsx — ConnectDeviceDialog
    "admin.devices.connect.trigger": "接入新账号",
    "admin.devices.connect.title": "扫码接入 WhatsApp 账号",
    "admin.devices.connect.desc": "用 WhatsApp → 已关联的设备 → 关联设备,扫描下方二维码。",
    "admin.devices.connect.note":
      "实时二维码由发信节点(cmd/wadist)的 whatsmeow 配对流程产生。该集群流式端点尚未对外开放,此处为预留位 —— 不触碰底层防封/集群逻辑。",
    "admin.devices.close": "关闭",

    // admin-devices.tsx — ImportDevicesDialog
    "admin.devices.import.trigger": "批量录入设备",
    "admin.devices.import.title": "批量录入设备元数据",
    "admin.devices.import.descPrefix": "每行一个,格式",
    "admin.devices.import.descSuffix":
      "。标签段可省略,多个标签用逗号分隔。仅写入元数据,实际登录仍走节点扫码。",
    "admin.devices.import.listLabel": "设备列表",
    "admin.devices.import.parsedCount": "解析到 {n} 条",
    "admin.devices.cancel": "取消",
    "admin.devices.import.importing": "录入中…",
    "admin.devices.import.confirm": "确认录入 {n} 条",
    "admin.devices.import.successTitle": "设备录入完成",
    "admin.devices.import.successDesc": "新增 {added} · 跳过 {skipped}",
    "admin.devices.import.failedTitle": "录入失败",
    "admin.devices.retry": "请重试",
    "admin.devices.loadFailed": "加载失败",

    // admin-devices.tsx — ProxyDialog
    "admin.devices.proxyDialog.title": "配置网络环境",
    "admin.devices.proxyDialog.descPrefix": "为 ",
    "admin.devices.proxyDialog.descSuffix": " 绑定独享静态代理 IP,实现账号间网络隔离、防止关联封号。",
    "admin.devices.proxyDialog.currentBinding": "当前绑定",
    "admin.devices.proxyDialog.selectLabel": "选择代理 IP",
    "admin.devices.proxyDialog.selectPlaceholder": "选择一个可用代理…",
    "admin.devices.proxyDialog.noneAvailable": "无可用代理",
    "admin.devices.proxyDialog.unbind": "解绑",
    "admin.devices.proxyDialog.submitting": "提交中…",
    "admin.devices.proxyDialog.confirmBind": "确认绑定",
    "admin.devices.proxyDialog.boundTitle": "代理已绑定",
    "admin.devices.proxyDialog.bindFailedTitle": "绑定失败",
    "admin.devices.proxyDialog.unboundTitle": "已解绑代理",
    "admin.devices.proxyDialog.unbindFailedTitle": "解绑失败",

    // admin-devices.tsx — EditDeviceDialog
    "admin.devices.edit.title": "编辑设备",
    "admin.devices.edit.descPrefix": "修改 ",
    "admin.devices.edit.descSuffix": " 的归属租户、手机号与标签。",
    "admin.devices.edit.tenantIdLabel": "租户 ID",
    "admin.devices.edit.saving": "保存中…",
    "admin.devices.edit.confirmSave": "确认保存",
    "admin.devices.edit.successTitle": "设备已更新",
    "admin.devices.edit.failedTitle": "更新失败",

    // admin-devices.tsx — DeleteDeviceDialog
    "admin.devices.delete.title": "删除设备",
    "admin.devices.delete.descPrefix": "确认删除设备 ",
    "admin.devices.delete.descSuffix": "?此操作不可撤销,若已绑定代理会自动释放。",
    "admin.devices.delete.deleting": "删除中…",
    "admin.devices.delete.confirm": "确认删除",
    "admin.devices.delete.successTitle": "设备已删除",
    "admin.devices.delete.failedTitle": "删除失败",

    // components/admin-proxies.tsx — table columns
    "admin.proxies.col.address": "代理地址",
    "admin.proxies.col.type": "类型",
    "admin.proxies.col.country": "国家",
    "admin.proxies.col.status": "状态",
    "admin.proxies.col.bindings": "绑定",
    "admin.proxies.col.failures": "失败",
    "admin.proxies.status.online": "在线",
    "admin.proxies.status.invalid": "失效",

    // admin-proxies.tsx — stat cards
    "admin.proxies.stat.totalLabel": "代理总数",
    "admin.proxies.stat.totalSub": "全平台出口",
    "admin.proxies.stat.onlineSub": "健康可调度",
    "admin.proxies.stat.offlineLabel": "离线 / 失效",
    "admin.proxies.stat.offlineSub": "需排查或剔除",
    "admin.proxies.stat.currentPageLabel": "当前页",
    "admin.proxies.stat.currentPageSub": "匹配筛选的总数",

    // admin-proxies.tsx — table search/empty/filters
    "admin.proxies.searchPlaceholder": "搜索地址或国家…",
    "admin.proxies.emptyState": "代理池为空,点击右上角批量导入。",
    "admin.proxies.filterByStatusAria": "按状态筛选",
    "admin.proxies.filter.allStatuses": "全部状态",

    // admin-proxies.tsx — row actions
    "admin.proxies.actionsAria": "操作",
    "admin.proxies.action.edit": "编辑",
    "admin.proxies.action.delete": "删除",

    // admin-proxies.tsx — ImportProxiesDialog
    "admin.proxies.import.title": "批量导入代理",
    "admin.proxies.import.descPrefix": "每行一个,格式 ",
    "admin.proxies.import.descSuffix": "(无认证可省略后两段)。",
    "admin.proxies.typeLabel": "类型",
    "admin.proxies.countryIsoLabel": "国家 ISO-2",
    "admin.proxies.listLabel": "代理列表",
    "admin.proxies.parsedCount": "解析到 {n} 条",
    "admin.proxies.cancel": "取消",
    "admin.proxies.import.importing": "导入中…",
    "admin.proxies.import.confirm": "确认导入 {n} 条",
    "admin.proxies.import.successTitle": "代理导入完成",
    "admin.proxies.import.successDesc": "新增 {added} · 跳过 {skipped}",
    "admin.proxies.import.failedTitle": "导入失败",
    "admin.proxies.retry": "请重试",
    "admin.proxies.loadFailed": "加载失败",

    // admin-proxies.tsx — EditProxyDialog
    "admin.proxies.edit.title": "编辑代理",
    "admin.proxies.edit.descPrefix": "修改 ",
    "admin.proxies.edit.descSuffix": " 的类型、国家与最大绑定数。",
    "admin.proxies.maxBindingsLabel": "最大绑定数",
    "admin.proxies.edit.saving": "保存中…",
    "admin.proxies.edit.confirmSave": "确认保存",
    "admin.proxies.edit.successTitle": "代理已更新",
    "admin.proxies.edit.failedTitle": "更新失败",

    // admin-proxies.tsx — DeleteProxyDialog
    "admin.proxies.delete.title": "删除代理",
    "admin.proxies.delete.descPrefix": "确认删除代理 ",
    "admin.proxies.delete.descSuffix": "?此操作不可撤销,已绑定的设备将自动解绑。",
    "admin.proxies.delete.deleting": "删除中…",
    "admin.proxies.delete.confirm": "确认删除",
    "admin.proxies.delete.successTitle": "代理已删除",
    "admin.proxies.delete.failedTitle": "删除失败",

    // components/admin-metrics.tsx
    "admin.metrics.loadFailedGeneric": "加载失败",
    "admin.metrics.overviewLoadFailedPrefix": "大盘加载失败:",
    "admin.metrics.totalConsumedRevenue": "平台总消耗 / 收入",
    "admin.metrics.cumulativeTopupPrefix": "累计充值 ",
    "admin.metrics.activeWaAccounts": "活跃 WA 账号",
    "admin.metrics.bannedFlaggedPrefix": "封禁/标记 ",
    "admin.metrics.queueBacklog": "队列积压任务",
    "admin.metrics.pendingRecipients": "待发送收件人",
    "admin.metrics.banRate": "风控封号率",
    "admin.metrics.bannedFlaggedOverTotal": "banned+flagged / 总账号",
  },
  en: {
    // components/admin-devices.tsx — status → tone label
    "admin.devices.status.banned": "Banned",
    "admin.devices.status.flagged": "Flagged",
    "admin.devices.status.online": "Online",
    "admin.devices.status.loggedOut": "Logged out",
    "admin.devices.status.offline": "Offline",

    // admin-devices.tsx — table columns
    "admin.devices.col.jid": "Account JID",
    "admin.devices.col.phone": "Phone number",
    "admin.devices.col.tenant": "Tenant",
    "admin.devices.col.tags": "Tags",
    "admin.devices.col.network": "Network environment",
    "admin.devices.col.status": "Status",
    "admin.devices.col.node": "Owner node",
    "admin.devices.notBound": "Not bound",

    // admin-devices.tsx — stat cards
    "admin.devices.stat.totalLabel": "Total devices",
    "admin.devices.stat.totalSub": "All WA accounts network-wide",
    "admin.devices.stat.onlineSub": "Claimed and ready to send",
    "admin.devices.stat.riskyLabel": "Banned / flagged",
    "admin.devices.stat.riskySub": "Flagged by risk controls",
    "admin.devices.stat.loggedOutSub": "Needs QR re-pairing",

    // admin-devices.tsx — table search/empty/filters
    "admin.devices.searchPlaceholder": "Search JID / phone / tags…",
    "admin.devices.emptyState": "Device pool is empty. Bulk-import metadata, or pair via QR code on the node.",
    "admin.devices.filterByStatusAria": "Filter by status",
    "admin.devices.filterByOnlineAria": "Filter by online status",
    "admin.devices.filter.allStatuses": "All statuses",
    "admin.devices.filter.active": "Active",
    "admin.devices.filter.all": "All",

    // admin-devices.tsx — row actions
    "admin.devices.actionsAria": "Actions",
    "admin.devices.action.configureNetwork": "Configure network",
    "admin.devices.action.edit": "Edit",
    "admin.devices.action.delete": "Delete",

    // admin-devices.tsx — ConnectDeviceDialog
    "admin.devices.connect.trigger": "Connect new account",
    "admin.devices.connect.title": "Pair a WhatsApp account via QR",
    "admin.devices.connect.desc": "In WhatsApp, go to Linked devices → Link a device, then scan the QR code below.",
    "admin.devices.connect.note":
      "The live QR code is generated by the sending node's (cmd/wadist) whatsmeow pairing flow. That cluster streaming endpoint isn't exposed yet — this is a placeholder and doesn't touch the underlying anti-ban/cluster logic.",
    "admin.devices.close": "Close",

    // admin-devices.tsx — ImportDevicesDialog
    "admin.devices.import.trigger": "Bulk import devices",
    "admin.devices.import.title": "Bulk import device metadata",
    "admin.devices.import.descPrefix": "One entry per line, format",
    "admin.devices.import.descSuffix":
      ". The tag segment is optional — separate multiple tags with commas. This only writes metadata; actual login still happens via QR scan on the node.",
    "admin.devices.import.listLabel": "Device list",
    "admin.devices.import.parsedCount": "Parsed {n} entries",
    "admin.devices.cancel": "Cancel",
    "admin.devices.import.importing": "Importing…",
    "admin.devices.import.confirm": "Import {n} entries",
    "admin.devices.import.successTitle": "Devices imported",
    "admin.devices.import.successDesc": "Added {added} · Skipped {skipped}",
    "admin.devices.import.failedTitle": "Import failed",
    "admin.devices.retry": "Please try again",
    "admin.devices.loadFailed": "Load failed",

    // admin-devices.tsx — ProxyDialog
    "admin.devices.proxyDialog.title": "Configure network environment",
    "admin.devices.proxyDialog.descPrefix": "Bind a dedicated static proxy IP for ",
    "admin.devices.proxyDialog.descSuffix": " to isolate network activity between accounts and prevent ban-by-association.",
    "admin.devices.proxyDialog.currentBinding": "Current binding",
    "admin.devices.proxyDialog.selectLabel": "Select proxy IP",
    "admin.devices.proxyDialog.selectPlaceholder": "Select an available proxy…",
    "admin.devices.proxyDialog.noneAvailable": "No available proxy",
    "admin.devices.proxyDialog.unbind": "Unbind",
    "admin.devices.proxyDialog.submitting": "Submitting…",
    "admin.devices.proxyDialog.confirmBind": "Confirm bind",
    "admin.devices.proxyDialog.boundTitle": "Proxy bound",
    "admin.devices.proxyDialog.bindFailedTitle": "Bind failed",
    "admin.devices.proxyDialog.unboundTitle": "Proxy unbound",
    "admin.devices.proxyDialog.unbindFailedTitle": "Unbind failed",

    // admin-devices.tsx — EditDeviceDialog
    "admin.devices.edit.title": "Edit device",
    "admin.devices.edit.descPrefix": "Edit the tenant, phone number, and tags for ",
    "admin.devices.edit.descSuffix": ".",
    "admin.devices.edit.tenantIdLabel": "Tenant ID",
    "admin.devices.edit.saving": "Saving…",
    "admin.devices.edit.confirmSave": "Confirm save",
    "admin.devices.edit.successTitle": "Device updated",
    "admin.devices.edit.failedTitle": "Update failed",

    // admin-devices.tsx — DeleteDeviceDialog
    "admin.devices.delete.title": "Delete device",
    "admin.devices.delete.descPrefix": "Confirm deleting device ",
    "admin.devices.delete.descSuffix": "? This cannot be undone — any bound proxy will be released automatically.",
    "admin.devices.delete.deleting": "Deleting…",
    "admin.devices.delete.confirm": "Confirm delete",
    "admin.devices.delete.successTitle": "Device deleted",
    "admin.devices.delete.failedTitle": "Delete failed",

    // components/admin-proxies.tsx — table columns
    "admin.proxies.col.address": "Proxy address",
    "admin.proxies.col.type": "Type",
    "admin.proxies.col.country": "Country",
    "admin.proxies.col.status": "Status",
    "admin.proxies.col.bindings": "Bindings",
    "admin.proxies.col.failures": "Failures",
    "admin.proxies.status.online": "Online",
    "admin.proxies.status.invalid": "Invalid",

    // admin-proxies.tsx — stat cards
    "admin.proxies.stat.totalLabel": "Total proxies",
    "admin.proxies.stat.totalSub": "Platform-wide egress",
    "admin.proxies.stat.onlineSub": "Healthy and schedulable",
    "admin.proxies.stat.offlineLabel": "Offline / invalid",
    "admin.proxies.stat.offlineSub": "Needs review or removal",
    "admin.proxies.stat.currentPageLabel": "Current page",
    "admin.proxies.stat.currentPageSub": "Total matching the filter",

    // admin-proxies.tsx — table search/empty/filters
    "admin.proxies.searchPlaceholder": "Search address or country…",
    "admin.proxies.emptyState": "The proxy pool is empty. Click bulk import in the top right.",
    "admin.proxies.filterByStatusAria": "Filter by status",
    "admin.proxies.filter.allStatuses": "All statuses",

    // admin-proxies.tsx — row actions
    "admin.proxies.actionsAria": "Actions",
    "admin.proxies.action.edit": "Edit",
    "admin.proxies.action.delete": "Delete",

    // admin-proxies.tsx — ImportProxiesDialog
    "admin.proxies.import.title": "Bulk import proxies",
    "admin.proxies.import.descPrefix": "One per line, format ",
    "admin.proxies.import.descSuffix": " (omit the last two segments if no auth).",
    "admin.proxies.typeLabel": "Type",
    "admin.proxies.countryIsoLabel": "Country ISO-2",
    "admin.proxies.listLabel": "Proxy list",
    "admin.proxies.parsedCount": "Parsed {n} entries",
    "admin.proxies.cancel": "Cancel",
    "admin.proxies.import.importing": "Importing…",
    "admin.proxies.import.confirm": "Import {n} entries",
    "admin.proxies.import.successTitle": "Proxies imported",
    "admin.proxies.import.successDesc": "Added {added} · Skipped {skipped}",
    "admin.proxies.import.failedTitle": "Import failed",
    "admin.proxies.retry": "Please try again",
    "admin.proxies.loadFailed": "Load failed",

    // admin-proxies.tsx — EditProxyDialog
    "admin.proxies.edit.title": "Edit proxy",
    "admin.proxies.edit.descPrefix": "Edit the type, country, and max bindings for ",
    "admin.proxies.edit.descSuffix": ".",
    "admin.proxies.maxBindingsLabel": "Max bindings",
    "admin.proxies.edit.saving": "Saving…",
    "admin.proxies.edit.confirmSave": "Confirm save",
    "admin.proxies.edit.successTitle": "Proxy updated",
    "admin.proxies.edit.failedTitle": "Update failed",

    // admin-proxies.tsx — DeleteProxyDialog
    "admin.proxies.delete.title": "Delete proxy",
    "admin.proxies.delete.descPrefix": "Confirm deleting proxy ",
    "admin.proxies.delete.descSuffix": "? This cannot be undone — any bound devices will be unbound automatically.",
    "admin.proxies.delete.deleting": "Deleting…",
    "admin.proxies.delete.confirm": "Confirm delete",
    "admin.proxies.delete.successTitle": "Proxy deleted",
    "admin.proxies.delete.failedTitle": "Delete failed",

    // components/admin-metrics.tsx
    "admin.metrics.loadFailedGeneric": "Load failed",
    "admin.metrics.overviewLoadFailedPrefix": "Overview failed to load: ",
    "admin.metrics.totalConsumedRevenue": "Total consumed / revenue",
    "admin.metrics.cumulativeTopupPrefix": "Cumulative top-up ",
    "admin.metrics.activeWaAccounts": "Active WA accounts",
    "admin.metrics.bannedFlaggedPrefix": "Banned/flagged ",
    "admin.metrics.queueBacklog": "Queue backlog",
    "admin.metrics.pendingRecipients": "Pending recipients",
    "admin.metrics.banRate": "Ban rate",
    "admin.metrics.bannedFlaggedOverTotal": "banned+flagged / total accounts",
  },
};
