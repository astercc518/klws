# 模块二 · 客户资源与分群 — 第一切片设计（联系人库 · 数据面无关）

**日期**: 2026-07-12
**分支**: `feat/contacts-audience`
**状态**: 设计已获用户认可，待评审 → writing-plans

## 背景与定位

现状：群发收件人是"每次群发粘一坨手机号"（`handleCreateCampaign` 收 `req.Phones []string`），**无可复用的联系人库、无标签分群、无联系人级去重**。黑名单/退订的*执行*已存在（`suppression_list` 表 + 分发器按 `phone_bidx` 的退订栅栏），但缺*管理*入口。

本切片建一个**可复用的、按 tenant 轴的联系人资源库**：导入一次、复用多次、按分段建群发。

**归属**：客户自助为主（`/dashboard`，`mgr.WithTenant()` 走 RLS）+ 管理员跨租户只读旁观（`/admin`，`systemPool` 只读，照 `/admin/send-records` 范式）。

**红线**：不改 `internal/{billing,dispatch,store,sendgate,cluster}` 核心业务逻辑。只新增迁移、`internal/api` 层 handler/纯查询、一个纯叶子包 `internal/phonenorm`、前端。复用 `crypto.BlindIndex`、`mgr.WithTenant()`、既有 `suppression_list`。

## 目标 / 非目标

**目标（本切片）**
1. 联系人库 CRUD + 分页/筛选（标签/国家/状态）。
2. 批量导入（粘贴/CSV）：格式清洗（E.164）+ 批内去重 + 库内去重（按 `phone_bidx`）+ 导入报告（inserted/duplicates/invalid）。
3. CSV 导出（带筛选）。
4. 多维标签：标签 CRUD + 批量打标/取消。
5. 分段：命名的保存筛选（动态解析），预览计数。
6. 黑名单/退订**手动**管理：`suppression_list` 的 list/add/remove/import。
7. 从分段一键建群发：`handleCreateCampaign` 增加可选 `segment_id`，服务端解析成 phones，复用现有插入路径；`phones[]` 老路保留、行为不变。

**非目标（留第二切片，依赖 Evolution 数据面真机验证）**
- 号码开通状态查号（WhatsApp-registered check，需活实例 `/chat/whatsappNumbers`）。
- 入站 `STOP`/退订关键词自动捕获（需入站 `messages.upsert`）。
- AI 相关（模块四/五）。

## 数据模型（新迁移 `0021_contacts.sql`）

RLS 一律复刻 `0008_security.sql`：`ALTER TABLE ... ENABLE/FORCE ROW LEVEL SECURITY`，policy `USING (tenant_id = current_setting('app.current_tenant_id')::bigint)` + 同款 `WITH CHECK`；`GRANT` 给 `app_tenant, app_system`；`updated_at` 用现有 `touch_updated_at()` 触发器。

```sql
DO $$ BEGIN CREATE TYPE contact_status_t AS ENUM ('active','unsubscribed','invalid');
  EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE TABLE contacts (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id    BIGINT NOT NULL,
  phone        TEXT   NOT NULL,          -- E.164, 明文（见"号码存储取舍"）
  phone_bidx   BYTEA  NOT NULL,          -- crypto.BlindIndex，去重 + 退订匹配
  country_code CHAR(2),
  display_name TEXT,
  status       contact_status_t NOT NULL DEFAULT 'active',
  source       TEXT,                     -- 'import:<batch>' | 'manual' | 'api'
  vars         JSONB NOT NULL DEFAULT '{}',  -- 动态变量替换（接 spintax 变量）
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, phone_bidx)          -- 租户内去重
);

CREATE TABLE contact_tags (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id BIGINT NOT NULL,
  name TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

CREATE TABLE contact_tag_map (
  tenant_id  BIGINT NOT NULL,
  contact_id BIGINT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  tag_id     BIGINT NOT NULL REFERENCES contact_tags(id) ON DELETE CASCADE,
  PRIMARY KEY (contact_id, tag_id)
);

CREATE TABLE contact_segments (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id BIGINT NOT NULL,
  name TEXT NOT NULL,
  filter JSONB NOT NULL DEFAULT '{}',    -- {tags:[id..], country, status}
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

CREATE TABLE contact_import_batches (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id BIGINT NOT NULL,
  filename TEXT,
  total INT NOT NULL DEFAULT 0,
  inserted INT NOT NULL DEFAULT 0,
  duplicates INT NOT NULL DEFAULT 0,
  invalid INT NOT NULL DEFAULT 0,
  actor_id BIGINT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_contacts_tenant_status ON contacts (tenant_id, status);
```

**复用**：`suppression_list (tenant_id, phone_bidx, reason, added_at)` 已存在，不改结构，只加管理端点。

### 号码存储取舍（已与用户确认：走明文）

`contacts.phone` 存 E.164 明文 + `phone_bidx` 盲索引，与现有 `campaign_recipients`（现库即明文 phone）一致。at-rest 加密是另一桩未完成的 M11 全表迁移，本切片不给单表半吊子加密。若未来做统一 PII 加密，contacts 随全表一起迁 `phone_enc`+每租户 DEK。

## 组件（隔离 / 可测）

- **`internal/phonenorm`（新叶子包，纯函数）**：`Normalize(raw, defaultCountry) (e164 string, country string, ok bool)`。去非数字 → 有 `+`/国码则用之，否则按 `defaultCountry` 补 → 按国家号长范围校验。表驱动单测。精度到 libphonenumber 列为后续升级（本切片覆盖主要国码即可，非法号归 invalid 不阻断整批）。
- **`internal/api/contacts.go`**：客户 + 管理员 handler。
- **`internal/api/contacts_query.go`（纯）**：`buildContactWhere(filter)`、`segmentToWhere(filter)`、`dedupeByBidx(...)`，照 `finance_query.go`/`resources_query.go` 既有纯查询范式，独立单测。
- 隔离经 `mgr.WithTenant()`（客户）/ `s.systemPool()` 只读（管理员）。审计写复用 `internal/api/audit.go`（导入、批量打标、黑名单增删、分段删）。

## 接口

**客户 `/api/v1`（requireAuth + customer/tenant）**
```
GET    /contacts                 分页/筛选(tag,country,status,q) -> {rows,total,stats}
POST   /contacts                 单条新增
PUT    /contacts/:id             编辑(name/status/vars/tags)
DELETE /contacts/:id             删除
POST   /contacts/import          批量导入(粘贴/CSV)-> {batch_id,inserted,duplicates,invalid}
GET    /contacts/export          CSV 导出(带筛选)
GET    /contacts/tags            列标签
POST   /contacts/tags            建标签
DELETE /contacts/tags/:id        删标签(级联解绑)
POST   /contacts/tags/:id/apply  批量打标/取消 {contact_ids[], remove?}
GET    /contacts/segments        列分段
POST   /contacts/segments        建分段
DELETE /contacts/segments/:id    删分段
GET    /contacts/segments/:id/preview  预览计数
GET    /suppression              列黑名单(分页)
POST   /suppression              加(单/批, phone->bidx)
DELETE /suppression/:id          移除
POST   /suppression/import       批量导入黑名单
```

**建群发扩展**：`POST /campaigns` body 增加可选 `segment_id`（与 `phones[]` 二选一）。服务端在同一 RLS 事务里 `segmentToWhere` 解析出 phones（并**排除 suppression**、排除 `status!='active'`），走现有 template+campaign+recipients 插入。老 `phones[]` 路径与行为完全不变。

**管理员 `/admin`（只读、跨租户、systemPool）**
```
GET /admin/contacts     跨租户联系人只读(buildContactWhere + tenant_id 筛选, {rows,total,stats})
```

## 流程 · 导入

粘贴文本或 CSV → 按行/列解析 → 逐条 `phonenorm.Normalize`（非法 → invalid 计数，跳过）→ 批内按 bidx 去重（duplicates 计数）→ 单事务 `INSERT ... ON CONFLICT (tenant_id, phone_bidx) DO NOTHING`（库内重复 → duplicates）→ 写 `contact_import_batches` 计数 → 返回报告。部分失败不整批回滚（报告式，照现有导入设备/代理范式）。

## 错误处理

- RLS/余额/409 重邮同现有范式。
- 建群发-从分段：分段解析后**人群为空** → 400「分段无可发送联系人」；不建空 campaign。
- 导入超大 body → 复用现有 `MaxBytesReader` 上限。
- 黑名单/联系人删除 → 自动解依赖不阻断（house guard 风格：删标签级联解绑并提示影响数，不阻断）。

## 测试

- **纯单测**：`phonenorm`（表驱动，多国码 + 非法）；`buildContactWhere`/`segmentToWhere`/`dedupeByBidx`。
- **真库（testcontainers，照 `crud_db_test.go`/`finance_db_test.go`）**：联系人 CRUD、导入去重(批内+库内)+报告计数、标签打标/级联删、分段解析+preview、黑名单增删、**从分段建群发**(排除 suppression+非 active、空人群 400)、RLS 隔离(跨租户不可见)。
- **前端**：`npm run build` + eslint（仓库无 JS 测试框架；house-style lint baseline 不回归）。

## 前端（`/dashboard` 客户自助）

新"联系人"区：列表(`ProDataTable` server 模式，分页 + tag/country/status 筛选 + 手机号搜索)、导入弹框(粘贴/上传 + 报告)、标签管理、分段构建器(选 tag/country/status → 存 + preview 计数)、黑名单 Tab。建群发页加"从分段选择"（与手动粘号二选一）。管理员侧跨租户只读列表薄挂 `/admin`（可与本切片同交付或紧随）。

## 交付切分（给 writing-plans 的种子）

1. 迁移 `0021_contacts.sql` + RLS。
2. `internal/phonenorm` 叶子包 + 单测。
3. `contacts_query.go` 纯查询 + 单测。
4. 联系人 CRUD + 分页/筛选 + 导入/导出（handler + 真库测试）。
5. 标签 + 分段（handler + 真库测试）。
6. 黑名单管理（handler + 真库测试）。
7. 建群发-从分段扩展（改 `campaign.go`，真库测试；老路回归）。
8. 前端 `/dashboard` 联系人区 + 建群发-从分段。
9. 管理员只读列表。

## 未决 / 假设

- 无阻塞未决项。假设：分段为动态筛选（非物化）；`display_name` 明文；号码明文（已确认）；管理员只读不可编辑客户联系人（治理边界）。
