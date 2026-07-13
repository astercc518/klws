# P1 基建:WhatsApp 主题 + cookie/SSR 双语 + ProDataTable 升级 — 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 全站(admin/dashboard/sales/auth)换上 WhatsApp 官方风双主题(亮/暗)+ 中英即时切换,并把 ProDataTable 升级为带批量选择/列自定义/筛选抽屉/快捷搜索的表格底座。

**Architecture:** 三条独立线:①i18n——把 `feat/app-i18n-ssr` 分支顶部 6 个纯前端提交(cookie+SSR LocaleProvider 机制+admin chrome 翻译)cherry-pick 回 main,再把 LanguageToggle 铺到 dashboard/sales/auth 顶栏;②主题——重写 `globals.css` 的 `:root`/`.dark` 语义 token 为 WhatsApp 官方色板(亮:白底+深青绿 `#008069`;暗:墨蓝 `#111B21`/`#202C33`+亮绿 `#00A884`),图表 token 用已过 dataviz 验证器的两组分类色;③表格——ProDataTable 向后兼容地增加 selection/storageKey/filters/`/` 快捷键四个可选能力。

**Tech Stack:** Next.js 16(App Router,魔改版)、React 19、Tailwind 4(`@theme` token)、shadcn/Base UI、Tremor v3、next-themes。

**Spec:** `docs/superpowers/specs/2026-07-13-platform-reskin-crud-design.md`

## Global Constraints

- **这不是你认识的 Next.js**:`frontend/AGENTS.md` 声明这是魔改构建,写任何 Next 相关代码前先读 `node_modules/next/dist/docs/` 对应指南(`cookies()`/`headers()` 均为 async)。
- **前端无 JS 测试框架**。每任务验证门槛(在 `frontend/` 下执行):`npx tsc --noEmit`(exit 0)+ `npm run lint`(**仅要求本任务改动的文件干净**;仓库有 ~29 条 `react-hooks/set-state-in-effect` 既定 house-style baseline,不许增不必减)+ 任务收尾 `npm run build` 成功。
- **不碰任何 Go 代码、migrations、五大引擎**。本计划纯前端 + docs。
- **ProDataTable 所有新能力必须向后兼容**:十几个既有调用点不传新 prop 时行为与现在完全一致。
- **不合并 `feat/app-i18n-ssr` 整支**(它底下压着未合并的 remove-tenant 大重构,与 main 的 tenant 模型矛盾)。只 cherry-pick 指定的 6 个提交。
- **UI 文案键名**:点分命名空间(`table.empty`、`header.logout`),字典按域拆文件到 `frontend/lib/i18n/dicts/`。
- 提交信息用 conventional commits;每任务至少一个 commit。

---

### Task 1: Cherry-pick i18n 机制系列(6 提交)+ nav.ts 冲突解决

`feat/app-i18n-ssr` 顶部 6 个纯前端提交实现了 cookie+SSR 的双语机制和 admin chrome 翻译,其分支基点(0e557ab)以来 main 只动过 `frontend/components/admin/nav.ts`(+18 行代理分销导航),故预期**唯一冲突点是 nav.ts**。

**Files:**
- 新增(来自 cherry-pick):`frontend/components/locale-provider.tsx`、`frontend/components/language-toggle.tsx`、`frontend/lib/server-locale.ts`、`frontend/lib/i18n/dicts/common.ts`、`frontend/lib/i18n/dicts/index.ts`
- 修改(来自 cherry-pick):`frontend/app/layout.tsx`、`frontend/lib/i18n.ts`、`frontend/components/admin-header.tsx`、`frontend/components/admin-sidebar.tsx`、`frontend/components/admin/nav.ts`(冲突)、`frontend/components/admin/page-header.tsx`、`frontend/components/admin/admin-avatar.tsx`、`frontend/components/theme-toggle.tsx`
- 修改:`docs/superpowers/specs/2026-07-13-platform-reskin-crud-design.md`(§4.2 localStorage→cookie+SSR)

**Interfaces:**
- Produces(后续任务全依赖):`useLocale(): {locale, setLocale, toggle, t}` 与 `useT(): (key:string)=>string`(from `@/components/locale-provider`);`<LanguageToggle/>`(from `@/components/language-toggle`);字典合并点 `frontend/lib/i18n/dicts/index.ts` 的 `namespaces` 数组;`getLocale(): Promise<Locale>`(from `@/lib/server-locale`,服务端用)。

- [ ] **Step 1: 按序 cherry-pick 六个提交**

```bash
cd /var/klwa
git cherry-pick c5e0d17 daccfb0 00cf2f5 d2afc6b 0e77453 659e2d1
```

预期:在 `d2afc6b`(locale-aware sidebar nav + breadcrumb)停下,报 `frontend/components/admin/nav.ts` 冲突;其余提交干净落地。若其他文件也冲突,原则一致:**结构/文案以 main 为准,只叠加分支引入的 i18n 能力(`en` 字段、`useT` 调用)**。

- [ ] **Step 2: 解决 nav.ts 冲突**

合并原则:保留 main 的全部 7 个分组与条目(**含"代理分销"组、"联系人"条目,标签保持"客户"不要改成分支的"用户管理"**——那是废弃的 customer 模型时代文案);叠加分支的 `NavGroup.en` 字段。冲突解决后 `NavGroup` 接口与 7 个分组头如下(条目行原样保留 main 的):

```ts
export interface NavGroup {
  /** Section heading shown above its items (hidden when collapsed). */
  title: string;
  /** English section heading, used when locale is "en". */
  en: string;
  items: NavItem[];
}
```

7 个分组的 `en` 值:概览=`"Overview"`、客户与财务=`"Customers & Finance"`、资源=`"Resources"`、发送中心=`"Sending"`、代理分销=`"Agents"`、策略中心=`"Policy"`、系统审计=`"Audit"`。

```bash
git add frontend/components/admin/nav.ts && git cherry-pick --continue
```

- [ ] **Step 3: 类型检查确认无 customer 模型残留**

```bash
cd frontend && npx tsc --noEmit
```

预期:exit 0。若报错指向 cherry-pick 进来的文件引用了 main 不存在的东西(如 `/customer` 路由、`customer_id` 字段),按 main 现状修正(tenant 模型)。

- [ ] **Step 4: 修订 spec §4.2**

把 spec 中"locale 存 localStorage"一句改为:"locale 存 cookie,服务端 async 根布局经 `getLocale()` 读取(cookie 优先→`Accept-Language`→默认 zh),SSR 首屏即正确语言,无水合闪烁;切换写 cookie + `router.refresh()`。(采纳自 `feat/app-i18n-ssr` 分支已实现机制,原 localStorage 方案有英文浏览器首屏闪烁问题。)"

- [ ] **Step 5: lint 改动文件 + 构建 + 提交 spec 修订**

```bash
cd frontend && npm run lint 2>&1 | grep -E "locale-provider|language-toggle|server-locale|dicts|nav\.ts|layout\.tsx|admin-header|admin-sidebar|page-header|admin-avatar|theme-toggle|i18n" ; npm run build
cd /var/klwa && git add docs/superpowers/specs/2026-07-13-platform-reskin-crud-design.md && git commit -m "docs(spec): P1 i18n mechanism is cookie+SSR (adopted from feat/app-i18n-ssr)"
```

预期:改动文件无新 lint 错误;build 成功。

**验收:** `npm run dev` 打开 `/admin`,顶栏出现"中/EN"切换钮,点击后侧边栏分组标题、面包屑、账户菜单即时中英互换,刷新保持;`<html lang>` 跟随变化;无首屏语言闪烁。

---

### Task 2: 双语铺到 dashboard/sales/auth 顶栏 + ProDataTable 字符串入字典

Task 1 只覆盖 admin chrome。本任务把 LanguageToggle 挂到客户端、销售端、登录壳三处顶栏,并把共享表格组件 ProDataTable 的 5 处硬编码中文转 `t()`(它是全站 chrome 的一部分,后续任务也要在里面加带文案的新 UI)。

**Files:**
- Modify: `frontend/components/header.tsx`(客户端顶栏)
- Modify: `frontend/components/sales-header.tsx`
- Modify: `frontend/components/auth/auth-shell.tsx`(已有 ThemeToggle,旁边加 LanguageToggle)
- Modify: `frontend/components/admin/pro-data-table.tsx`
- Modify: `frontend/lib/i18n/dicts/common.ts`(加 key)

**Interfaces:**
- Consumes: Task 1 的 `useT`/`useLocale`、`<LanguageToggle/>`、`common.ts` 字典。
- Produces: `common.ts` 新增键 `table.searchPlaceholder`、`table.loadFailed`、`table.empty`、`table.noMatch`、`table.prevPage`、`table.nextPage`、`sales.tagline`(后续任务可复用)。

- [ ] **Step 1: common.ts 追加键**

在 `zh`/`en` 两块各自追加(保持点分命名空间,按 namespace 分段放):

```ts
// zh 块追加:
"table.searchPlaceholder": "搜索…",
"table.loadFailed": "加载失败:",
"table.empty": "暂无数据",
"table.noMatch": "没有匹配「{q}」的结果",
"table.prevPage": "上一页",
"table.nextPage": "下一页",
"sales.tagline": "名下客户管理",
// en 块追加:
"table.searchPlaceholder": "Search…",
"table.loadFailed": "Failed to load: ",
"table.empty": "No data",
"table.noMatch": "No results for \"{q}\"",
"table.prevPage": "Previous page",
"table.nextPage": "Next page",
"sales.tagline": "Managed customers",
```

- [ ] **Step 2: ProDataTable 换 t()**

`pro-data-table.tsx` 顶部 `import { useT } from "@/components/locale-provider";`,组件体内 `const t = useT();`,然后:
- L126 `placeholder={search.placeholder ?? "搜索…"}` → `placeholder={search.placeholder ?? t("table.searchPlaceholder")}`
- L157 `加载失败:{error}` → `{t("table.loadFailed")}{error}`
- L171-173 空态三元 → `{queryValue.trim() ? t("table.noMatch").replace("{q}", queryValue.trim()) : (emptyState ?? t("table.empty"))}`
- L212 `aria-label="上一页"` → `aria-label={t("table.prevPage")}`;L224 同理 `table.nextPage`

- [ ] **Step 3: header.tsx(客户端)挂 toggle + 翻译**

```tsx
// 新增 import:
import { ThemeToggle } from "@/components/theme-toggle";
import { LanguageToggle } from "@/components/language-toggle";
import { useT } from "@/components/locale-provider";
// 组件体内:const t = useT();
// "Account + sign out" 区块改为:
      <div className="flex items-center gap-2">
        <span className="hidden font-mono text-xs text-muted-foreground sm:inline">
          {MOCK_TENANT.email}
        </span>
        <LanguageToggle />
        <ThemeToggle />
        <Button variant="ghost" size="sm" onClick={handleLogout} className="text-muted-foreground">
          <LogOut className="size-4" />
          {t("menu.logout")}
        </Button>
      </div>
```

(若该文件已有 ThemeToggle 挂载则只加 LanguageToggle,不重复。)

- [ ] **Step 4: sales-header.tsx 同款**

`Sales · 名下客户管理` → `Sales · {t("sales.tagline")}`;`退出登录` → `{t("menu.logout")}`;登出按钮前插入 `<LanguageToggle />` 与 `<ThemeToggle />`(与 header.tsx 相同的容器结构)。

- [ ] **Step 5: auth-shell.tsx 在 ThemeToggle 旁挂 LanguageToggle**

找到现有 `<ThemeToggle/>` 渲染处,前面插 `<LanguageToggle/>`。auth 页面正文文案不在本任务范围(P1b 铺开时做)。

- [ ] **Step 6: 验证 + 提交**

```bash
cd frontend && npx tsc --noEmit && npm run lint 2>&1 | grep -E "header\.tsx|sales-header|auth-shell|pro-data-table|common\.ts" ; npm run build
cd /var/klwa && git add -A frontend && git commit -m "feat(i18n): language+theme toggles on dashboard/sales/auth chrome; ProDataTable strings via dict"
```

预期:tsc exit 0;改动文件无新 lint 错误;build 成功。`/dashboard` `/sales` `/login` 顶栏均可切换语言与主题。

---

### Task 3: WhatsApp 官方双主题色板(globals.css)

现状:`:root`/`.dark` 是 shadcn 默认中性灰(暗色 `--sidebar-primary` 甚至是紫罗兰),品牌绿只存在于 `--color-brand-*` 工具色。本任务把语义 token 整体换成 WhatsApp 官方色系——亮色:`#F0F2F5` 应用底/白卡片/深青绿 `#008069` 主色;暗色:墨蓝 `#111B21` 底/`#202C33` 面板/亮绿 `#00A884` 主色。图表 token 用已过 dataviz 验证器(六检查全 PASS)的两组分类五色。

**Files:**
- Modify: `frontend/app/globals.css`(仅 `:root{}` 与 `.dark{}` 两块;`@theme inline` 与 `--color-brand-*` 不动)

**Interfaces:**
- Consumes: 无(纯 CSS 变量值替换,变量名一个不改)。
- Produces: 全站消费的语义 token 新值;`--chart-1..5` 两组已验证分类色。

- [ ] **Step 1: 替换 `:root` 块(亮色)**

变量名与顺序保持不变,只换值。`--radius: 0.625rem;` 保持原值不动:

```css
:root {
  --background: #f0f2f5;
  --foreground: #111b21;
  --card: #ffffff;
  --card-foreground: #111b21;
  --popover: #ffffff;
  --popover-foreground: #111b21;
  --primary: #008069;
  --primary-foreground: #ffffff;
  --secondary: #e9edef;
  --secondary-foreground: #111b21;
  --muted: #f0f2f5;
  --muted-foreground: #667781;
  --accent: #d9fdd3;
  --accent-foreground: #0b3d2e;
  --destructive: oklch(0.577 0.245 27.325);
  --border: #dfe5e7;
  --input: #dfe5e7;
  --ring: #00a884;
  --chart-1: #008069;
  --chart-2: #1173b3;
  --chart-3: #b45309;
  --chart-4: #7c4dd4;
  --chart-5: #bd3a72;
  --radius: 0.625rem;
  --sidebar: #ffffff;
  --sidebar-foreground: #111b21;
  --sidebar-primary: #008069;
  --sidebar-primary-foreground: #ffffff;
  --sidebar-accent: #f0f2f5;
  --sidebar-accent-foreground: #111b21;
  --sidebar-border: #e9edef;
  --sidebar-ring: #00a884;
}
```

- [ ] **Step 2: 替换 `.dark` 块(暗色,WhatsApp 墨蓝系)**

```css
.dark {
  --background: #111b21;
  --foreground: #e9edef;
  --card: #202c33;
  --card-foreground: #e9edef;
  --popover: #202c33;
  --popover-foreground: #e9edef;
  --primary: #00a884;
  --primary-foreground: #111b21;
  --secondary: #2a3942;
  --secondary-foreground: #e9edef;
  --muted: #182229;
  --muted-foreground: #8696a0;
  --accent: #2a3942;
  --accent-foreground: #e9edef;
  --destructive: oklch(0.704 0.191 22.216);
  --border: rgb(134 150 160 / 15%);
  --input: rgb(134 150 160 / 22%);
  --ring: #00a884;
  --chart-1: #00a884;
  --chart-2: #2f97cc;
  --chart-3: #b8850f;
  --chart-4: #8f6fe8;
  --chart-5: #dd5794;
  --sidebar: #202c33;
  --sidebar-foreground: #e9edef;
  --sidebar-primary: #00a884;
  --sidebar-primary-foreground: #111b21;
  --sidebar-accent: #2a3942;
  --sidebar-accent-foreground: #e9edef;
  --sidebar-border: rgb(134 150 160 / 15%);
  --sidebar-ring: #00a884;
}
```

两块上方各加一行注释:`/* WhatsApp-inspired palette — light: white surfaces + deep teal-green; dark: ink-blue #111B21 family + bright green. Chart tokens validated (dataviz 6-check) against their mode's card surface. */`

- [ ] **Step 3: 构建 + 双主题目检**

```bash
cd frontend && npx tsc --noEmit && npm run build
```

然后 `npm run dev`,亮/暗各截一遍 `/login`、`/admin`(大盘)、`/admin/users`、`/dashboard`:确认①主按钮/链接/激活态导航是绿系;②暗色底是墨蓝不是纯灰黑;③正文文字对比度目测无低于可读的区域;④图表(大盘趋势卡)配色随主题正确切换;⑤无残留紫罗兰 sidebar-primary。

- [ ] **Step 4: 提交**

```bash
cd /var/klwa && git add frontend/app/globals.css && git commit -m "feat(theme): WhatsApp official palette for light/dark semantic tokens + validated chart colors"
```

---

### Task 4: 硬编码颜色清扫(chrome 范围)

palette 换掉后,直接写 Tailwind 调色板类(`emerald-500`、`bg-black` 等)的地方不会跟随主题。本任务只清扫**共享 chrome**(顶栏/侧边栏/auth 壳/共享小组件),不动 landing 营销页(它有独立视觉),不动各业务页面正文(P1b/P4 逐页精修时顺手处理)。

**Files:**
- Modify(以 grep 结果为准,预期主要是):`frontend/components/header.tsx`、`frontend/components/admin-header.tsx`、`frontend/components/sales-header.tsx`、`frontend/components/sidebar.tsx`、`frontend/components/admin-sidebar.tsx`、`frontend/components/sales-sidebar.tsx`、`frontend/components/auth/*.tsx`、`frontend/components/metric-card.tsx`

**Interfaces:** 无新接口;纯类名替换。

- [ ] **Step 1: 清点**

```bash
cd frontend && grep -rnE "(emerald|green|teal|violet|indigo|sky|blue|slate|zinc|gray|neutral)-[0-9]{2,3}" components/header.tsx components/admin-header.tsx components/sales-header.tsx components/sidebar.tsx components/admin-sidebar.tsx components/sales-sidebar.tsx components/auth components/metric-card.tsx components/admin/page-header.tsx
```

- [ ] **Step 2: 按规则替换**

决策规则(逐条 hit 应用,拿不准的保留并在提交信息里列出):
- 表示"在线/正常"的状态点、上升趋势:`emerald-*`/`green-*` → `brand-500`(亮暗通用的品牌绿工具色,已存在)或语义化 `text-primary`。
- 装饰性中性灰(`slate/zinc/gray/neutral-*`)→ 对应语义 token 类:文字用 `text-muted-foreground`,底用 `bg-muted`,边用 `border-border`。
- 任何 `violet/indigo/sky/blue-*` 的强调用色 → `text-primary`/`bg-primary`(除非它明确表达"信息/链接"语义且与绿冲突,则保留并记录)。
- 红色告警/danger 一律不动(`destructive` 语义已覆盖)。

- [ ] **Step 3: Tremor 图表配色对齐**

Tremor v3 不消费 `--chart-*` 变量,吃的是自己的命名色(`colors={["emerald"]}`,经 globals.css 的 `@source inline` 编译进来)。清点并对齐:

```bash
cd frontend && grep -rn "colors=" components/*trend-chart*.tsx components/admin-metrics.tsx components/dashboard components/admin 2>/dev/null | grep -v node_modules
```

规则:系列首色统一用 `emerald`(Tremor 命名色里最接近 WhatsApp 绿),第二系列用 `cyan`,禁用 `violet/indigo` 作强调;若改动引入了新的 Tremor 色名,同步把该色名加进 globals.css 顶部 `@source inline("{fill,stroke,text}-{…}")` 的枚举,否则类不会被编译(现有枚举只含 emerald/rose/neutral)。

- [ ] **Step 4: 验证 + 提交**

```bash
cd frontend && npx tsc --noEmit && npm run build
cd /var/klwa && git add -A frontend && git commit -m "refactor(theme): sweep hardcoded palette classes in shared chrome to semantic tokens"
```

`npm run dev` 亮暗两态复查上述文件对应的界面区域(顶栏状态点、侧边栏激活态、auth 壳、大盘趋势图)无突兀色块。

---

### Task 5: ProDataTable 批量选择 + 批量操作条

向后兼容新增 `selection` prop:受控选择集(父组件持有 `Set`),表格渲染勾选列与批量操作条。不传时零变化。

**Files:**
- Modify: `frontend/components/admin/pro-data-table.tsx`
- Modify: `frontend/lib/i18n/dicts/common.ts`(加 `table.selected`/`table.clearSelection`/`table.selectAll`/`table.selectRow` 四键)

**Interfaces:**
- Consumes: Task 2 已引入的 `useT`。
- Produces(P4 批量操作接线时消费):

```ts
export interface SelectionMode {
  /** Controlled set of selected row keys (from getRowKey). */
  selected: ReadonlySet<string | number>;
  onChange: (next: Set<string | number>) => void;
  /** Rendered on the right side of the bulk bar. */
  actions?: (keys: Array<string | number>) => React.ReactNode;
}
// ProDataTableProps 新增可选字段: selection?: SelectionMode;
```

- [ ] **Step 1: common.ts 追加键**

```ts
// zh:
"table.selected": "已选 {n} 项",
"table.clearSelection": "清除选择",
"table.selectAll": "全选本页",
"table.selectRow": "选择此行",
// en:
"table.selected": "{n} selected",
"table.clearSelection": "Clear selection",
"table.selectAll": "Select page",
"table.selectRow": "Select row",
```

- [ ] **Step 2: 实现选择列与批量条**

`pro-data-table.tsx` 改动(在现有代码基础上叠加):

```tsx
// 接口区新增(见 Interfaces 块的 SelectionMode 定义,原样落码);
// ProDataTableProps 加 selection?: SelectionMode;
// 组件参数解构加 selection。

// colSpan 计入勾选列:
const colSpan = columns.length + (rowActions ? 1 : 0) + (selection ? 1 : 0);

// 当前页选择状态(rows 为当前页行):
const pageKeys = rows.map((r) => getRowKey(r));
const allPageSelected =
  !!selection && pageKeys.length > 0 && pageKeys.every((k) => selection.selected.has(k));
const somePageSelected = !!selection && pageKeys.some((k) => selection.selected.has(k));

function togglePage() {
  if (!selection) return;
  const next = new Set(selection.selected);
  if (allPageSelected) pageKeys.forEach((k) => next.delete(k));
  else pageKeys.forEach((k) => next.add(k));
  selection.onChange(next);
}
function toggleRow(key: string | number) {
  if (!selection) return;
  const next = new Set(selection.selected);
  if (next.has(key)) next.delete(key);
  else next.add(key);
  selection.onChange(next);
}
```

TableHeader 行首(columns.map 之前)插入:

```tsx
{selection && (
  <TableHead className="w-10">
    <input
      type="checkbox"
      aria-label={t("table.selectAll")}
      className="size-3.5 accent-primary"
      checked={allPageSelected}
      ref={(el) => { if (el) el.indeterminate = !allPageSelected && somePageSelected; }}
      onChange={togglePage}
    />
  </TableHead>
)}
```

数据行首(columns.map 之前)插入(`stopPropagation` 防触发 onRowClick):

```tsx
{selection && (
  <TableCell className="w-10 py-2.5" onClick={(e) => e.stopPropagation()}>
    <input
      type="checkbox"
      aria-label={t("table.selectRow")}
      className="size-3.5 accent-primary"
      checked={selection.selected.has(getRowKey(row))}
      onChange={() => toggleRow(getRowKey(row))}
    />
  </TableCell>
)}
```

错误/骨架/空态三个整行 `colSpan` 已由上面的 colSpan 计算覆盖,无需单改。

- [ ] **Step 3: 批量操作条**

工具栏区块之后、`<Table>` 之前插入:

```tsx
{selection && selection.selected.size > 0 && (
  <div className="flex flex-wrap items-center gap-2 border-b bg-accent/60 px-3 py-2">
    <span className="text-sm font-medium">
      {t("table.selected").replace("{n}", String(selection.selected.size))}
    </span>
    <Button
      variant="ghost"
      size="sm"
      className="text-muted-foreground"
      onClick={() => selection.onChange(new Set())}
    >
      {t("table.clearSelection")}
    </Button>
    <div className="ml-auto flex items-center gap-2">
      {selection.actions?.(Array.from(selection.selected))}
    </div>
  </div>
)}
```

- [ ] **Step 4: 验证 + 提交**

```bash
cd frontend && npx tsc --noEmit && npm run lint 2>&1 | grep -E "pro-data-table|common\.ts"; npm run build
```

预期全绿(不传 selection 的现有调用点零变化——tsc 若在任何 admin-*.tsx 报错即破坏了兼容,必须修)。临时验证:任选一页(如 `/admin/devices`)在浏览器 devtools 里确认无勾选列出现(未传 prop)。

```bash
cd /var/klwa && git add -A frontend && git commit -m "feat(table): opt-in row selection + bulk action bar on ProDataTable"
```

---

### Task 6: ProDataTable 列显隐自定义(localStorage 持久化)

新增 `storageKey` prop:传入时工具栏出现"列"菜单,可勾选隐藏列,选择持久化到 `localStorage["pdt:{storageKey}:hidden"]`。`Column` 增加可选 `title`(菜单里的纯文本列名,缺省用 `key`)与 `hideable`(默认 true;主标识列传 false 防全部藏光)。

**Files:**
- Modify: `frontend/components/admin/pro-data-table.tsx`
- Modify: `frontend/lib/i18n/dicts/common.ts`(加 `table.columns` 键:zh `"列"` / en `"Columns"`)

**Interfaces:**
- Produces:

```ts
export interface Column<T> {
  // ……既有字段不变,追加:
  /** Plain-text name shown in the column-visibility menu. Defaults to key. */
  title?: string;
  /** Set false to pin the column (not hideable). Default true. */
  hideable?: boolean;
}
// ProDataTableProps 新增: storageKey?: string;
```

- [ ] **Step 1: 实现**

```tsx
// import 区追加:
import { Columns3 } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Check } from "lucide-react";

// 组件体内(useState 区):
const [hidden, setHidden] = useState<Set<string>>(new Set());
useEffect(() => {
  if (!storageKey) return;
  try {
    const raw = localStorage.getItem(`pdt:${storageKey}:hidden`);
    if (raw) setHidden(new Set(JSON.parse(raw) as string[]));
  } catch {} // 损坏的存储值静默忽略,等同默认全显
  // eslint-disable-next-line react-hooks/exhaustive-deps
}, [storageKey]);

function toggleColumn(key: string) {
  setHidden((prev) => {
    const next = new Set(prev);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    if (storageKey) {
      try {
        localStorage.setItem(`pdt:${storageKey}:hidden`, JSON.stringify([...next]));
      } catch {}
    }
    return next;
  });
}

const visibleColumns = useMemo(
  () => (storageKey ? columns.filter((c) => !hidden.has(c.key)) : columns),
  [columns, hidden, storageKey],
);
```

**渲染层所有 `columns.map` 改为 `visibleColumns.map`**(表头与数据行两处),colSpan 计算同步改用 `visibleColumns.length`。

- [ ] **Step 2: 列菜单按钮**

工具栏区块内 `{toolbar && …}` 之前插入(与搜索框同排,靠右):

```tsx
{storageKey && (
  <DropdownMenu>
    <DropdownMenuTrigger
      render={
        <Button variant="outline" size="sm" className="ml-auto text-muted-foreground">
          <Columns3 className="size-3.5" />
          {t("table.columns")}
        </Button>
      }
    />
    <DropdownMenuContent align="end">
      {columns
        .filter((c) => c.hideable !== false)
        .map((c) => (
          <DropdownMenuItem key={c.key} closeOnClick={false} onClick={() => toggleColumn(c.key)}>
            <span className="flex size-4 items-center justify-center">
              {!hidden.has(c.key) && <Check className="size-3.5" />}
            </span>
            {c.title ?? c.key}
          </DropdownMenuItem>
        ))}
    </DropdownMenuContent>
  </DropdownMenu>
)}
```

注意:`DropdownMenuTrigger`/`DropdownMenuItem` 是 Base UI 封装——落码前读 `frontend/components/ui/dropdown-menu.tsx` 现有用法(trigger 的 render 模式在别的调用点有先例可抄);Base UI Menu.Item 若不支持 `closeOnClick={false}`,查 `node_modules/@base-ui/react` 文档确认正确的"点击不关菜单"写法,实在不支持则接受点击后菜单关闭(功能不损)。同时注意 `storageKey` 与 `toolbar` 并存时布局:两者都存在时把 `ml-auto` 留给最先出现的右侧元素,其余 `gap-2` 排列即可。

- [ ] **Step 3: 验证 + 提交**

```bash
cd frontend && npx tsc --noEmit && npm run build
```

`npm run dev` 给任意一处调用点临时加 `storageKey="dev-test"`(验证后撤销):藏两列→刷新仍隐藏→localStorage 里键值正确→撤销临时改动。

```bash
cd /var/klwa && git add -A frontend && git commit -m "feat(table): column visibility menu with localStorage persistence on ProDataTable"
```

---

### Task 7: ProDataTable 筛选抽屉 + `/` 快捷键聚焦搜索

新增 `filters` prop:传入时工具栏出现带活跃计数徽标的"筛选"按钮,点开右侧 Sheet 渲染父组件提供的筛选内容(父组件owns 筛选状态,与 server 模式一致的受控哲学)。另:页面任意处按 `/`(不在输入控件内)聚焦表格搜索框。

**Files:**
- Modify: `frontend/components/admin/pro-data-table.tsx`
- Modify: `frontend/lib/i18n/dicts/common.ts`(加 `table.filters` 键:zh `"筛选"` / en `"Filters"`)

**Interfaces:**
- Produces:

```ts
// ProDataTableProps 新增:
/** Filter drawer. Parent owns filter state; content is rendered inside a Sheet. */
filters?: { content: React.ReactNode; activeCount?: number; title?: string };
```

- [ ] **Step 1: 筛选按钮 + Sheet**

```tsx
// import 区追加:
import { useRef } from "react"; // 并入现有 react import
import { ListFilter } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";

// 组件体内:
const [filtersOpen, setFiltersOpen] = useState(false);
```

工具栏区(列菜单按钮旁)插入:

```tsx
{filters && (
  <Button
    variant="outline"
    size="sm"
    className="text-muted-foreground"
    onClick={() => setFiltersOpen(true)}
  >
    <ListFilter className="size-3.5" />
    {t("table.filters")}
    {(filters.activeCount ?? 0) > 0 && (
      <Badge variant="secondary" className="ml-1 px-1.5 font-mono text-[10px]">
        {filters.activeCount}
      </Badge>
    )}
  </Button>
)}
```

组件返回的 `<Card>` 末尾(分页条之后)插入:

```tsx
{filters && (
  <Sheet open={filtersOpen} onOpenChange={setFiltersOpen}>
    <SheetContent>
      <SheetHeader>
        <SheetTitle>{filters.title ?? t("table.filters")}</SheetTitle>
      </SheetHeader>
      <div className="grid gap-4 px-4">{filters.content}</div>
    </SheetContent>
  </Sheet>
)}
```

落码前读 `frontend/components/ui/sheet.tsx` 与既有调用点(如 `campaign-detail-sheet.tsx`)对齐 open/onOpenChange 与内容布局的实际用法。Badge variant 名以 `ui/badge.tsx` 实际导出为准。

- [ ] **Step 2: `/` 快捷键**

```tsx
// 搜索 input 加 ref:
const searchRef = useRef<HTMLInputElement>(null);
// <input …> 上加 ref={searchRef}

useEffect(() => {
  function onKey(e: KeyboardEvent) {
    if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return;
    const target = e.target as HTMLElement | null;
    if (target?.closest("input, textarea, select, [contenteditable='true']")) return;
    if (!searchRef.current) return;
    e.preventDefault();
    searchRef.current.focus();
  }
  window.addEventListener("keydown", onKey);
  return () => window.removeEventListener("keydown", onKey);
}, []);
```

已知取舍(可接受,注释记录在代码里):同屏多个 ProDataTable 时 `/` 聚焦最后挂载者的搜索框。

- [ ] **Step 3: 验证 + 提交**

```bash
cd frontend && npx tsc --noEmit && npm run build
```

`npm run dev`:任意列表页按 `/` 搜索框聚焦、输入框内按 `/` 正常输入字符不被劫持;临时传一个 `filters={{content:<div>test</div>, activeCount:2}}` 验证抽屉与徽标后撤销。

```bash
cd /var/klwa && git add -A frontend && git commit -m "feat(table): filter drawer slot + slash-to-search shortcut on ProDataTable"
```

---

### Task 8: 翻译记忆收割 + 全量终验

`feat/frontend-i18n-theme` 分支(落后 main 263 提交,永不合并)带着 709 行现成中英对照 `messages.ts`。把它存为参考文件,供 P1b(admin/dashboard/sales 全页面文案铺开)当翻译记忆,然后跑全量门槛收尾。

**Files:**
- Create: `docs/superpowers/reference/i18n-translation-memory.ts.txt`(从分支导出,原样存档)

**Interfaces:** 无;纯资料存档 + 终验。

- [ ] **Step 1: 收割**

```bash
cd /var/klwa && mkdir -p docs/superpowers/reference
git show feat/frontend-i18n-theme:frontend/lib/messages.ts > docs/superpowers/reference/i18n-translation-memory.ts.txt
```

文件头部手工加注释块:

```
// 翻译记忆存档 — 摘自 feat/frontend-i18n-theme(2026-07-02,落后 main 263 提交,该分支永不合并)。
// 仅作 P1b 文案铺开时的中英对照参考;键名结构与现行 lib/i18n/dicts/* 不同,不可直接 import。
// 注意:该分支基于已废弃的 customer-as-workspace 模型,涉及"用户/客户"称谓的译文需按现行 tenant 模型口径复核。
```

- [ ] **Step 2: 全量终验**

```bash
cd frontend && npx tsc --noEmit && npm run lint; npm run build
```

预期:tsc exit 0;lint 错误数不高于任务开始前 baseline(house-style ~29 条不增);build 成功。再跑一遍 Task 1/3 的浏览器验收(双语切换 × 亮暗主题 × admin/dashboard/sales/login 四区),四象限都正常。

- [ ] **Step 3: 提交**

```bash
cd /var/klwa && git add docs/superpowers/reference && git commit -m "docs(i18n): archive translation memory from feat/frontend-i18n-theme for P1b rollout"
```

---

## 计划外(明确不做,防散焦)

- admin/dashboard/sales 各页面**正文**文案抽取(~436+ CJK 行)→ P1b 单独计划,用 Task 8 的翻译记忆。
- 批量操作的**后端端点**与各表接线 → P4。
- landing 营销页配色/文案 → 不在本次范围。
- `feat/app-i18n-ssr`、`feat/frontend-i18n-theme`、`feat/whatsapp-qr-pairing-wt` 分支清理 → 换皮验收后另行处置(sp9 的自助改密 2 提交记为 P4 候选)。
