# P4 批量操作接线 + 表格能力铺开 设计

日期:2026-07-15
状态:已获用户批准(P4 两问定案)
上位:`2026-07-13-platform-reskin-crud-design.md` 模块④⑥ + P4 收尾

## 1. 目标

兑现 P1 建好但零/低接线的 ProDataTable 三大能力(批量操作条 `selection` / 列显隐 `storageKey` / 筛选抽屉 `filters`),铺到各业务列表页,完成换皮路线图收尾。

## 2. 需求定案(P4 两问)

- **覆盖范围**:全铺——所有非只读列表页上批量,所有列多表上列显隐,多维筛选表上筛选抽屉。理性配置(每表配合适能力,非机械三塞;只读表不加批量)。
- **批量充值**:**不做**(金额个性化强)。批量只做挂起/恢复、指派销售、停/续任务、删除、联系人打标签/黑名单。

## 3. 架构

- **纯前端**(+ 复用现有单项端点,批量=前端循环 per-item + 汇总 toast,照 P2 admin-instances 的 selection.actions 模式)。不新增后端端点,不碰引擎。
- **通用批量确认对话框**:抽一个 `BulkActionDialog`(选中数 + 确认 + 可选输入如指派选销售/挂起原因),各页复用。避免每页重造确认弹框。
- 后端单项端点已齐:`POST /tenants/:id/status`(挂起恢复)、`POST /sales/:id/assign`(指派)、`POST /campaigns/:id/stop|resume`、`DELETE /resources/devices/:id`、`DELETE /resources/proxies/:id`;联系人 bulk tag/suppression 用模块二既有端点(T5 勘查确认)。
- 列显隐 `storageKey`:**P1 立约——主标识列必须 `hideable:false`**(防全隐);顺手加 P3 终审建议的 `Math.max(1, colSpan)` 保险(若 pro-data-table 未做)。
- 门槛:前端 `npx tsc --noEmit` + `node scripts/check-i18n-keys.mjs`(0 missing)+ 改动文件无新 lint 错误 + `npm run build`;文案入 i18n dict zh/en 成对。

## 4. 能力矩阵(每表配置)

| 表 | 批量操作 | 列显隐 | 筛选抽屉 | 备注 |
|---|---|---|---|---|
| admin-users-table(客户) | 挂起/恢复、指派销售 | ✓ | — | 客户中心;充值不批量 |
| admin-campaigns(发送任务) | 停止/继续 | ✓ | — | 已有 state Tab |
| admin-devices | 删除 | ✓ | ✓ | 列多+节点/状态/代理多维 |
| admin-proxies | 删除 | ✓ | ✓ | 多维 |
| admin-instances(P2) | 已有登出/删除 | 补 ✓ | — | selection 已接 |
| admin-contacts(只读跨租户) | — | ✓ | — | 只读 |
| admin-send-records(只读) | — | ✓ | — | 已有 state Tab |
| admin-risk-monitor 异常账号(P3) | — | ✓ | — | 解除隔离涉引擎,不批量 |
| admin-ledger(只读) | — | ✓ | — | 已有 kind Tab |
| admin-reports 消耗排行(只读) | — | ✓ | — | |
| admin-audit-log(只读) | — | ✓ | — | 已有筛选 |
| dashboard/contacts-list(客户端) | 打标签、加黑名单 | ✓ | ✓ | 依赖模块二 bulk 端点 |
| dashboard/suppression-list | — | — | — | append-only 只读 |
| admin-billing | — | — | — | 图表为主非表 |

## 5. 分任务(SDD)

- T1 通用 `BulkActionDialog` 组件 + 客户中心批量(挂起/恢复/指派销售,per-item 汇总 toast)
- T2 发送任务批量停/续 + 资源(设备/代理)批量删除
- T3 列显隐全铺(给矩阵中标 ✓ 的表加 storageKey + 主标识列 hideable:false;pro-data-table 补 colSpan≥1 保险)
- T4 筛选抽屉(设备/代理多维筛选;若现有筛选已够则理性精简)
- T5 客户端联系人库批量(打标签/加黑名单,勘查模块二 bulk 端点;端点不支持则降级为记录并跳过,报告说明)
- T6 精修收尾 + 全量终验(残余中文第④类归零)+ 浏览器冒烟(批量/列显隐 × 中英)

## 6. 验收标准

- 客户中心/发送任务/设备/代理可批量操作(选中→确认→per-item 执行→汇总 toast 诚实报告部分失败)。
- 列多表有"列"菜单,隐藏选择持久化,主标识列不可隐(全隐不崩)。
- 设备/代理有高级筛选抽屉(多维);联系人库客户端可批量打标签/加黑名单(若端点支持)。
- 批量操作复用单项端点循环,不新增后端;不碰引擎红线。
- 前端 build+lint(baseline 不回归)+ check-i18n-keys 0 missing;批量/列显隐浏览器冒烟(中英)。

## 7. 计划外(不做)

- 批量充值(金额个性化);批量解除隔离(涉引擎);新批量后端端点(前端循环足够);只读表的批量;联系人若无 bulk 端点则不硬造。
