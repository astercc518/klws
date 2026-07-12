# 模块二 · 联系人库(第一切片) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建一个按 tenant 轴、可复用的联系人资源库（导入清洗去重 + 标签分群 + 黑名单管理 + 从分段建群发），全部数据面无关。

**Architecture:** 新迁移 `0021_contacts.sql`（RLS 复刻 0020）+ 纯叶子包 `internal/phonenorm` + `internal/api` 层 handler/纯查询。客户走 `mgr.WithTenant()`（RLS），管理员只读走 `s.systemPool()`。复用 `crypto.BlindIndex`、既有 `suppression_list`、审计 `audit.AuditWriter`。

**Tech Stack:** Go 1.23 + pgx v5 + gin + testcontainers-go(postgres:16) + Next.js(前端 build/lint)。

## Global Constraints

- **红线**：禁改 `internal/{billing,dispatch,store,sendgate,cluster}` 核心业务逻辑。只新增迁移、`internal/phonenorm`、`internal/api` 层、前端。允许在 api 层**调用** `mgr.WithTenant`/`systemPool`/`crypto.BlindIndex`/`audit`。
- **RLS 范式（每张新表逐字照 0020）**：`ENABLE` + `FORCE ROW LEVEL SECURITY`；`CREATE POLICY tenant_isolation ... USING (tenant_id = current_setting('app.current_tenant_id', true)::bigint) WITH CHECK (同)`。迁移 replay-all 幂等（`CREATE TABLE IF NOT EXISTS`、`DO $$ ... EXCEPTION WHEN duplicate_object`）。
- **盲索引**：`crypto.BlindIndex(key []byte, value string) []byte`；key = `s.deps.BlindKey`（32 字节，未配置时 handler fail-closed `503`）。去重/退订匹配一律按 `phone_bidx`，不按明文。
- **号码明文**：`contacts.phone` 存 E.164 明文 + `phone_bidx`（与现有 `campaign_recipients` 一致；at-rest 加密是独立 M11 全表迁移，本切片不做）。
- **租户轴**：一切按 `tenant_id`；客户 handler 用 `tenantID(c)` 取 session 租户 + `mgr.WithTenant(ctx, tid)`。
- **审计**：导入、批量打标、黑名单增删、分段/标签删 写 `audit_log`，复用 `s.deps.Audit`（照 `internal/api/audit.go`）。密码/PII 不入审计 details。
- **前端无 JS 测试框架**：前端任务的"测试"= `npm run build` + `eslint`，house-style lint baseline 不回归。

---

## File Structure

- Create `migrations/0021_contacts.sql` — contacts/tags/tag_map/segments/import_batches 表 + RLS。
- Create `internal/phonenorm/phonenorm.go` + `phonenorm_test.go` — 纯号码清洗叶子包。
- Create `internal/api/contacts_query.go` + `contacts_query_test.go` — 纯 `buildContactWhere`/`segmentToWhere`/`dedupeByBidx`。
- Create `internal/api/contacts.go` — 联系人/标签/分段/黑名单 handler。
- Create `internal/api/campaign_from_segment.go` — 建群发-从分段的解析助手（避免膨胀 campaign.go）。
- Modify `internal/api/campaign.go` — `createCampaignRequest` 加 `SegmentID`，创建流程分叉。
- Modify `internal/api/router.go` — 注册客户 `/contacts*`/`/suppression*` + 管理员 `/admin/contacts`。
- Create `internal/api/contacts_db_test.go` — testcontainers 真库测试（照 `resources_db_test.go`）。
- Modify `frontend/lib/nav.ts`（客户导航如有）+ Create `frontend/app/dashboard/contacts/*` + 组件。

---

## Task 1: 迁移 0021 + schema 冒烟

**Files:**
- Create: `migrations/0021_contacts.sql`
- Test: `internal/api/contacts_db_test.go`（新建，先放 schema 冒烟）

**Interfaces:**
- Produces: 表 `contacts, contact_tags, contact_tag_map, contact_segments, contact_import_batches`（列见下），全 FORCE RLS。

- [ ] **Step 1: 写迁移**

`migrations/0021_contacts.sql`:
```sql
-- 0021: 联系人资源库（模块二切片一）。按 tenant 轴，FORCE RLS 复刻 0020。replay-all 幂等。
DO $$ BEGIN CREATE TYPE contact_status_t AS ENUM ('active','unsubscribed','invalid');
  EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE TABLE IF NOT EXISTS contacts (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id    BIGINT NOT NULL,
    phone        TEXT   NOT NULL,
    phone_bidx   BYTEA  NOT NULL,
    country_code CHAR(2),
    display_name TEXT,
    status       contact_status_t NOT NULL DEFAULT 'active',
    source       TEXT,
    vars         JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, phone_bidx)
);
CREATE INDEX IF NOT EXISTS idx_contacts_tenant_status ON contacts (tenant_id, status);

CREATE TABLE IF NOT EXISTS contact_tags (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS contact_tag_map (
    tenant_id  BIGINT NOT NULL,
    contact_id BIGINT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    tag_id     BIGINT NOT NULL REFERENCES contact_tags(id) ON DELETE CASCADE,
    PRIMARY KEY (contact_id, tag_id)
);

CREATE TABLE IF NOT EXISTS contact_segments (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    name TEXT NOT NULL,
    filter JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS contact_import_batches (
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

DROP TRIGGER IF EXISTS trg_contacts_touch ON contacts;
CREATE TRIGGER trg_contacts_touch BEFORE UPDATE ON contacts
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- FORCE RLS on every tenant-scoped table (policy verbatim from 0020).
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['contacts','contact_tags','contact_tag_map','contact_segments','contact_import_batches']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format($f$CREATE POLICY tenant_isolation ON %I
      USING (tenant_id = current_setting('app.current_tenant_id', true)::bigint)
      WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::bigint)$f$, t);
  END LOOP;
END $$;
```

- [ ] **Step 2: 写 schema 冒烟测试**

`internal/api/contacts_db_test.go`:
```go
package api

import (
	"context"
	"testing"
)

func TestContactsSchemaApplies(t *testing.T) {
	pool := testPool(t) // applies all migrations incl. 0021
	ctx := context.Background()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_name IN ('contacts','contact_tags','contact_tag_map','contact_segments','contact_import_batches')`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 5 {
		t.Fatalf("want 5 contact tables, got %d", n)
	}
}
```

- [ ] **Step 3: 跑测试确认通过（迁移可 apply）**

Run: `go test ./internal/api/ -run TestContactsSchemaApplies -count=1`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add migrations/0021_contacts.sql internal/api/contacts_db_test.go
git commit -m "feat(contacts): 0021 schema + RLS (tenant-scoped contact library)"
```

---

## Task 2: `internal/phonenorm` 号码清洗叶子包

**Files:**
- Create: `internal/phonenorm/phonenorm.go`
- Test: `internal/phonenorm/phonenorm_test.go`

**Interfaces:**
- Produces: `func Normalize(raw, defaultCountry string) (e164 string, country string, ok bool)` — 去非数字；`+`/国码开头则用之，否则按 `defaultCountry`(ISO alpha-2) 的国际拨号码补齐；按国长范围粗校验；`ok=false` = 非法（调用方计 invalid）。

- [ ] **Step 1: 写失败测试**

`internal/phonenorm/phonenorm_test.go`:
```go
package phonenorm

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		raw, def   string
		wantE164   string
		wantCC     string
		ok         bool
	}{
		{"+1 (415) 555-2671", "", "+14155552671", "US", true},
		{"4155552671", "US", "+14155552671", "US", true},   // 补美国码
		{"08613800138000", "CN", "+8613800138000", "CN", true}, // 去前导0/国际前缀
		{"13800138000", "CN", "+8613800138000", "CN", true},
		{"555", "US", "", "", false},                        // 太短 -> 非法
		{"not-a-number", "US", "", "", false},
		{"", "US", "", "", false},
	}
	for _, c := range cases {
		e, cc, ok := Normalize(c.raw, c.def)
		if ok != c.ok || (ok && (e != c.wantE164 || cc != c.wantCC)) {
			t.Fatalf("Normalize(%q,%q)=(%q,%q,%v) want (%q,%q,%v)", c.raw, c.def, e, cc, ok, c.wantE164, c.wantCC, c.ok)
		}
	}
}
```

- [ ] **Step 2: 跑确认失败**

Run: `go test ./internal/phonenorm/ -run TestNormalize -count=1`
Expected: FAIL（包不存在）

- [ ] **Step 3: 实现**

`internal/phonenorm/phonenorm.go`:
```go
// Package phonenorm normalizes raw phone strings to E.164. It is a pragmatic
// normalizer covering the country codes we sell into; unknown/short inputs are
// rejected (ok=false) so the caller can count them as invalid without aborting a
// whole import. libphonenumber-grade validation is a future upgrade.
package phonenorm

import "strings"

// callingCode maps ISO alpha-2 -> {international dialing code, national number
// length range [min,max] excluding the country code}.
type ccInfo struct {
	code     string
	min, max int
}

var byCountry = map[string]ccInfo{
	"US": {"1", 10, 10},
	"CA": {"1", 10, 10},
	"CN": {"86", 11, 11},
	"GB": {"44", 9, 10},
	"IN": {"91", 10, 10},
	"BR": {"55", 10, 11},
	"ID": {"62", 9, 12},
	"NG": {"234", 7, 11},
	"MX": {"52", 10, 10},
	"PH": {"63", 10, 10},
}

// byPrefix lets us detect the country when the raw string already carries a
// calling code (longest prefix wins).
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteByte(byte(r))
		}
	}
	return b.String()
}

func Normalize(raw, defaultCountry string) (string, string, bool) {
	hadPlus := strings.HasPrefix(strings.TrimSpace(raw), "+")
	d := digitsOnly(raw)
	if d == "" {
		return "", "", false
	}
	// Strip a leading international access prefix "00" (e.g. 008613... ).
	if strings.HasPrefix(d, "00") {
		d = strings.TrimPrefix(d, "00")
		hadPlus = true
	}

	// 1) If it starts with a known calling code, trust it.
	if hadPlus || len(d) > 11 {
		for cc, info := range byCountry {
			if strings.HasPrefix(d, info.code) {
				nat := strings.TrimPrefix(d, info.code)
				nat = strings.TrimPrefix(nat, "0") // national trunk 0
				if len(nat) >= info.min && len(nat) <= info.max {
					return "+" + info.code + nat, cc, true
				}
			}
		}
	}

	// 2) Otherwise treat it as a national number for defaultCountry.
	info, known := byCountry[defaultCountry]
	if !known {
		return "", "", false
	}
	nat := strings.TrimPrefix(d, "0")
	// If it already includes the country code (no plus), strip it.
	if strings.HasPrefix(nat, info.code) && len(nat)-len(info.code) >= info.min {
		nat = strings.TrimPrefix(nat, info.code)
		nat = strings.TrimPrefix(nat, "0")
	}
	if len(nat) < info.min || len(nat) > info.max {
		return "", "", false
	}
	return "+" + info.code + nat, defaultCountry, true
}
```

- [ ] **Step 4: 跑确认通过**

Run: `go test ./internal/phonenorm/ -run TestNormalize -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/phonenorm/
git commit -m "feat(phonenorm): E.164 normalizer leaf package"
```

---

## Task 3: 纯查询构建器 `contacts_query.go`

**Files:**
- Create: `internal/api/contacts_query.go`
- Test: `internal/api/contacts_query_test.go`

**Interfaces:**
- Consumes: `clampPage`（已存在于 resources_query.go）。
- Produces:
  - `type contactFilter struct { Q string; Country string; Status string; TagID int64; Limit, Offset int }`
  - `func buildContactWhere(f contactFilter) (string, []any)` — 返回 `" WHERE ..."`(或"") + 位置参数，列不带别名（表别名 `c` 由调用方拼，见下）。约定：调用方 SQL 为 `FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id`，故 TagID 过滤用 `m.tag_id`。
  - `type segmentFilter struct { Tags []int64; Country string; Status string }`
  - `func segmentToWhere(f segmentFilter) (string, []any)` — 解析分段 filter 成 WHERE（同表别名约定；多标签 = `m.tag_id = ANY($n)`）。

- [ ] **Step 1: 写失败测试**

`internal/api/contacts_query_test.go`:
```go
package api

import (
	"strings"
	"testing"
)

func TestBuildContactWhere(t *testing.T) {
	where, args := buildContactWhere(contactFilter{Q: "138", Country: "CN", Status: "active", TagID: 7})
	for _, want := range []string{"c.phone ILIKE", "c.country_code = $", "c.status::text = $", "m.tag_id = $"} {
		if !strings.Contains(where, want) {
			t.Fatalf("where %q missing %q", where, want)
		}
	}
	if len(args) != 4 {
		t.Fatalf("want 4 args, got %d (%v)", len(args), args)
	}
}

func TestBuildContactWhere_Empty(t *testing.T) {
	where, args := buildContactWhere(contactFilter{})
	if where != "" || len(args) != 0 {
		t.Fatalf("empty filter -> %q %v", where, args)
	}
}

func TestSegmentToWhere_Tags(t *testing.T) {
	where, args := segmentToWhere(segmentFilter{Tags: []int64{1, 2}, Status: "active"})
	if !strings.Contains(where, "m.tag_id = ANY($1)") || !strings.Contains(where, "c.status::text = $2") {
		t.Fatalf("where=%q", where)
	}
	if len(args) != 2 {
		t.Fatalf("args=%v", args)
	}
}
```

- [ ] **Step 2: 跑确认失败**

Run: `go test ./internal/api/ -run 'BuildContactWhere|SegmentToWhere' -count=1`
Expected: FAIL（未定义）

- [ ] **Step 3: 实现**

`internal/api/contacts_query.go`:
```go
package api

import (
	"fmt"
	"strings"
)

type contactFilter struct {
	Q       string
	Country string
	Status  string
	TagID   int64
	Limit   int
	Offset  int
}

// buildContactWhere builds the WHERE clause for a query shaped as
//   FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id = c.id
func buildContactWhere(f contactFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Q != "" {
		args = append(args, "%"+f.Q+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf("(c.phone ILIKE $%d OR c.display_name ILIKE $%d)", n, n))
	}
	if f.Country != "" {
		args = append(args, f.Country)
		conds = append(conds, fmt.Sprintf("c.country_code = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("c.status::text = $%d", len(args)))
	}
	if f.TagID != 0 {
		args = append(args, f.TagID)
		conds = append(conds, fmt.Sprintf("m.tag_id = $%d", len(args)))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

type segmentFilter struct {
	Tags    []int64 `json:"tags"`
	Country string  `json:"country"`
	Status  string  `json:"status"`
}

// segmentToWhere resolves a saved segment filter into a WHERE clause over the
// same (contacts c LEFT JOIN contact_tag_map m) shape.
func segmentToWhere(f segmentFilter) (string, []any) {
	var conds []string
	var args []any
	if len(f.Tags) > 0 {
		args = append(args, f.Tags)
		conds = append(conds, fmt.Sprintf("m.tag_id = ANY($%d)", len(args)))
	}
	if f.Country != "" {
		args = append(args, f.Country)
		conds = append(conds, fmt.Sprintf("c.country_code = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("c.status::text = $%d", len(args)))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
```

- [ ] **Step 4: 跑确认通过**

Run: `go test ./internal/api/ -run 'BuildContactWhere|SegmentToWhere' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/contacts_query.go internal/api/contacts_query_test.go
git commit -m "feat(contacts): pure where-builders for list + segment"
```

---

## Task 4: 联系人 CRUD + 分页/筛选 + 导入 + 导出

**Files:**
- Create: `internal/api/contacts.go`
- Modify: `internal/api/router.go`（注册 `/contacts*`）
- Test: `internal/api/contacts_db_test.go`（追加）

**Interfaces:**
- Consumes: `tenantID(c)`、`s.deps.Mgr.WithTenant(ctx,tid)`、`s.deps.BlindKey`、`crypto.BlindIndex`、`phonenorm.Normalize`、`buildContactWhere`、`clampPage`、`fail(c,code,msg)`、`s.deps.Audit`、`writeCSV`/`csvSanitize`（已存在于 csv.go）。
- Produces: handlers `handleListContacts, handleCreateContact, handleUpdateContact, handleDeleteContact, handleImportContacts, handleExportContacts`。

- [ ] **Step 1: 写失败测试（导入去重 + 列表）**

追加到 `internal/api/contacts_db_test.go`:
```go
// newContactServer wires a Server with a real Manager (WithTenant/RLS) + BlindKey + Audit.
func newContactServer(t *testing.T) (*Server, *store.Manager, int64) {
	t.Helper()
	ctx := context.Background()
	dsn := testDSN(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil { t.Fatalf("pool: %v", err) }
	t.Cleanup(pool.Close)
	applyAllMigrations(t, ctx, pool)
	rdb := newTestRedis(t)
	mgr, err := store.NewManager(ctx, store.Config{DSN: dsn, Redis: rdb, NodeID: "contacts-test", BadgerDir: t.TempDir()}, wlog.Noop)
	if err != nil { t.Fatalf("NewManager: %v", err) }
	t.Cleanup(mgr.Close)
	// seed a tenant to own the contacts
	var tid int64
	if err := pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('acme') RETURNING id`).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	key := make([]byte, 32)
	s := &Server{sysPool: pool, deps: Deps{Mgr: mgr, BlindKey: key, Audit: audit.NewAuditWriter(pool)}}
	return s, mgr, tid
}

// withTenant sets the session tenant on a test gin context.
func ctxWithTenant(tid int64) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("tenant_id", tid) // matches tenantID(c) key; verify against tenant.go
	return c
}

func TestImportContacts_Dedup(t *testing.T) {
	s, _, tid := newContactServer(t)
	// import 3 lines, two of which normalize to the same number
	body := `{"country":"CN","text":"13800138000\n13800138000\n555"}`
	w := doJSONTenant(t, s, s.handleImportContacts, http.MethodPost, "/contacts/import", tid, body)
	if w.Code != http.StatusOK {
		t.Fatalf("import status %d: %s", w.Code, w.Body.String())
	}
	// expect inserted=1, duplicates=1, invalid=1
	if !strings.Contains(w.Body.String(), `"inserted":1`) ||
		!strings.Contains(w.Body.String(), `"duplicates":1`) ||
		!strings.Contains(w.Body.String(), `"invalid":1`) {
		t.Fatalf("report=%s", w.Body.String())
	}
}
```

> **Note:** `doJSONTenant` is a small helper mirroring `doJSON` but calling `c.Set("tenant_id", tid)` before the handler. If `tenant.go`'s `tenantID(c)` reads a different context key, use that key. Add `doJSONTenant` to `contacts_db_test.go`.

- [ ] **Step 2: 跑确认失败**

Run: `go test ./internal/api/ -run TestImportContacts_Dedup -count=1`
Expected: FAIL（handler 未定义）

- [ ] **Step 3: 实现 handler**

`internal/api/contacts.go`（核心 import + list + CRUD；每个 handler 完整代码）:
```go
package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/acme/wadist/internal/crypto"
	"github.com/acme/wadist/internal/phonenorm"
)

type importContactsRequest struct {
	Country  string `json:"country" binding:"required,len=2"`
	Text     string `json:"text"`     // 粘贴：一行一个号
	Filename string `json:"filename"` // 可选，审计用
}

type importReport struct {
	BatchID    int64 `json:"batch_id"`
	Total      int   `json:"total"`
	Inserted   int   `json:"inserted"`
	Duplicates int   `json:"duplicates"`
	Invalid    int   `json:"invalid"`
}

func (s *Server) handleImportContacts(c *gin.Context) {
	tid, ok := tenantID(c)
	if !ok { fail(c, http.StatusForbidden, "no tenant in session"); return }
	if len(s.deps.BlindKey) != 32 { fail(c, http.StatusServiceUnavailable, "contacts not configured"); return }
	var req importContactsRequest
	if err := c.ShouldBindJSON(&req); err != nil { fail(c, http.StatusBadRequest, "invalid body"); return }
	lines := strings.FieldsFunc(req.Text, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' })
	ctx := c.Request.Context()

	rep := importReport{Total: len(lines)}
	seen := map[string]bool{} // in-batch dedup by e164
	type row struct{ phone, cc string; bidx []byte }
	var rows []row
	for _, ln := range lines {
		e164, cc, valid := phonenorm.Normalize(ln, req.Country)
		if !valid { rep.Invalid++; continue }
		if seen[e164] { rep.Duplicates++; continue }
		seen[e164] = true
		rows = append(rows, row{e164, cc, crypto.BlindIndex(s.deps.BlindKey, e164)})
	}

	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil { fail(c, http.StatusInternalServerError, "tx"); return }
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, r := range rows {
		ct, err := tx.Exec(ctx,
			`INSERT INTO contacts (tenant_id, phone, phone_bidx, country_code, source)
			 VALUES ($1,$2,$3,$4,'import')
			 ON CONFLICT (tenant_id, phone_bidx) DO NOTHING`,
			tid, r.phone, r.bidx, r.cc)
		if err != nil { fail(c, http.StatusInternalServerError, "insert contact"); return }
		if ct.RowsAffected() == 1 { rep.Inserted++ } else { rep.Duplicates++ }
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO contact_import_batches (tenant_id, filename, total, inserted, duplicates, invalid)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		tid, req.Filename, rep.Total, rep.Inserted, rep.Duplicates, rep.Invalid).Scan(&rep.BatchID); err != nil {
		fail(c, http.StatusInternalServerError, "batch"); return
	}
	if err := tx.Commit(ctx); err != nil { fail(c, http.StatusInternalServerError, "commit"); return }
	s.audit(ctx, c, tid, "contacts.import", "contact_import_batch", rep.BatchID, map[string]any{"inserted": rep.Inserted, "invalid": rep.Invalid})
	c.JSON(http.StatusOK, rep)
}

type contactRow struct {
	ID          int64          `json:"id"`
	Phone       string         `json:"phone"`
	CountryCode string         `json:"country_code"`
	DisplayName string         `json:"display_name"`
	Status      string         `json:"status"`
	CreatedAt   string         `json:"created_at"`
}

func (s *Server) handleListContacts(c *gin.Context) {
	tid, ok := tenantID(c)
	if !ok { fail(c, http.StatusForbidden, "no tenant"); return }
	f := contactFilter{
		Q:       c.Query("q"),
		Country: c.Query("country"),
		Status:  c.Query("status"),
		Limit:   clampPage(atoiDefault(c.Query("limit"), 50), 50, 500),
		Offset:  atoiDefault(c.Query("offset"), 0),
	}
	if t := c.Query("tag_id"); t != "" { f.TagID = atoiDefault(t, 0) }
	where, args := buildContactWhere(f)
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil { fail(c, http.StatusInternalServerError, "tx"); return }
	defer tx.Rollback(ctx) //nolint:errcheck

	var total int
	if err := tx.QueryRow(ctx,
		`SELECT count(DISTINCT c.id) FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id`+where, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count"); return
	}
	args = append(args, f.Limit, f.Offset)
	q := `SELECT DISTINCT c.id, c.phone, COALESCE(c.country_code,''), c.display_name, c.status::text, c.created_at
	      FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id` + where +
		` ORDER BY c.id DESC LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
	rows, err := tx.Query(ctx, q, args...)
	if err != nil { fail(c, http.StatusInternalServerError, "query"); return }
	defer rows.Close()
	out := []contactRow{}
	for rows.Next() {
		var r contactRow
		var name *string
		var ts interface{ String() string }
		_ = ts
		if err := rows.Scan(&r.ID, &r.Phone, &r.CountryCode, &name, &r.Status, &r.CreatedAt); err != nil {
			fail(c, http.StatusInternalServerError, "scan"); return
		}
		if name != nil { r.DisplayName = *name }
		out = append(out, r)
	}
	c.JSON(http.StatusOK, gin.H{"rows": out, "total": total})
}
```

> **Scan note:** `created_at` 是 `timestamptz`；照本仓库其它 handler 的做法把它 Scan 进 `time.Time` 再 `.Format(time.RFC3339)`，或直接 Scan 进 `string`（pgx 支持）。实现时对齐 `campaign.go` 里 `campaignSummary.CreatedAt` 的既有写法。`atoiDefault` 若不存在则在 contacts.go 内加：`func atoiDefault(s string, d int) int { n, err := strconv.Atoi(s); if err != nil { return d }; return n }`（注意与既有工具函数不要重名——先 grep）。

实现 `handleCreateContact`（单条，normalize + bidx + ON CONFLICT 409）、`handleUpdateContact`（name/status/vars，PUT，WithTenant）、`handleDeleteContact`（DELETE，级联 tag_map 由 FK 处理）、`handleExportContacts`（复用 `writeCSV`/`csvSanitize`，同 buildContactWhere 筛选，写审计 `contacts.export`）。每个 handler 照上面范式：`tenantID` → `WithTenant` → SQL → `fail`/JSON。

`s.audit(...)` 助手：若 `internal/api/audit.go` 已有等价写审计入口则复用；否则在 contacts.go 内薄封装 `s.deps.Audit.Write(...)`（照 audit.go 现有签名，先读该文件对齐）。

- [ ] **Step 4: 注册路由**

`internal/api/router.go` 客户组（`campaigns` 组同级，requireAuth）新增：
```go
contacts := v1.Group("/contacts", s.requireAuth())
{
	contacts.GET("", s.handleListContacts)
	contacts.POST("", s.handleCreateContact)
	contacts.PUT("/:id", s.handleUpdateContact)
	contacts.DELETE("/:id", s.handleDeleteContact)
	contacts.POST("/import", s.handleImportContacts)
	contacts.GET("/export", s.handleExportContacts)
}
```
> 对齐现有 group/中间件写法（见 `campaigns := ...` 块的确切 group 变量与 `requireAuth` 调用形式）。

- [ ] **Step 5: 跑确认通过**

Run: `go test ./internal/api/ -run 'TestImportContacts_Dedup|TestContacts' -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/api/contacts.go internal/api/router.go internal/api/contacts_db_test.go
git commit -m "feat(contacts): CRUD + paginated list + import(dedup/report) + export"
```

---

## Task 5: 标签 + 分段

**Files:**
- Modify: `internal/api/contacts.go`（追加 tag/segment handler）
- Modify: `internal/api/router.go`
- Test: `internal/api/contacts_db_test.go`（追加）

**Interfaces:**
- Consumes: Task 3 `segmentFilter`/`segmentToWhere`、Task 4 handler 范式。
- Produces: `handleListTags, handleCreateTag, handleDeleteTag, handleApplyTag, handleListSegments, handleCreateSegment, handleDeleteSegment, handleSegmentPreview`。

- [ ] **Step 1: 写失败测试（打标 + 分段预览计数）**

追加到 `contacts_db_test.go`:
```go
func TestApplyTagAndSegmentPreview(t *testing.T) {
	s, _, tid := newContactServer(t)
	// seed 2 contacts + 1 tag directly (systemPool bypasses RLS in tests)
	ctx := context.Background()
	var c1, c2, tag int64
	s.systemPool().QueryRow(ctx, `INSERT INTO contacts (tenant_id,phone,phone_bidx,country_code) VALUES ($1,'+8613800138000','\x01','CN') RETURNING id`, tid).Scan(&c1)
	s.systemPool().QueryRow(ctx, `INSERT INTO contacts (tenant_id,phone,phone_bidx,country_code) VALUES ($1,'+8613800138001','\x02','CN') RETURNING id`, tid).Scan(&c2)
	s.systemPool().QueryRow(ctx, `INSERT INTO contact_tags (tenant_id,name) VALUES ($1,'vip') RETURNING id`, tid).Scan(&tag)

	// apply tag to c1 only
	body := `{"contact_ids":[` + itoa(c1) + `]}`
	w := doJSONTenantID(t, s, s.handleApplyTag, http.MethodPost, "/contacts/tags/x/apply", itoa(tag), tid, body)
	if w.Code != http.StatusOK { t.Fatalf("apply %d: %s", w.Code, w.Body.String()) }

	// preview a segment {tags:[tag]} -> expect count 1
	var segID int64
	s.systemPool().QueryRow(ctx, `INSERT INTO contact_segments (tenant_id,name,filter) VALUES ($1,'seg',$2) RETURNING id`,
		tid, `{"tags":[`+itoa(tag)+`]}`).Scan(&segID)
	w2 := doJSONTenantID(t, s, s.handleSegmentPreview, http.MethodGet, "/contacts/segments/x/preview", itoa(segID), tid, "")
	if w2.Code != http.StatusOK || !strings.Contains(w2.Body.String(), `"count":1`) {
		t.Fatalf("preview=%d %s", w2.Code, w2.Body.String())
	}
}
```
> `doJSONTenantID` = `doJSON` + `:id` param + `c.Set("tenant_id",tid)`. Add it to the test file (one small helper).

- [ ] **Step 2: 跑确认失败**

Run: `go test ./internal/api/ -run TestApplyTagAndSegmentPreview -count=1`
Expected: FAIL

- [ ] **Step 3: 实现**

追加 handler 到 `contacts.go`。关键两个：

```go
type applyTagRequest struct {
	ContactIDs []int64 `json:"contact_ids" binding:"required"`
	Remove     bool    `json:"remove"`
}

func (s *Server) handleApplyTag(c *gin.Context) {
	tid, ok := tenantID(c)
	if !ok { fail(c, http.StatusForbidden, "no tenant"); return }
	tagID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil { fail(c, http.StatusBadRequest, "bad tag id"); return }
	var req applyTagRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.ContactIDs) == 0 { fail(c, http.StatusBadRequest, "no contacts"); return }
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil { fail(c, http.StatusInternalServerError, "tx"); return }
	defer tx.Rollback(ctx) //nolint:errcheck
	if req.Remove {
		if _, err := tx.Exec(ctx, `DELETE FROM contact_tag_map WHERE tag_id=$1 AND contact_id = ANY($2)`, tagID, req.ContactIDs); err != nil {
			fail(c, http.StatusInternalServerError, "untag"); return
		}
	} else {
		if _, err := tx.Exec(ctx,
			`INSERT INTO contact_tag_map (tenant_id, contact_id, tag_id)
			 SELECT $1, unnest($2::bigint[]), $3 ON CONFLICT DO NOTHING`,
			tid, req.ContactIDs, tagID); err != nil {
			fail(c, http.StatusInternalServerError, "tag"); return
		}
	}
	if err := tx.Commit(ctx); err != nil { fail(c, http.StatusInternalServerError, "commit"); return }
	s.audit(ctx, c, tid, "contacts.tag_apply", "contact_tag", tagID, map[string]any{"n": len(req.ContactIDs), "remove": req.Remove})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleSegmentPreview(c *gin.Context) {
	tid, ok := tenantID(c)
	if !ok { fail(c, http.StatusForbidden, "no tenant"); return }
	segID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil { fail(c, http.StatusBadRequest, "bad seg id"); return }
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil { fail(c, http.StatusInternalServerError, "tx"); return }
	defer tx.Rollback(ctx) //nolint:errcheck
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT filter FROM contact_segments WHERE id=$1`, segID).Scan(&raw); err != nil {
		fail(c, http.StatusNotFound, "segment not found"); return
	}
	var f segmentFilter
	_ = json.Unmarshal(raw, &f)
	where, args := segmentToWhere(f)
	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(DISTINCT c.id) FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id`+where, args...).Scan(&count); err != nil {
		fail(c, http.StatusInternalServerError, "count"); return
	}
	c.JSON(http.StatusOK, gin.H{"count": count})
}
```
（`encoding/json` 加进 import。）实现 tag CRUD（`contact_tags` 表，创建 409 重名、删级联）与 segment CRUD（`contact_segments`，filter 存 JSONB）照 Task 4 范式。

- [ ] **Step 4: 注册路由**（`/contacts/tags*`, `/contacts/segments*`，见 spec 接口清单）

- [ ] **Step 5: 跑确认通过**

Run: `go test ./internal/api/ -run 'TestApplyTagAndSegmentPreview' -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/api/contacts.go internal/api/router.go internal/api/contacts_db_test.go
git commit -m "feat(contacts): tags + dynamic segments (preview counts)"
```

---

## Task 6: 黑名单/退订手动管理

**Files:**
- Modify: `internal/api/contacts.go`
- Modify: `internal/api/router.go`
- Test: `internal/api/contacts_db_test.go`（追加）

**Interfaces:**
- Consumes: 既有 `suppression_list (tenant_id, phone_bidx, reason, added_at)`、`crypto.BlindIndex`、`phonenorm.Normalize`。
- Produces: `handleListSuppression, handleAddSuppression, handleDeleteSuppression, handleImportSuppression`。

- [ ] **Step 1: 写失败测试**

```go
func TestSuppressionAddAndList(t *testing.T) {
	s, _, tid := newContactServer(t)
	body := `{"country":"CN","phones":["13800138000"],"reason":"opt-out"}`
	w := doJSONTenant(t, s, s.handleAddSuppression, http.MethodPost, "/suppression", tid, body)
	if w.Code != http.StatusOK { t.Fatalf("add %d: %s", w.Code, w.Body.String()) }
	w2 := doJSONTenant(t, s, s.handleListSuppression, http.MethodGet, "/suppression", tid, "")
	if !strings.Contains(w2.Body.String(), `"total":1`) { t.Fatalf("list=%s", w2.Body.String()) }
}
```

- [ ] **Step 2: 跑确认失败** — `go test ./internal/api/ -run TestSuppressionAddAndList -count=1` → FAIL

- [ ] **Step 3: 实现** `handleAddSuppression`（normalize 每个 phone → bidx → `INSERT INTO suppression_list (tenant_id, phone_bidx, reason) ... ON CONFLICT (tenant_id, phone_bidx) DO NOTHING`，WithTenant，写审计 `suppression.add`），`handleListSuppression`（分页，返回 `{rows:[{id,reason,added_at}], total}` — 注意 suppression 只存 bidx，列表不能显真号，只显 reason/时间/id），`handleDeleteSuppression`（按 id，审计），`handleImportSuppression`（批量粘贴，同导入范式）。

- [ ] **Step 4: 注册路由** `/suppression*`。

- [ ] **Step 5: 跑确认通过** — PASS

- [ ] **Step 6: Commit**
```bash
git commit -am "feat(contacts): manual suppression (blacklist/opt-out) management"
```

---

## Task 7: 建群发-从分段

**Files:**
- Create: `internal/api/campaign_from_segment.go`
- Modify: `internal/api/campaign.go`（`createCampaignRequest` + 分叉）
- Test: `internal/api/contacts_db_test.go`（追加）

**Interfaces:**
- Consumes: `segmentToWhere`、既有 `handleCreateCampaign` 的 template/campaign/recipients 插入路径、`suppression_list`。
- Produces: `func (s *Server) resolveSegmentPhones(ctx context.Context, tx pgx.Tx, tid, segID int64) ([]segmentPhone, error)`，`type segmentPhone struct{ Phone, Country string }`。解析时**排除 suppression（按 bidx）+ 排除 status!='active'**。

- [ ] **Step 1: 写失败测试（从分段建群发，排除黑名单）**

```go
func TestCreateCampaignFromSegment_ExcludesSuppressed(t *testing.T) {
	s, _, tid := newContactServer(t)
	ctx := context.Background()
	// price so the balance guard passes; topup wallet
	// (mirror campaign_test / finance seed helpers for pricing + wallet)
	seedPriceAndWallet(t, ctx, s, tid, "CN") // helper: sets tenant_pricing + tops up
	var tag int64
	s.systemPool().QueryRow(ctx, `INSERT INTO contact_tags (tenant_id,name) VALUES ($1,'seg') RETURNING id`, tid).Scan(&tag)
	// two active CN contacts, one of them suppressed
	bidxA := blind(s, "+8613800138000"); bidxB := blind(s, "+8613800138001")
	var a, b int64
	s.systemPool().QueryRow(ctx, `INSERT INTO contacts (tenant_id,phone,phone_bidx,country_code,status) VALUES ($1,'+8613800138000',$2,'CN','active') RETURNING id`, tid, bidxA).Scan(&a)
	s.systemPool().QueryRow(ctx, `INSERT INTO contacts (tenant_id,phone,phone_bidx,country_code,status) VALUES ($1,'+8613800138001',$2,'CN','active') RETURNING id`, tid, bidxB).Scan(&b)
	s.systemPool().Exec(ctx, `INSERT INTO contact_tag_map (tenant_id,contact_id,tag_id) VALUES ($1,$2,$3),($1,$4,$3)`, tid, a, tag, b)
	s.systemPool().Exec(ctx, `INSERT INTO suppression_list (tenant_id, phone_bidx, reason) VALUES ($1,$2,'x')`, tid, bidxB)
	var segID int64
	s.systemPool().QueryRow(ctx, `INSERT INTO contact_segments (tenant_id,name,filter) VALUES ($1,'s',$2) RETURNING id`, tid, `{"tags":[`+itoa(tag)+`],"status":"active"}`).Scan(&segID)

	body := `{"country":"CN","body":"hi {name}","segment_id":` + itoa(segID) + `}`
	w := doJSONTenant(t, s, s.handleCreateCampaign, http.MethodPost, "/campaigns", tid, body)
	if w.Code != http.StatusOK { t.Fatalf("create %d: %s", w.Code, w.Body.String()) }
	if !strings.Contains(w.Body.String(), `"total":1`) { // B excluded by suppression
		t.Fatalf("want total 1 (suppressed excluded), got %s", w.Body.String())
	}
}
```
> `blind(s,phone)` = `crypto.BlindIndex(s.deps.BlindKey, phone)` 测试助手；`seedPriceAndWallet` 复用 finance/campaign 测试里已有的定价+充值 seed（先 grep 现有 helper 名，别重造）。

- [ ] **Step 2: 跑确认失败** — FAIL

- [ ] **Step 3: 实现**

`internal/api/campaign_from_segment.go`:
```go
package api

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type segmentPhone struct {
	Phone   string
	Country string
}

// resolveSegmentPhones expands a saved segment into sendable recipients within
// the caller's RLS tx: status='active' only, and NOT on the suppression list
// (matched by phone_bidx). Returns [] when the segment yields nobody.
func (s *Server) resolveSegmentPhones(ctx context.Context, tx pgx.Tx, segID int64) ([]segmentPhone, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT filter FROM contact_segments WHERE id=$1`, segID).Scan(&raw); err != nil {
		return nil, err
	}
	var f segmentFilter
	_ = json.Unmarshal(raw, &f)
	if f.Status == "" {
		f.Status = "active" // sendable default
	}
	where, args := segmentToWhere(f)
	q := `SELECT DISTINCT c.phone, COALESCE(c.country_code,'')
	      FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id` + where +
		` AND NOT EXISTS (SELECT 1 FROM suppression_list sl
		   WHERE sl.tenant_id=c.tenant_id AND sl.phone_bidx=c.phone_bidx)`
	// where already starts with " WHERE ..."; if empty, prepend WHERE for the NOT EXISTS
	if where == "" {
		q = `SELECT DISTINCT c.phone, COALESCE(c.country_code,'')
		     FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id=c.id
		     WHERE NOT EXISTS (SELECT 1 FROM suppression_list sl
		       WHERE sl.tenant_id=c.tenant_id AND sl.phone_bidx=c.phone_bidx)`
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []segmentPhone
	for rows.Next() {
		var p segmentPhone
		if err := rows.Scan(&p.Phone, &p.Country); err != nil { return nil, err }
		out = append(out, p)
	}
	return out, rows.Err()
}
```

`campaign.go` 改动：
- `createCampaignRequest` 加 `SegmentID int64 \`json:"segment_id"\``，并把 `Phones` 的 `binding:"required"` 去掉（改为二选一校验）。
- `handleCreateCampaign` 开头：若 `req.SegmentID != 0`，在开 tx 后调 `resolveSegmentPhones`，把结果填充成与 `req.Phones` 等价的 `[]string`（并按 phone 的 country 逐条定价——本切片简化：整批用 `req.Country` 定价，与现有一致；分段里混国码留作后续）。空人群 → `400 "分段无可发送联系人"`。老 `phones[]` 路径不变。

> 实现要点：`resolveSegmentPhones` 必须在**同一个 `WithTenant` tx** 内调用（RLS 上下文一致）。保持现有 template/campaign/recipients 插入与计费 Hold 下游逻辑**一字不改**。

- [ ] **Step 4: 跑确认通过 + 老路回归**

Run: `go test ./internal/api/ -run 'CreateCampaign' -count=1`
Expected: PASS（含既有 `phones[]` 测试仍绿）

- [ ] **Step 5: Commit**
```bash
git add internal/api/campaign.go internal/api/campaign_from_segment.go internal/api/contacts_db_test.go
git commit -m "feat(contacts): build campaign from a segment (excl. suppressed/inactive)"
```

---

## Task 8: 前端 `/dashboard` 联系人区 + 建群发-从分段

**Files:**
- Create: `frontend/app/dashboard/contacts/page.tsx` + 组件 `frontend/components/dashboard/contacts-*.tsx`
- Modify: 建群发页（`frontend/app/dashboard/campaigns/*`）加"从分段选择"
- Modify: 客户侧导航（如有 dashboard nav 文件）

**Interfaces:** 消费 Task 4–7 的 `/api/v1/contacts*`、`/api/v1/suppression*`、`/api/v1/campaigns`(带 `segment_id`)。

- [ ] **Step 1: 实现联系人列表页**（`ProDataTable` server 模式，照 `admin-send-records.tsx` 的 server-mode 范式：分页 + tag/country/status 筛选 + 手机号搜索；列：号码/国家/状态/标签/创建时间）。
- [ ] **Step 2: 导入弹框**（粘贴/上传 textarea + 国家选择 → POST `/contacts/import` → 展示 inserted/duplicates/invalid 报告）。
- [ ] **Step 3: 标签管理 + 分段构建器**（建/删标签；分段=选 tag/country/status → 存 + preview 计数）。
- [ ] **Step 4: 黑名单 Tab**（列 reason/时间；加/删；批量导入）。
- [ ] **Step 5: 建群发页加"从分段选择"**（与手动粘号二选一：选分段 → 提交 `segment_id`；显示分段 preview 计数作为预计发送量）。
- [ ] **Step 6: build + lint**

Run: `cd frontend && npm run build && npx eslint .`
Expected: build 通过；eslint 仅 house-style baseline（`react-hooks/set-state-in-effect` 等既有），无新错误。

- [ ] **Step 7: Commit**
```bash
git add frontend/
git commit -m "feat(contacts): dashboard contact library UI + campaign-from-segment"
```

---

## Task 9: 管理员跨租户只读列表

**Files:**
- Modify: `internal/api/contacts.go`（`handleAdminListContacts`）+ `router.go`
- Create: `frontend/app/admin/contacts/*`（薄只读表）
- Test: `internal/api/contacts_db_test.go`（追加）

**Interfaces:** Consumes `buildContactWhere` + `s.systemPool()`（只读，跨租户，`tenant_id` 作为可选筛选，不走 WithTenant）。Produces `handleAdminListContacts`。

- [ ] **Step 1: 写失败测试**（跨两个租户 seed，`/admin/contacts?tenant_id=` 只返该租户；无 tenant_id 返全部）。
- [ ] **Step 2: 跑确认失败** — FAIL
- [ ] **Step 3: 实现** `handleAdminListContacts`：`s.systemPool()` 查询 `FROM contacts c LEFT JOIN contact_tag_map m ...` + `buildContactWhere` + 可选 `AND c.tenant_id=$n`，返回 `{rows,total}`（含 tenant_id 列）。只读、无审计（照 `handleAdminListRecipients` 范式）。
- [ ] **Step 4: 注册** `admin.GET("/contacts", s.handleAdminListContacts)`。
- [ ] **Step 5: 前端** `/admin/contacts` 只读表（照 `admin-send-records.tsx`），挂 admin nav "发送中心"或"客户与财务"组。
- [ ] **Step 6: build/lint + go test** — PASS
- [ ] **Step 7: Commit**
```bash
git commit -am "feat(contacts): admin cross-tenant read-only contacts view"
```

---

## 收尾

- [ ] `make gate`（tidy/vet/test-race/labels 全绿；vuln 仅预存 GO-2026-5856）。
- [ ] 用 `superpowers:requesting-code-review` 全分支复核（重点：RLS 隔离、盲索引一致性、建群发老路无回归、计费 Hold 未被触碰）。
- [ ] 更新记忆 `admin-console-roadmap` / 新建模块二进度记忆。

## Self-Review 覆盖核对

- spec §目标 1(库CRUD/分页)→T4；2(导入清洗去重报告)→T2+T4；3(导出)→T4；4(标签)→T5；5(分段)→T5；6(黑名单手动)→T6；7(从分段建群发)→T7；前端→T8；管理员只读→T9。**全覆盖**。
- 非目标(查号/STOP自动/AI)→未排任务，正确。
- 类型一致：`contactFilter`/`segmentFilter`(T3) 被 T4/T5/T7/T9 消费；`segmentPhone`(T7) 自洽；`importReport` 字段名与前端 T8 消费一致。
