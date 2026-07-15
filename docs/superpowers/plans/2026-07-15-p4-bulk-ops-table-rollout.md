# P4 批量操作接线 + 表格能力铺开 — 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 P1 建好的 ProDataTable 三大能力(批量 `selection` / 列显隐 `storageKey` / 筛选抽屉 `filters`)铺到各业务列表页,批量操作复用现有单项端点前端循环 per-item,完成换皮路线图收尾。

**Architecture:** 纯前端。通用 `BulkActionDialog` 组件(确认+可选输入)各页复用;批量 = selection.actions 循环调单项端点 + 汇总 toast(照 P2 admin-instances)。不新增后端,不碰引擎。

**Tech Stack:** Next.js 16 魔改版 + React 19 + Tailwind 4 + P1 ProDataTable + P1b i18n dict。

**Spec:** `docs/superpowers/specs/2026-07-15-p4-bulk-ops-table-rollout-design.md`(含能力矩阵)

## Global Constraints

- **纯前端**:不新增后端端点、不碰 Go/引擎。批量 = 前端循环现有单项端点 + per-item 汇总 toast(诚实报告部分失败,不谎称全成功——照 P2 admin-instances 的批量登出/删除)。
- **列显隐**:主标识列必须 `hideable:false`(P1 立约,防全隐);T3 顺手在 pro-data-table 加 `Math.max(1, colSpan)` 保险(P3 终审建议)。
- 文案入 i18n dict(`admin.*.bulk.*` / `admin.*.cols.*` 等,zh/en 成对);无硬编码中文(除注释);插值函数式 `.replace("{x}", () => v)`;避免 `(t)=>` 遮蔽 useT。
- 门槛(每任务,`cd frontend`):`npx tsc --noEmit` + `node scripts/check-i18n-keys.mjs`(0 missing)+ 改动文件 `npx eslint` 无新错误(既有 `react-hooks/set-state-in-effect` baseline 不增)+ `npm run build`。前台跑,别起后台 Monitor。
- git 在执行时 worktree 根;每任务至少一 commit。

---

### Task 1: 通用 BulkActionDialog + 客户中心批量(挂起/恢复/指派销售)

**Files:**
- Create: `frontend/components/admin/bulk-action-dialog.tsx`
- Modify: `frontend/components/admin-users-table.tsx`(接 selection + 批量 actions)
- Modify: `frontend/lib/i18n/dicts/admin.ts`(`admin.users.bulk.*`)

**Interfaces:**
- Produces:
```tsx
// bulk-action-dialog.tsx — 复用确认弹框(可带一个输入槽)
interface BulkActionDialogProps {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: string;            // 已 t() 的文案
  description: string;      // 含选中数,调用方 .replace("{n}",...)
  confirmLabel: string;
  destructive?: boolean;    // 红色确认按钮
  children?: React.ReactNode; // 可选输入槽(如指派选销售下拉、原因输入)
  onConfirm: () => Promise<void>; // 执行批量;内部 loading
}
```

- [ ] **Step 1: 勘查** — 读 P2 admin-instances.tsx 的 selection.actions + 批量确认弹框 + per-item 汇总 toast 写法(照抄模式);读 admin-users-table.tsx 现有单个挂起/指派操作(确认怎么拿每行的 tenant_id + 调 `POST /tenants/:id/status`(挂起恢复 body)/ `POST /sales/:id/assign`(指派 body));读 P1 ProDataTable 的 SelectionMode(selected/onChange/actions)。
- [ ] **Step 2: BulkActionDialog** — 基于现有 ui/dialog + button,受控 open,children 输入槽,onConfirm 内 loading 态。全文案由调用方传入(已 t())。
- [ ] **Step 3: 客户中心接 selection** — admin-users-table 加 `const [sel, setSel] = useState<Set<...>>(new Set())`;ProDataTable 传 `selection={{selected:sel, onChange:setSel, actions:(keys)=> <批量按钮组>}}`;批量按钮:挂起/恢复(BulkActionDialog 确认 → 循环选中客户的 tenant_id 调 status → 汇总 toast)、指派销售(BulkActionDialog + 销售下拉 children → 循环 assign → 汇总)。per-item 失败独立计数,`toast.success` 仅 failCount==0 否则 `toast.error`(照 P2)。
- [ ] **Step 4: 字典 + 门槛** — `admin.users.bulk.*` zh/en 成对;tsc + check-i18n-keys(0 missing)+ eslint 改动文件 + build。
- [ ] **Step 5: 提交** — `git commit -m "feat(p4): BulkActionDialog + customer bulk suspend/resume/assign-sales"`。

---

### Task 2: 发送任务批量停/续 + 资源(设备/代理)批量删除

**Files:**
- Modify: `frontend/components/admin-campaigns.tsx`(批量停/续)
- Modify: `frontend/components/admin-devices.tsx`、`frontend/components/admin-proxies.tsx`(批量删除)
- Modify: `frontend/lib/i18n/dicts/admin.ts`

**Interfaces:** Consumes T1 的 `BulkActionDialog`;单项端点 `POST /campaigns/:id/stop|resume`、`DELETE /resources/devices/:id`、`DELETE /resources/proxies/:id`。

- [ ] **Step 1: 勘查** — 读 admin-campaigns 现有单个 stop/resume + admin-devices/proxies 现有单个删除(确认端点路径 + 每行 id);T1 的 BulkActionDialog 用法。
- [ ] **Step 2: campaigns 批量停/续** — selection + actions(停止/继续,BulkActionDialog 确认 → 循环 stop/resume → 汇总 toast)。注意 state:只对可停的任务停、可续的续(或不预筛,per-item 失败诚实报告)。
- [ ] **Step 3: devices/proxies 批量删除** — selection + actions(删除,destructive BulkActionDialog → 循环 DELETE → 汇总 toast + 刷新列表)。
- [ ] **Step 4: 字典 + 门槛 + 提交** — zh/en 成对;四门槛;`git commit -m "feat(p4): campaign bulk stop/resume + device/proxy bulk delete"`。

---

### Task 3: 列显隐全铺 + colSpan 保险

**Files:**
- Modify: `frontend/components/admin/pro-data-table.tsx`(colSpan `Math.max(1, ...)` 保险)
- Modify: 矩阵中标 ✓ 列显隐的表:admin-users-table、admin-campaigns、admin-devices、admin-proxies、admin-instances、admin-contacts、admin-send-records、admin-risk-monitor、admin-ledger、admin-reports、admin-audit-log

**Interfaces:** Consumes P1 ProDataTable 的 `storageKey` + Column `title`/`hideable`。

- [ ] **Step 1: 勘查** — 读 P1 ProDataTable 的 storageKey/列菜单实现 + Column.title/hideable;确认 colSpan 计算处(P3 终审建议 `Math.max(1, colSpan)`)。
- [ ] **Step 2: colSpan 保险** — pro-data-table 的错误/骨架/空态行 colSpan 用 `Math.max(1, colSpan)`(防全列隐时 colSpan=0 非法)。
- [ ] **Step 3: 逐表加 storageKey** — 每张表:ProDataTable 传唯一 `storageKey="admin-<name>"`;给每个 Column 补 `title`(纯文本列名,菜单显示)+ **主标识列(号码/名称/id)传 `hideable:false`**;其余列默认可隐。批量改动,每表小改。
- [ ] **Step 4: 门槛** — tsc + check-i18n-keys(0 missing;title 若用 t() 需入字典)+ eslint 改动文件 + build。
- [ ] **Step 5: 提交** — `git commit -m "feat(p4): column visibility across list tables + colSpan floor guard"`。

---

### Task 4: 筛选抽屉(设备/代理多维)

**Files:**
- Modify: `frontend/components/admin-devices.tsx`、`frontend/components/admin-proxies.tsx`
- Modify: `frontend/lib/i18n/dicts/admin.ts`

**Interfaces:** Consumes P1 ProDataTable 的 `filters`({content, activeCount, title})。

- [ ] **Step 1: 勘查** — 读 P1 ProDataTable 的 filters 抽屉用法 + admin-devices/proxies 现有筛选参数(哪些维度可筛:节点/状态/代理/国家/健康等,后端 buildWhere 支持哪些 query)。**理性判断**:若现有 state Tab + 搜索已覆盖主要筛选需求,filters 抽屉只补"多维组合"(如设备按 节点+ban_status+代理绑定 组合筛);若维度少则精简,报告说明。
- [ ] **Step 2: devices 筛选抽屉** — filters={{content: <多维筛选表单>, activeCount: <活跃筛选数>}},父组件持筛选状态,传后端 query。
- [ ] **Step 3: proxies 筛选抽屉** — 同理(国家/类型/存活状态等后端支持的维度)。
- [ ] **Step 4: 字典 + 门槛 + 提交** — zh/en 成对;四门槛;`git commit -m "feat(p4): filter drawers for devices/proxies multi-dimension"`。

---

### Task 5: 客户端联系人库批量(打标签/加黑名单)

**Files:**
- Modify: `frontend/components/dashboard/contacts-list.tsx`
- Modify: `frontend/lib/i18n/dicts/dashboard.ts`

**Interfaces:** Consumes T1 BulkActionDialog + 模块二 bulk 端点(勘查确认)。

- [ ] **Step 1: 勘查(关键)** — grep 后端联系人 bulk 端点(模块二:标签 bulk apply/remove、suppression add;`grep -n "contacts.*tag\|bulk\|suppression" internal/api/router.go`)。确认端点签名(批量 apply tag 是否接受 contact_ids 数组,还是逐个)。**若后端已有批量 apply/add 端点**(接受数组)→ 直接调;**若只有单项**→ 前端循环 per-item(照 P2)。**若联系人库端点结构复杂/不清晰,报告 DONE_WITH_CONCERNS 说明,本任务可降级为只加列显隐**(批量留后续)。
- [ ] **Step 2: contacts-list 接 selection** — 批量打标签(选标签 → apply)、批量加黑名单(suppression add,合规:加黑名单确认文案要清晰"将退订这些号码")。用 BulkActionDialog + 汇总 toast。
- [ ] **Step 3: 字典 + 门槛 + 提交** — `dash.contacts.bulk.*` zh/en;四门槛;`git commit -m "feat(p4): contacts bulk tag/suppress"`(或 concerns 说明)。

---

### Task 6: 精修收尾 + 全量终验

- [ ] **Step 1: 全量残余中文扫描**(排除 landing/en/nav/dicts)→ 第④类遗漏归零。
- [ ] **Step 2: 门槛** — 前端 tsc + check-i18n-keys(0 missing)+ eslint(baseline 不增)+ build。
- [ ] **Step 3: 浏览器冒烟**(控制器 playwright)— 客户中心/发送任务/设备批量操作条出现(选中行)、列菜单显隐、筛选抽屉打开 × 中英;确认批量确认弹框渲染、列隐藏持久化、EN 态无中文残留。(数据 401 无妨,验交互 shell + 空态。)
- [ ] **Step 4: 提交** — `git commit -m "chore(p4): final sweep + smoke verification"`(若有修)。

---

## 计划外(不做)

- 批量充值;批量解除隔离(涉引擎);新批量后端端点;只读表批量;联系人若无 bulk 端点则不硬造;筛选抽屉硬塞到已够用的表。
