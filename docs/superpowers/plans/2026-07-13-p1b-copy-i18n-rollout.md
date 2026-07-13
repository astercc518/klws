# P1b 页面正文文案双语铺开 — 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 admin/dashboard/sales/auth 四区全部页面正文的硬编码中文抽入 TS 字典,完成 spec §8"两份字典覆盖全部导航与主要页面文案"的验收;landing 不动。

**Architecture:** 沿用 P1 已落地的 cookie+SSR 机制(`useT`/`useLocale` 来自 `@/components/locale-provider`)。字典按域拆新文件(`admin.ts`/`dashboard.ts`/`sales.ts`/`auth.ts`)注册进 `frontend/lib/i18n/dicts/index.ts` 的 `namespaces` 数组。服务端组件(各 `page.tsx`)的标题走 `PageHeader` 已支持的 `I18nText`(`{zh,en}`)记录,不需要 useT。英文译文优先取自翻译记忆 `docs/superpowers/reference/i18n-translation-memory.ts.txt`(注意:它基于废弃的 customer 模型,涉及"客户/租户"称谓按现行口径改)。

**Tech Stack:** 同 P1(Next.js 16 魔改版、React 19、TS 严格模式)。

**Plan 基线:** main @ fe2ddef(P1 已合并)。

## Global Constraints

- **landing 不动**(`components/landing/*`、`app/page.tsx`、`app/en/*` 有独立 URL 式双语)。`components/admin/nav.ts` 已双语(label+en),不动。
- **中文注释保留**——只翻译**运行时可见文案**:JSX 文本、字符串 prop(placeholder/aria-label/title)、toast 消息、confirm 文案、下拉选项标签、空态/错误文案。代码注释、console 内部日志不翻。
- **键名规范**:点分命名空间 `<域>.<组件缩写>.<语义>`,如 `admin.devices.bindProxy`、`dash.contacts.importTitle`、`auth.login.submit`。同一文案跨组件复用时提升到 `common.ts`。
- **动态插值**:沿用既有 `{x}` token + `.replace("{x}", () => value)` **函数形式**(防 `$` 展开,P1 终审教训)。
- **zh/en 必须成对**;en 优先取翻译记忆,没有的自己译(简洁、面向运营人员的英文)。
- **状态/枚举映射**(如 state→中文标签的 Record)改为 state→key→t()。**金额/数字/日期格式不动**。
- 每任务门槛:`cd frontend && npx tsc --noEmit`(exit 0)+ `node scripts/check-i18n-keys.mjs`(T1 引入,0 missing)+ 改动文件无新 lint 错误 + 任务收尾 `npm run build` 成功。
- 每任务报告须附**残余中文清单**:`grep -nP '[\x{4e00}-\x{9fff}]' <本任务文件>` 的输出中,每一行要么是注释要么有明确保留理由(如品牌名);复审逐行核对该清单。
- 不碰 Go 代码。git 操作在**执行时的 worktree 根**进行(不要照抄本文件中的绝对路径)。

---

### Task 1: 字典架构 + 键校验脚本 + 服务端 page.tsx 标题迁移

**Files:**
- Create: `frontend/lib/i18n/dicts/admin.ts`、`dashboard.ts`、`sales.ts`、`auth.ts`(先建骨架:`export const admin: Record<Locale, Record<string,string>> = { zh: {}, en: {} }`,后续任务填充)
- Modify: `frontend/lib/i18n/dicts/index.ts`(namespaces 数组注册四个新命名空间)
- Create: `frontend/scripts/check-i18n-keys.mjs`(键存在性校验)
- Modify: 全部含中文 title/description 的 `app/admin/**/page.tsx`、`app/dashboard/**/page.tsx`、`app/sales/**/page.tsx`(约 15 个,以 grep 实际命中为准)——`PageHeader` 的 `title`/`description` 传 `{zh:"…", en:"…"}` 记录(`I18nText`,P1 已支持 `string | I18nText`);`eyebrow` 缩写(IAM/OPS 等)不翻。

**Interfaces:**
- Produces:四个空字典命名空间(后续任务往里填);校验脚本 `node scripts/check-i18n-keys.mjs`——扫描 `components/**/*.tsx`、`app/**/*.tsx` 中 `t("…")` 字面量键,比对 `lib/i18n/dicts/*.ts` 中 `"key":` 定义,zh/en 缺一即非零退出并列出;供所有后续任务当门槛。

- [ ] **Step 1: 建四个字典骨架 + index.ts 注册**(照 common.ts 的类型与导出风格)
- [ ] **Step 2: 写 check-i18n-keys.mjs**

```js
#!/usr/bin/env node
// 扫源码里的 t("key") 字面量,核对每个 key 在合并字典 zh/en 两侧都有定义。
// 用正则而非 import:字典是 TS 模块,直接文本抽取 "key": 定义即可。
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const SRC_DIRS = ["components", "app"];
const DICT_DIR = "lib/i18n/dicts";

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.(tsx|ts)$/.test(name)) out.push(p);
  }
  return out;
}

const used = new Set();
for (const dir of SRC_DIRS)
  for (const f of walk(dir))
    for (const m of readFileSync(f, "utf8").matchAll(/\bt\(\s*"([a-z0-9.]+)"\s*[),]/g))
      used.add(m[1]);

const defined = { zh: new Set(), en: new Set() };
for (const f of walk(DICT_DIR)) {
  const src = readFileSync(f, "utf8");
  // 每个文件里 zh 块在 en 块之前(与 common.ts 一致的约定)
  const zhPart = src.slice(src.indexOf("zh:"), src.indexOf("en:"));
  const enPart = src.slice(src.indexOf("en:"));
  for (const m of zhPart.matchAll(/"([a-z0-9.]+)":/g)) defined.zh.add(m[1]);
  for (const m of enPart.matchAll(/"([a-z0-9.]+)":/g)) defined.en.add(m[1]);
}

let bad = 0;
for (const k of [...used].sort()) {
  const missZh = !defined.zh.has(k), missEn = !defined.en.has(k);
  if (missZh || missEn) { bad++; console.error(`MISSING ${k}${missZh ? " [zh]" : ""}${missEn ? " [en]" : ""}`); }
}
console.log(`${used.size} keys used, ${bad} missing`);
process.exit(bad ? 1 : 0);
```

- [ ] **Step 3: 跑一遍脚本**——预期 0 missing(P1 的 27 个键全绿),坏则修脚本
- [ ] **Step 4: page.tsx 标题迁移**:`grep -rlP '[\x{4e00}-\x{9fff}]' app/admin app/dashboard app/sales --include=page.tsx` 逐个把 PageHeader 的 title/description 改 `{zh,en}`;dashboard/sales 若用别的头部组件,同理(该组件若只收 string,升级为 `string | I18nText` + `pick()`,照 page-header.tsx 的 P1 改法)
- [ ] **Step 5: tsc + check-i18n-keys + build + 残余清单 + 提交** `feat(i18n): dict scaffolding + key checker + bilingual page titles`

---

### Task 2: admin 资源域(设备/代理/大盘)

**Files:** Modify: `components/admin-devices.tsx`(82 行中文)、`components/admin-proxies.tsx`(48)、`components/admin-metrics.tsx`(10);字典填 `dicts/admin.ts`

- [ ] Step 1: 逐文件抽取→`admin.devices.*`/`admin.proxies.*`/`admin.metrics.*` 键(客户端组件加 `const t = useT()`;列定义若在组件体外的模块级常量,移进组件体或改为接收 t 的工厂函数——保持最小改动,选前者)
- [ ] Step 2: 门槛四件套 + 残余中文清单
- [ ] Step 3: 提交 `feat(i18n): admin resources copy via dict (devices/proxies/metrics)`

### Task 3: admin 客户与财务域

**Files:** Modify: `components/admin-users-table.tsx`(70)、`components/admin-user-dialogs.tsx`(26)、`components/admin-billing.tsx`(27)、`components/admin-ledger.tsx`(24)、`components/customer-ledger-sheet.tsx`(10);字典 `admin.users.*`/`admin.billing.*`/`admin.ledger.*`

- [ ] Step 1: 抽取替换(注意 users-table 里的角色 Tab、佣金弹框、挂起确认等 confirm/toast 文案)
- [ ] Step 2: 门槛四件套 + 残余清单;Step 3: 提交 `feat(i18n): admin customers & finance copy via dict`

### Task 4: admin 发送中心/风控/审计 + 共享详情面板

**Files:** Modify: `components/admin-campaigns.tsx`(31)、`components/admin-send-records.tsx`(18)、`components/admin-contacts.tsx`(18)、`components/admin-risk-settings.tsx`(42)、`components/admin-audit-log.tsx`、`components/campaign-detail-sheet.tsx`(33,admin/dashboard 共用→键放 `common.campaign.*`)

- [ ] Step 1: 抽取替换;Step 2: 门槛四件套 + 残余清单;Step 3: 提交 `feat(i18n): admin sending/risk/audit + campaign sheet copy via dict`

### Task 5: 代理分销(admin)+ 销售端

**Files:** Modify: `components/admin-agent-tree.tsx`(29)、`components/admin-agent-settlements.tsx`(30)、`components/admin-agent-cost-pricing.tsx`(16)、`components/sales-console.tsx`(21)、`components/sales/agent-downline.tsx`(22)、`components/sales/agent-statement.tsx`(14)、`components/sales/agent-allocate-dialog.tsx`(10);字典 `admin.agents.*` 填 admin.ts、`sales.*` 填 sales.ts

- [ ] Step 1: 抽取替换(结算/划拨的金额语义文案照直译,币种符号不动);Step 2: 门槛四件套 + 残余清单;Step 3: 提交 `feat(i18n): agent distribution + sales console copy via dict`

### Task 6: 客户端 dashboard

**Files:** Modify: `components/dashboard/contacts-list.tsx`(50)、`components/dashboard/contacts-tags-segments.tsx`(37)、`components/dashboard/suppression-list.tsx`(24)、`components/dashboard/contacts-import-dialog.tsx`(18)、`components/customer-campaigns.tsx`(16)、`components/new-campaign-dialog.tsx`(24)、`components/dashboard-metrics.tsx`、`components/header.tsx`(剩余 MOCK 状态文案);字典填 dashboard.ts(键 `dash.*`)

- [ ] Step 1: 抽取替换(导入报告 inserted/duplicates/invalid 的插值用 {n} 函数式 replace);Step 2: 门槛四件套 + 残余清单;Step 3: 提交 `feat(i18n): customer dashboard copy via dict`

### Task 7: auth 四页 + 拟真面板

**Files:** Modify: `app/login/page.tsx`(17)、`app/register/page.tsx`(20)、`app/forgot-password/page.tsx`(12)、`app/reset-password/page.tsx`(17)、`components/auth/auth-shell.tsx`(正文文案)、`components/auth/broadcast-demo.tsx`(12,拟真聊天内容也翻——英文访客应看到英文 demo);字典填 auth.ts(键 `auth.*`)

- [ ] Step 1: 注意这些 page.tsx 是否服务端组件:登录表单均是 "use client"(有 Turnstile/状态),直接 useT;若个别是服务端,标题走 I18nText + `getLocale()`。表单校验错误消息(如"至少 6 位")一并入字典。
- [ ] Step 2: 门槛四件套 + 残余清单;Step 3: 提交 `feat(i18n): auth pages + broadcast demo copy via dict`

### Task 8: 全量终验 + /en landing lang 修正

**Files:** Modify: `app/en/layout.tsx` 或 landing 相关(仅此一处例外):P1 终审 Minor——`/en` 页 `<html lang>` 跟随 cookie 与内容错轨。**最小修**:在 `app/en/` 的 layout(若无则建)导出强制 `lang="en"` 不可行(html 在根布局)→ 改为根布局 `getLocale()` 之后:pathname 无法在服务端布局拿到,故**接受现状,仅在 spec 记档**(已记)。本任务实际动作:确认无解后跳过,不 hack。
- [ ] Step 1: 全量 `grep -rnP '[\x{4e00}-\x{9fff}]' components app --include=*.tsx --include=*.ts | grep -v "components/landing\|app/en\|app/page.tsx\|nav.ts"`,产出最终残余清单,逐行分类(注释/品牌名/遗漏),遗漏归零
- [ ] Step 2: `npx tsc --noEmit` + `node scripts/check-i18n-keys.mjs` + `npm run lint`(错误数 = 46 baseline)+ `npm run build`
- [ ] Step 3: 浏览器四象限冒烟(控制器执行):login/register 中英×亮暗;en 下无中文残留(拟真面板除品牌名)
- [ ] Step 4: 提交 `chore(i18n): final sweep — zero untranslated copy outside landing`

---

## 计划外(明确不做)

- landing/`app/en` 双语体系重构;`/en` 的 html lang 错轨(已记档 spec)。
- 新增语言(仅 zh/en)。
- ICU 复数/日期本地化(现有文案无需求)。
