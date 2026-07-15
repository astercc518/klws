# P5 养号系统 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Go 生产栈原生实现养号系统——新号自动进养号、池内多轮脚本互发防封、够信号自动毕业、业务发送只用成熟号，配后台可视化与控制。

**Architecture:** 新增独立包 `internal/warmup/`（状态机 + 策略 + 服务 + 脚本库，端口注入、可独立单测）。数据落 `warmup_profiles`/`warmup_policies`/`warmup_scripts` 三张新表（migration 0024，SystemPool 专用，照 `metric_snapshots` 平台表范式）。worker（`cmd/wadist`）加一个养号 tick（仿 `internal/metrics.Sampler` 的 `SampleOnce+RunLoop`）驱动互发与毕业评估。webhook 新增 `messages.upsert` 入站 case 喂回复信号。`selectaccount.go` 加 MATURE 闸门（`WADIST_WARMUP_GATE` 开关，默认 off，回退零风险）。后台 `/admin/warmup` 复用 P1 ProDataTable。

**Tech Stack:** Go 1.x（gin / pgx v5 / testcontainers-go）、PostgreSQL、Next.js 16 + React 19 + Tailwind 4（frontend）、Evolution API v2.3.7 数据面。

## Global Constraints

- **依据 spec：** `docs/superpowers/specs/2026-07-15-warmup-system-design.md`（每个 Task 的需求隐含包含它）。
- **迁移格式：** 新迁移文件编号 `0024_warmup.sql`，全部语句 `IF NOT EXISTS`/幂等；warmup 三表为 worker+admin 专用，照 `migrations/0023_metric_snapshots.sql`：**不启 RLS**，`REVOKE ALL ... FROM app_tenant`（并对 `app_customer` 存在则 REVOKE），仅 `SystemPool()`（BYPASSRLS）访问。`tenant_id BIGINT` 仅作展示/筛选列。
- **养号不计费：** 养号互发**绝不**触碰 `billing.Topup`/`wallet_ledger`；只有业务发送计费。
- **配额语义分离：** 养号发送计 `warmup_sent_today`，与业务 `account_devices.sent_today` 严格分离，互不相减。
- **Layer 1 不动：** 不改 `effective_quota` SQL 函数与 Go `warmupQuota/EffectiveQuota`。
- **特性开关默认关：** `WADIST_WARMUP_GATE` 默认 `off`（selectaccount 保持当前行为）。P5c 闸门仅在开关 `on` 时生效。
- **随机性可注入：** 所有随机（脚本选择、抖动间隔）用注入的 `*math/rand.Rand`（构造器接收 seed 或 `*rand.Rand`），测试可注入确定性种子。禁止在库逻辑里直接 `rand.Int`（全局源）。
- **Next.js 是改版：** 写任何 frontend 代码前先读 `frontend/node_modules/next/dist/docs/` 相关指南（见 `frontend/AGENTS.md`）。
- **i18n：** 前端所有可见文案走 zh/en 字典（`frontend/lib/i18n/dicts/admin.ts`），过 `frontend/scripts/check-i18n-keys.mjs`（注意盲区：labelKey/map 间接 key 需人工核）。
- **测试基建：** Go 集成测用 `postgres.Run(ctx,"postgres:16")` testcontainer + `applyMigrations`（glob `../../migrations/*.sql` 排序执行），照 `internal/dispatch/testsupport_test.go`。
- **提交粒度：** 每 Task 末尾提交一次；提交信息前缀 `feat(p5a/b/c):` 或 `test:`/`docs:`，结尾附 `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`。

---

# 阶段 P5a — 养号中心 + 引擎骨架 + 自动 enroll（不依赖账号池，立即可用）

## Task 1: 迁移 0024（三张表）

**Files:**
- Create: `migrations/0024_warmup.sql`
- Test: `internal/warmup/migration_test.go`

**Interfaces:**
- Produces: 表 `warmup_profiles`(account_jid PK, tenant_id, lane, stage, warmup_messages_sent, replies_received, online_since, matured_at, warmup_sent_today, warmup_sent_date, paused, created_at, updated_at)、`warmup_policies`(lane PK, min_warmup_messages, min_replies, min_online_hours, warming_cap, mature_base_cap, mature_max_cap, mature_ramp_step, updated_at)、`warmup_scripts`(id PK, lang, turns JSONB, enabled, created_at)。

- [ ] **Step 1: 写迁移文件**

`migrations/0024_warmup.sql`:
```sql
-- 0024: warmup — 养号子系统(worker+admin 专用)。照 0023 平台表范式:不启 RLS,
-- 仅 SystemPool(BYPASSRLS 的 app_system)访问,REVOKE app_tenant/app_customer。
-- tenant_id 仅作展示/筛选列。养号发送计 warmup_sent_today,与业务 sent_today 分离。

CREATE TABLE IF NOT EXISTS warmup_profiles (
    account_jid          TEXT PRIMARY KEY,
    tenant_id            BIGINT NOT NULL,
    lane                 TEXT NOT NULL DEFAULT 'STANDARD',   -- FAST / STANDARD
    stage                TEXT NOT NULL DEFAULT 'NEW',        -- NEW / WARMING / MATURE
    warmup_messages_sent INT NOT NULL DEFAULT 0,
    replies_received     INT NOT NULL DEFAULT 0,
    online_since         TIMESTAMPTZ,
    matured_at           TIMESTAMPTZ,
    warmup_sent_today    INT NOT NULL DEFAULT 0,
    warmup_sent_date     DATE,
    paused               BOOLEAN NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_warmup_profiles_stage ON warmup_profiles (stage);
CREATE INDEX IF NOT EXISTS ix_warmup_profiles_tenant_stage ON warmup_profiles (tenant_id, stage);

CREATE TABLE IF NOT EXISTS warmup_policies (
    lane                 TEXT PRIMARY KEY,
    min_warmup_messages  INT NOT NULL,
    min_replies          INT NOT NULL,
    min_online_hours     INT NOT NULL,
    warming_cap          INT NOT NULL,
    mature_base_cap      INT NOT NULL,
    mature_max_cap       INT NOT NULL,
    mature_ramp_step     INT NOT NULL,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 初始策略(照 TS LANE_POLICIES)。ON CONFLICT DO NOTHING 保持幂等 + 不覆盖后台已改值。
INSERT INTO warmup_policies (lane, min_warmup_messages, min_replies, min_online_hours,
                             warming_cap, mature_base_cap, mature_max_cap, mature_ramp_step)
VALUES ('FAST', 2, 0, 2, 5, 15, 20, 5),
       ('STANDARD', 20, 5, 36, 8, 20, 40, 5)
ON CONFLICT (lane) DO NOTHING;

CREATE TABLE IF NOT EXISTS warmup_scripts (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lang       TEXT NOT NULL,           -- zh / pt / en …,与配对号国家/代理国匹配
    turns      JSONB NOT NULL,          -- [{"from":"A","text":"oi {name}, tudo bem?"},{"from":"B","text":"tudo 👍"}]
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_warmup_scripts_lang ON warmup_scripts (lang) WHERE enabled;

-- 初始植入(每语种至少一套多轮脚本;pt/zh/en)。幂等:仅当该 lang 无脚本时插入。
INSERT INTO warmup_scripts (lang, turns)
SELECT v.lang, v.turns::jsonb FROM (VALUES
  ('pt', '[{"from":"A","text":"oi {name}, tudo bem?"},{"from":"B","text":"tudo sim {emoji} e vc?"},{"from":"A","text":"tudo tranquilo por aqui {emoji}"},{"from":"B","text":"que bom! bom dia"}]'),
  ('en', '[{"from":"A","text":"hey {name}, how are you?"},{"from":"B","text":"good {emoji} you?"},{"from":"A","text":"all good here {emoji}"},{"from":"B","text":"nice, take care"}]'),
  ('zh', '[{"from":"A","text":"在吗{name}?"},{"from":"B","text":"在的{emoji}"},{"from":"A","text":"最近咋样"},{"from":"B","text":"挺好的 你呢{emoji}"}]')
) AS v(lang, turns)
WHERE NOT EXISTS (SELECT 1 FROM warmup_scripts w WHERE w.lang = v.lang);

REVOKE ALL ON warmup_profiles, warmup_policies, warmup_scripts FROM app_tenant;
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_customer') THEN
    EXECUTE 'REVOKE ALL ON warmup_profiles, warmup_policies, warmup_scripts FROM app_customer';
  END IF;
END $$;
```

- [ ] **Step 2: 写迁移测试**

`internal/warmup/migration_test.go`（用与 store 相同的 testcontainer + 全量迁移 glob；注意本包无现成 helper，本 Task 顺带建包内测试 helper `internal/warmup/testsupport_test.go`）：

`internal/warmup/testsupport_test.go`:
```go
package warmup

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func pgPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("app_test"), postgres.WithUsername("test"), postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(wait.ForAll(
			wait.ForListeningPort("5432/tcp"),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		).WithStartupTimeout(60*time.Second)))
	if err != nil {
		t.Fatalf("pg: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, _ := c.ConnectionString(ctx, "sslmode=disable")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyMigrations(t, ctx, pool)
	return pool, ctx
}

func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	files, _ := filepath.Glob("../../migrations/*.sql")
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, ".down.sql") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
}
```

`internal/warmup/migration_test.go`:
```go
package warmup

import "testing"

func TestMigrationSeedsPolicies(t *testing.T) {
	pool, ctx := pgPool(t)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM warmup_policies`).Scan(&n); err != nil {
		t.Fatalf("query policies: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 seeded lanes, got %d", n)
	}
	var scripts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM warmup_scripts WHERE enabled`).Scan(&scripts); err != nil {
		t.Fatalf("query scripts: %v", err)
	}
	if scripts < 3 {
		t.Fatalf("want >=3 seeded scripts, got %d", scripts)
	}
}
```

- [ ] **Step 3: 跑测试验证通过**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestMigrationSeedsPolicies -v`
Expected: PASS（起容器约需 30–60s）。

- [ ] **Step 4: 提交**

```bash
git add migrations/0024_warmup.sql internal/warmup/migration_test.go internal/warmup/testsupport_test.go
git commit -m "feat(p5a): migration 0024 warmup tables + seeded policies/scripts

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: 状态机 `state.go`

**Files:**
- Create: `internal/warmup/state.go`
- Test: `internal/warmup/state_test.go`

**Interfaces:**
- Produces: `type Stage string`（`StageNew="NEW"`,`StageWarming="WARMING"`,`StageMature="MATURE"`）、`type Event string`（`EventEnroll="ENROLL"`,`EventPromote="PROMOTE"`,`EventDemote="DEMOTE"`）、`func Transition(s Stage, e Event) (Stage, error)`、`var ErrInvalidTransition error`。

- [ ] **Step 1: 写失败测试**

`internal/warmup/state_test.go`:
```go
package warmup

import "testing"

func TestTransition(t *testing.T) {
	cases := []struct {
		s    Stage
		e    Event
		want Stage
		ok   bool
	}{
		{StageNew, EventEnroll, StageWarming, true},
		{StageWarming, EventPromote, StageMature, true},
		{StageMature, EventDemote, StageWarming, true},
		{StageNew, EventPromote, "", false},
		{StageMature, EventEnroll, "", false},
		{StageWarming, EventDemote, "", false},
	}
	for _, c := range cases {
		got, err := Transition(c.s, c.e)
		if c.ok && (err != nil || got != c.want) {
			t.Fatalf("%s+%s: want %s ok, got %s err=%v", c.s, c.e, c.want, got, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%s+%s: want error, got %s", c.s, c.e, got)
		}
	}
}
```

- [ ] **Step 2: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestTransition -v`
Expected: 编译失败 "undefined: Transition/Stage/Event"。

- [ ] **Step 3: 写实现**

`internal/warmup/state.go`:
```go
// Package warmup 实现新号养号子系统:状态机、策略、服务、脚本库。
package warmup

import "errors"

type Stage string
type Event string

const (
	StageNew     Stage = "NEW"
	StageWarming Stage = "WARMING"
	StageMature  Stage = "MATURE"

	EventEnroll  Event = "ENROLL"
	EventPromote Event = "PROMOTE"
	EventDemote  Event = "DEMOTE"
)

// ErrInvalidTransition 表示当前阶段不接受该事件。
var ErrInvalidTransition = errors.New("warmup: invalid stage transition")

var transitions = map[Stage]map[Event]Stage{
	StageNew:     {EventEnroll: StageWarming},
	StageWarming: {EventPromote: StageMature},
	StageMature:  {EventDemote: StageWarming},
}

// Transition 返回 s 接受 e 后的新阶段;非法组合返回 ErrInvalidTransition。
func Transition(s Stage, e Event) (Stage, error) {
	if next, ok := transitions[s][e]; ok {
		return next, nil
	}
	return "", ErrInvalidTransition
}
```

- [ ] **Step 4: 跑测试验证通过**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestTransition -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/warmup/state.go internal/warmup/state_test.go
git commit -m "feat(p5a): warmup stage machine (NEW->WARMING->MATURE + DEMOTE)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: 策略 `policy.go`

**Files:**
- Create: `internal/warmup/policy.go`
- Test: `internal/warmup/policy_test.go`

**Interfaces:**
- Consumes: `Stage`（Task 2）。
- Produces: `type Lane string`（`LaneFast="FAST"`,`LaneStandard="STANDARD"`）、`type Policy struct{MinWarmupMessages, MinReplies, MinOnlineHours, WarmingCap, MatureBaseCap, MatureMaxCap, MatureRampStep int}`、`func MeetsPromotion(msgs, replies int, onlineHours float64, p Policy) bool`、`func DailyCap(p Policy, stage Stage, matureDays int) int`。

- [ ] **Step 1: 写失败测试**

`internal/warmup/policy_test.go`:
```go
package warmup

import "testing"

var stdPolicy = Policy{MinWarmupMessages: 20, MinReplies: 5, MinOnlineHours: 36,
	WarmingCap: 8, MatureBaseCap: 20, MatureMaxCap: 40, MatureRampStep: 5}

func TestMeetsPromotion(t *testing.T) {
	if !MeetsPromotion(20, 5, 36, stdPolicy) {
		t.Fatal("exact thresholds should promote")
	}
	if MeetsPromotion(19, 5, 36, stdPolicy) {
		t.Fatal("insufficient messages must not promote")
	}
	if MeetsPromotion(20, 4, 36, stdPolicy) {
		t.Fatal("insufficient replies must not promote")
	}
	if MeetsPromotion(20, 5, 35.9, stdPolicy) {
		t.Fatal("insufficient online hours must not promote")
	}
}

func TestDailyCap(t *testing.T) {
	if got := DailyCap(stdPolicy, StageNew, 0); got != 0 {
		t.Fatalf("NEW cap want 0 got %d", got)
	}
	if got := DailyCap(stdPolicy, StageWarming, 0); got != 8 {
		t.Fatalf("WARMING cap want 8 got %d", got)
	}
	if got := DailyCap(stdPolicy, StageMature, 0); got != 20 {
		t.Fatalf("MATURE day0 cap want 20 got %d", got)
	}
	if got := DailyCap(stdPolicy, StageMature, 3); got != 35 {
		t.Fatalf("MATURE day3 cap want 35 got %d", got)
	}
	if got := DailyCap(stdPolicy, StageMature, 100); got != 40 {
		t.Fatalf("MATURE cap must clamp to max 40 got %d", got)
	}
}
```

- [ ] **Step 2: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/warmup/ -run 'TestMeetsPromotion|TestDailyCap' -v`
Expected: 编译失败 "undefined: Policy/MeetsPromotion/DailyCap"。

- [ ] **Step 3: 写实现**

`internal/warmup/policy.go`:
```go
package warmup

type Lane string

const (
	LaneFast     Lane = "FAST"
	LaneStandard Lane = "STANDARD"
)

// Policy 是一条车道的养号阈值与配额上限,存 warmup_policies,可后台热改。
type Policy struct {
	MinWarmupMessages int
	MinReplies        int
	MinOnlineHours    int
	WarmingCap        int
	MatureBaseCap     int
	MatureMaxCap      int
	MatureRampStep    int
}

// MeetsPromotion 判断信号是否够 WARMING->MATURE(三条全达标)。
func MeetsPromotion(msgs, replies int, onlineHours float64, p Policy) bool {
	return msgs >= p.MinWarmupMessages &&
		replies >= p.MinReplies &&
		onlineHours >= float64(p.MinOnlineHours)
}

// DailyCap 返回该阶段当日允许的发送额。NEW=0;WARMING=warmingCap;
// MATURE=base+matureDays*step,封顶 max。
func DailyCap(p Policy, stage Stage, matureDays int) int {
	switch stage {
	case StageNew:
		return 0
	case StageWarming:
		return p.WarmingCap
	default:
		cap := p.MatureBaseCap + matureDays*p.MatureRampStep
		if cap > p.MatureMaxCap {
			return p.MatureMaxCap
		}
		return cap
	}
}
```

- [ ] **Step 4: 跑测试验证通过**

Run: `cd /var/klwa && go test ./internal/warmup/ -run 'TestMeetsPromotion|TestDailyCap' -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/warmup/policy.go internal/warmup/policy_test.go
git commit -m "feat(p5a): warmup lane policy (promotion criteria + daily cap ramp)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: 存储 `store.go`（warmup_profiles / warmup_policies CRUD）

**Files:**
- Create: `internal/warmup/store.go`
- Test: `internal/warmup/store_test.go`

**Interfaces:**
- Consumes: `Stage`,`Lane`,`Policy`（Task 2/3）。
- Produces:
  - `type Profile struct{AccountJID string; TenantID int64; Lane Lane; Stage Stage; WarmupMessagesSent, RepliesReceived int; OnlineSince, MaturedAt *time.Time; WarmupSentToday int; WarmupSentDate *time.Time; Paused bool}`
  - `type Store struct{...}` + `func NewStore(pool *pgxpool.Pool) *Store`（pool 须为 SystemPool）
  - `func (s *Store) EnrollIfAbsent(ctx, jid string, tenantID int64, lane Lane, now time.Time) error`（幂等插入 stage=WARMING, online_since=now；`ON CONFLICT DO NOTHING`）
  - `func (s *Store) Get(ctx, jid string) (Profile, error)`（无行返回 `ErrNotFound`）
  - `func (s *Store) ListByStage(ctx, stage Stage, limit int) ([]Profile, error)`
  - `func (s *Store) Save(ctx, p Profile, now time.Time) error`（全字段 UPDATE + updated_at=now）
  - `func (s *Store) PolicyFor(ctx, lane Lane) (Policy, error)`
  - `var ErrNotFound error`

- [ ] **Step 1: 写失败测试**

`internal/warmup/store_test.go`:
```go
package warmup

import (
	"testing"
	"time"
)

func TestStoreEnrollGetSave(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

	if err := st.EnrollIfAbsent(ctx, "111@s.whatsapp.net", 1, LaneStandard, now); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	// 幂等:二次 enroll 不报错、不重置。
	if err := st.EnrollIfAbsent(ctx, "111@s.whatsapp.net", 1, LaneFast, now.Add(time.Hour)); err != nil {
		t.Fatalf("re-enroll: %v", err)
	}
	p, err := st.Get(ctx, "111@s.whatsapp.net")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.Stage != StageWarming || p.Lane != LaneStandard {
		t.Fatalf("want WARMING/STANDARD got %s/%s", p.Stage, p.Lane)
	}
	if p.OnlineSince == nil || !p.OnlineSince.Equal(now) {
		t.Fatalf("online_since not preserved: %v", p.OnlineSince)
	}

	p.WarmupMessagesSent = 20
	p.RepliesReceived = 5
	p.Stage = StageMature
	if err := st.Save(ctx, p, now); err != nil {
		t.Fatalf("save: %v", err)
	}
	p2, _ := st.Get(ctx, "111@s.whatsapp.net")
	if p2.WarmupMessagesSent != 20 || p2.Stage != StageMature {
		t.Fatalf("save not persisted: %+v", p2)
	}
}

func TestStoreListByStageAndPolicy(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	_ = st.EnrollIfAbsent(ctx, "a@s.whatsapp.net", 1, LaneStandard, now)
	_ = st.EnrollIfAbsent(ctx, "b@s.whatsapp.net", 1, LaneStandard, now)
	list, err := st.ListByStage(ctx, StageWarming, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("list warming want 2 got %d err=%v", len(list), err)
	}
	pol, err := st.PolicyFor(ctx, LaneStandard)
	if err != nil || pol.MinWarmupMessages != 20 {
		t.Fatalf("policy STANDARD want min 20 got %+v err=%v", pol, err)
	}
}

func TestStoreGetNotFound(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	if _, err := st.Get(ctx, "missing@s.whatsapp.net"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound got %v", err)
	}
}
```

- [ ] **Step 2: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestStore -v`
Expected: 编译失败 "undefined: NewStore/Store/Profile/ErrNotFound"。

- [ ] **Step 3: 写实现**

`internal/warmup/store.go`:
```go
package warmup

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound 表示无该 jid 的 warmup profile。
var ErrNotFound = errors.New("warmup: profile not found")

// Profile 是 warmup_profiles 一行。
type Profile struct {
	AccountJID         string
	TenantID           int64
	Lane               Lane
	Stage              Stage
	WarmupMessagesSent int
	RepliesReceived    int
	OnlineSince        *time.Time
	MaturedAt          *time.Time
	WarmupSentToday    int
	WarmupSentDate     *time.Time
	Paused             bool
}

// Store 是 warmup 三表的持久层。pool 必须是 SystemPool(BYPASSRLS):
// warmup 表 REVOKE 了 app_tenant,worker 与 admin 跨租户操作。
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// EnrollIfAbsent 幂等插入 stage=WARMING 的 profile;已存在则不动(不重置车道/信号)。
func (s *Store) EnrollIfAbsent(ctx context.Context, jid string, tenantID int64, lane Lane, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO warmup_profiles (account_jid, tenant_id, lane, stage, online_since, created_at, updated_at)
VALUES ($1, $2, $3, 'WARMING', $4, $4, $4)
ON CONFLICT (account_jid) DO NOTHING`, jid, tenantID, string(lane), now)
	return err
}

const profileCols = `account_jid, tenant_id, lane, stage, warmup_messages_sent,
	replies_received, online_since, matured_at, warmup_sent_today, warmup_sent_date, paused`

func scanProfile(row pgx.Row) (Profile, error) {
	var p Profile
	var lane, stage string
	err := row.Scan(&p.AccountJID, &p.TenantID, &lane, &stage, &p.WarmupMessagesSent,
		&p.RepliesReceived, &p.OnlineSince, &p.MaturedAt, &p.WarmupSentToday, &p.WarmupSentDate, &p.Paused)
	p.Lane = Lane(lane)
	p.Stage = Stage(stage)
	return p, err
}

func (s *Store) Get(ctx context.Context, jid string) (Profile, error) {
	p, err := scanProfile(s.pool.QueryRow(ctx,
		`SELECT `+profileCols+` FROM warmup_profiles WHERE account_jid=$1`, jid))
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	return p, err
}

func (s *Store) ListByStage(ctx context.Context, stage Stage, limit int) ([]Profile, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+profileCols+` FROM warmup_profiles WHERE stage=$1 ORDER BY updated_at LIMIT $2`,
		string(stage), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Save 覆盖写全部可变字段。
func (s *Store) Save(ctx context.Context, p Profile, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
UPDATE warmup_profiles SET lane=$2, stage=$3, warmup_messages_sent=$4, replies_received=$5,
	online_since=$6, matured_at=$7, warmup_sent_today=$8, warmup_sent_date=$9, paused=$10, updated_at=$11
WHERE account_jid=$1`,
		p.AccountJID, string(p.Lane), string(p.Stage), p.WarmupMessagesSent, p.RepliesReceived,
		p.OnlineSince, p.MaturedAt, p.WarmupSentToday, p.WarmupSentDate, p.Paused, now)
	return err
}

func (s *Store) PolicyFor(ctx context.Context, lane Lane) (Policy, error) {
	var p Policy
	err := s.pool.QueryRow(ctx, `
SELECT min_warmup_messages, min_replies, min_online_hours, warming_cap,
	mature_base_cap, mature_max_cap, mature_ramp_step
FROM warmup_policies WHERE lane=$1`, string(lane)).Scan(
		&p.MinWarmupMessages, &p.MinReplies, &p.MinOnlineHours, &p.WarmingCap,
		&p.MatureBaseCap, &p.MatureMaxCap, &p.MatureRampStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return Policy{}, ErrNotFound
	}
	return p, err
}
```

- [ ] **Step 4: 跑测试验证通过**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestStore -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/warmup/store.go internal/warmup/store_test.go
git commit -m "feat(p5a): warmup Store (profiles CRUD + policy loader, SystemPool)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: 服务 `service.go`（Enroll/RecordReply/EvaluateAndPromote/Demote；PairAndWarm 桩）

**Files:**
- Create: `internal/warmup/service.go`
- Test: `internal/warmup/service_test.go`

**Interfaces:**
- Consumes: `Store`,`Profile`,`Policy`,`Stage`,`Lane`,`Transition`,`MeetsPromotion`（Task 2–4）。
- Produces:
  - `type Clock func() time.Time`
  - `type Service struct{...}` + `func NewService(store *Store, clock Clock) *Service`
  - `func (s *Service) Enroll(ctx, jid string, tenantID int64, lane Lane) error`
  - `func (s *Service) RecordReply(ctx, jid string) error`（找不到 profile 时静默返回 nil——非池内号）
  - `func (s *Service) EvaluateAndPromote(ctx, jid string) (bool, error)`
  - `func (s *Service) Demote(ctx, jid, reason string) error`
  - `func (s *Service) SetLane / SetPaused / ForcePromote(ctx, jid ...) error`（后台手动控制用）

- [ ] **Step 1: 写失败测试**

`internal/warmup/service_test.go`:
```go
package warmup

import (
	"testing"
	"time"
)

func fixedClock(ts time.Time) Clock { return func() time.Time { return ts } }

func TestServiceEnrollAndPromote(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	base := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	svc := NewService(st, fixedClock(base))

	if err := svc.Enroll(ctx, "x@s.whatsapp.net", 1, LaneStandard); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	// 信号不足,不该毕业。
	ok, err := svc.EvaluateAndPromote(ctx, "x@s.whatsapp.net")
	if err != nil || ok {
		t.Fatalf("premature promote ok=%v err=%v", ok, err)
	}

	// 灌满信号(直接改库),并让 clock 前进 40h 满足在线时长。
	p, _ := st.Get(ctx, "x@s.whatsapp.net")
	p.WarmupMessagesSent = 20
	p.RepliesReceived = 5
	_ = st.Save(ctx, p, base)
	svc2 := NewService(st, fixedClock(base.Add(40*time.Hour)))
	ok, err = svc2.EvaluateAndPromote(ctx, "x@s.whatsapp.net")
	if err != nil || !ok {
		t.Fatalf("should promote ok=%v err=%v", ok, err)
	}
	p2, _ := st.Get(ctx, "x@s.whatsapp.net")
	if p2.Stage != StageMature || p2.MaturedAt == nil {
		t.Fatalf("want MATURE+maturedAt got %s %v", p2.Stage, p2.MaturedAt)
	}
}

func TestServiceRecordReplyMissingIsNoop(t *testing.T) {
	pool, ctx := pgPool(t)
	svc := NewService(NewStore(pool), fixedClock(time.Now()))
	if err := svc.RecordReply(ctx, "notpool@s.whatsapp.net"); err != nil {
		t.Fatalf("record reply on missing must be noop, got %v", err)
	}
}

func TestServiceDemote(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	base := time.Now().UTC()
	svc := NewService(st, fixedClock(base))
	_ = svc.Enroll(ctx, "d@s.whatsapp.net", 1, LaneStandard)
	p, _ := st.Get(ctx, "d@s.whatsapp.net")
	p.Stage = StageMature
	_ = st.Save(ctx, p, base)
	if err := svc.Demote(ctx, "d@s.whatsapp.net", "device_removed"); err != nil {
		t.Fatalf("demote: %v", err)
	}
	p2, _ := st.Get(ctx, "d@s.whatsapp.net")
	if p2.Stage != StageWarming {
		t.Fatalf("want WARMING after demote got %s", p2.Stage)
	}
}
```

- [ ] **Step 2: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestService -v`
Expected: 编译失败 "undefined: NewService/Service/Clock"。

- [ ] **Step 3: 写实现**

`internal/warmup/service.go`:
```go
package warmup

import (
	"context"
	"errors"
	"time"
)

// Clock 返回当前时间,便于测试注入。
type Clock func() time.Time

// Service 是养号业务编排:enroll / 回复信号 / 毕业评估 / 降级 / 手动控制。
// PairAndWarm(池内互发)在 scripts.go / pair.go 中扩展(P5b)。
type Service struct {
	store *Store
	clock Clock
}

func NewService(store *Store, clock Clock) *Service {
	return &Service{store: store, clock: clock}
}

// Enroll 让新号进 WARMING。幂等(已有 profile 不动)。
func (s *Service) Enroll(ctx context.Context, jid string, tenantID int64, lane Lane) error {
	return s.store.EnrollIfAbsent(ctx, jid, tenantID, lane, s.clock())
}

// RecordReply 累加入站回复信号。非池内号(无 profile)静默跳过。
func (s *Service) RecordReply(ctx context.Context, jid string) error {
	p, err := s.store.Get(ctx, jid)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	p.RepliesReceived++
	return s.store.Save(ctx, p, s.clock())
}

// EvaluateAndPromote 在信号达标时把 WARMING 号升 MATURE,返回是否毕业。
func (s *Service) EvaluateAndPromote(ctx context.Context, jid string) (bool, error) {
	p, err := s.store.Get(ctx, jid)
	if err != nil {
		return false, err
	}
	if p.Stage != StageWarming {
		return false, nil
	}
	pol, err := s.store.PolicyFor(ctx, p.Lane)
	if err != nil {
		return false, err
	}
	var onlineHours float64
	if p.OnlineSince != nil {
		onlineHours = s.clock().Sub(*p.OnlineSince).Hours()
	}
	if !MeetsPromotion(p.WarmupMessagesSent, p.RepliesReceived, onlineHours, pol) {
		return false, nil
	}
	next, err := Transition(p.Stage, EventPromote)
	if err != nil {
		return false, err
	}
	now := s.clock()
	p.Stage = next
	p.MaturedAt = &now
	if err := s.store.Save(ctx, p, now); err != nil {
		return false, err
	}
	return true, nil
}

// Demote 把 MATURE 号退回 WARMING(封号信号/health 掉)。reason 仅用于日志/审计。
func (s *Service) Demote(ctx context.Context, jid, reason string) error {
	p, err := s.store.Get(ctx, jid)
	if err != nil {
		return err
	}
	next, err := Transition(p.Stage, EventDemote)
	if err != nil {
		return err
	}
	now := s.clock()
	p.Stage = next
	p.MaturedAt = nil
	p.OnlineSince = &now // 重置在线基准,重新计时养号
	return s.store.Save(ctx, p, now)
}

// SetLane 改车道(后台手动)。
func (s *Service) SetLane(ctx context.Context, jid string, lane Lane) error {
	p, err := s.store.Get(ctx, jid)
	if err != nil {
		return err
	}
	p.Lane = lane
	return s.store.Save(ctx, p, s.clock())
}

// SetPaused 暂停/恢复养号(后台手动)。
func (s *Service) SetPaused(ctx context.Context, jid string, paused bool) error {
	p, err := s.store.Get(ctx, jid)
	if err != nil {
		return err
	}
	p.Paused = paused
	return s.store.Save(ctx, p, s.clock())
}

// ForcePromote 无视信号强制毕业(后台手动)。
func (s *Service) ForcePromote(ctx context.Context, jid string) error {
	p, err := s.store.Get(ctx, jid)
	if err != nil {
		return err
	}
	next, err := Transition(p.Stage, EventPromote)
	if err != nil {
		return err
	}
	now := s.clock()
	p.Stage = next
	p.MaturedAt = &now
	return s.store.Save(ctx, p, now)
}
```

- [ ] **Step 4: 跑测试验证通过**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestService -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/warmup/service.go internal/warmup/service_test.go
git commit -m "feat(p5a): warmup Service (enroll/reply/promote/demote + manual controls)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 6: 新号自动 enroll（扫码接入即进养号）

**Files:**
- Modify: `internal/api/webhook_evolution.go`（`connection.update` case，`EnrollDeviceForInstance` 之后追加 warmup enroll）
- Modify: `internal/api/webhook_evolution.go`（`EvolutionWebhook` 结构 + 构造器加可选 `warmupEnroller`）
- Modify: `internal/api/router.go`（构造 webhook 时注入 warmup service）
- Test: `internal/api/webhook_warmup_test.go`

**Interfaces:**
- Consumes: `warmup.Service.Enroll`（Task 5）。
- Produces: `type warmupEnroller interface{ Enroll(ctx context.Context, jid string, tenantID int64, lane warmup.Lane) error }`；`EvolutionWebhook.warmup` 字段（nil 安全）。

- [ ] **Step 1: 写失败测试**

`internal/api/webhook_warmup_test.go`（用 fake enroller + 直接调 handle，模仿 `audit_db_test.go` 的 httptest 方式；此处只验 enroll 被调用，不起 pg）：
```go
package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/acme/wadist/internal/warmup"
)

type fakeEnroller struct{ calls []string }

func (f *fakeEnroller) Enroll(_ context.Context, jid string, _ int64, _ warmup.Lane) error {
	f.calls = append(f.calls, jid)
	return nil
}

func TestConnectionUpdateAutoEnrollsWarmup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	enr := &fakeEnroller{}
	wh := NewEvolutionWebhook("", nil, fakeInstStore{}, nil, nil)
	wh.warmup = enr // 注入(字段导出给测试或经构造器,见实现)
	r := gin.New()
	wh.Register(r)

	body := `{"event":"connection.update","instance":"inst-1","data":{"state":"open","wuid":"55119@s.whatsapp.net"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	if len(enr.calls) != 1 || enr.calls[0] != "55119@s.whatsapp.net" {
		t.Fatalf("want enroll of jid, got %v", enr.calls)
	}
}
```

> 注：本测试需要一个满足 `instanceStore` 的最小 fake `fakeInstStore`（`JIDForInstance`/`BindInstanceJIDIfUnset`/`SetInstanceState`/`EnrollDeviceForInstance` 全部返回零值/nil）。若包内已有类似 fake 则复用；否则在本测试文件内定义。`data.wuid` 是 connection.update 里本号 jid 的来源（真机验证 V1，见 spec §5 与 `webhook_evolution_types.go` 的 `ownJID()`）。租户 id 从 instanceStore 解析不到时用 0（fake 返回 0），enroll 仍被调用即达标。

- [ ] **Step 2: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/api/ -run TestConnectionUpdateAutoEnrollsWarmup -v`
Expected: 编译失败（`wh.warmup` 字段不存在）。

- [ ] **Step 3: 写实现**

在 `internal/api/webhook_evolution.go`：

新增接口与字段：
```go
// warmupEnroller folds a freshly-connected account into the warmup pool.
// Optional: nil skips the warmup path (dev/tests without warmup wiring).
type warmupEnroller interface {
	Enroll(ctx context.Context, jid string, tenantID int64, lane warmup.Lane) error
}
```
在 `EvolutionWebhook` struct 末尾加 `warmup warmupEnroller`，并加导入 `"github.com/acme/wadist/internal/warmup"`。构造器保持签名不变（新字段经 setter 或 router 直接赋值——为最小改动，导出一个方法）：
```go
// WithWarmup wires the warmup enroller (called post-EnrollDeviceForInstance on
// connection.update). Separate from the constructor to keep NewEvolutionWebhook's
// signature stable for existing callers/tests.
func (h *EvolutionWebhook) WithWarmup(w warmupEnroller) *EvolutionWebhook {
	h.warmup = w
	return h
}
```
在 `connection.update` case 里，`EnrollDeviceForInstance` 之后追加：
```go
			// 折进养号池(best-effort,和 device 入池同理由:失败不阻塞 200,
			// 下次 connection.update 重试)。lane 默认 STANDARD;租户从路由行解析,
			// 解析不到用 0(仅影响展示列,不影响养号逻辑)。
			if h.warmup != nil {
				tenantID := h.tenantForInstance(ctx, w.Instance)
				_ = h.warmup.Enroll(ctx, jid, tenantID, warmup.LaneStandard)
			}
```
新增私有辅助（若 instanceStore 未暴露 tenant 解析，则返回 0；这里用已有 `JIDForInstance` 无法取 tenant，故简化为 0，租户列由后台 enroll 时无关紧要——展示可后续 join account_instances 补）：
```go
// tenantForInstance best-effort 解析实例所属租户;取不到返回 0。
func (h *EvolutionWebhook) tenantForInstance(ctx context.Context, instance string) int64 {
	// instanceStore 当前不暴露 tenant 解析;保留 0,展示列由 admin 列表 join
	// account_instances 时补全。避免为此扩接口。
	return 0
}
```

- [ ] **Step 4: router 注入 warmup service**

在 `internal/api/router.go` 第 133 行附近（`NewEvolutionWebhook(...).Register(v1)`）改为链式注入。需 `Deps` 增加 `Warmup *warmup.Service` 字段（Task 8 会用同一字段）：
```go
	NewEvolutionWebhook(s.deps.EvolutionWebhookSecret, s.deps.Receipt, s.deps.Mgr, nil, s.deps.QRCache).
		WithWarmup(s.deps.Warmup). // nil 安全
		Register(v1)
```
并在 `Deps` struct（router.go 第 31 行块）加：
```go
	Warmup   *warmup.Service       // 养号服务(webhook 自动 enroll + admin API);nil 安全
```
（`warmup.Service` 满足 `warmupEnroller`：`Enroll` 签名一致。）

- [ ] **Step 5: 跑测试验证通过**

Run: `cd /var/klwa && go test ./internal/api/ -run TestConnectionUpdateAutoEnrollsWarmup -v`
Expected: PASS。（若 `s.deps.Warmup` 为 nil，`WithWarmup(nil)` 后 `h.warmup != nil` 为 false，跳过——需确认 `WithWarmup` 对 nil `*warmup.Service` 的处理：传入 nil 指针会让接口非 nil。**修正见 Step 6**。）

- [ ] **Step 6: 修正 nil 接口陷阱 + 跑全包测试**

由于把 `*warmup.Service`(nil) 赋给 `warmupEnroller` 接口会得到"非 nil 接口含 nil 指针"，`WithWarmup` 须显式判空：
```go
func (h *EvolutionWebhook) WithWarmup(w *warmup.Service) *EvolutionWebhook {
	if w != nil {
		h.warmup = w
	}
	return h
}
```
（把参数类型收窄为具体 `*warmup.Service`，测试里的 `fakeEnroller` 改为直接对 `h.warmup` 赋值——测试已这么写。）

Run: `cd /var/klwa && go build ./... && go test ./internal/api/ -run TestConnectionUpdateAutoEnrollsWarmup -v`
Expected: PASS。

- [ ] **Step 7: 提交**

```bash
git add internal/api/webhook_evolution.go internal/api/router.go internal/api/webhook_warmup_test.go
git commit -m "feat(p5a): auto-enroll scanned accounts into warmup on connection.update

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 7: 后台养号 API `warmup_api.go`（列表 + 单号动作 + 策略读写）

**Files:**
- Create: `internal/api/warmup_api.go`
- Create: `internal/warmup/list.go`（带 account_devices join 的列表查询）
- Test: `internal/api/warmup_api_test.go`
- Test: `internal/warmup/list_test.go`

**Interfaces:**
- Consumes: `warmup.Service`（Task 5）、`warmup.Store`（Task 4）、Server/Deps（Task 6）。
- Produces:
  - `internal/warmup`：`type ListRow struct{AccountJID string; TenantID int64; Lane Lane; Stage Stage; WarmupMessagesSent, RepliesReceived int; OnlineHours float64; Health int; RegisteredAt *time.Time; BusinessQuotaRemaining int; Paused bool}`；`func (s *Store) List(ctx, f ListFilter) ([]ListRow, int, error)`；`type ListFilter struct{Stage, Lane, Q string; Limit, Offset int}`；`type Overview struct{...}`；`func (s *Store) Overview(ctx) (Overview, error)`；`func (s *Store) UpsertPolicy(ctx, lane Lane, p Policy, now time.Time) error`；`func (s *Store) ListPolicies(ctx) (map[Lane]Policy, error)`。
  - `internal/api`：handlers `handleAdminWarmupList/Overview/Action/GetPolicies/SetPolicy`。
- 端点：`GET /admin/warmup`、`GET /admin/warmup/overview`、`POST /admin/warmup/:jid/action`（body `{action:"pause|resume|promote|demote|lane", lane?}`）、`GET /admin/warmup/policies`、`PUT /admin/warmup/policies/:lane`。

- [ ] **Step 1: 写失败测试（list.go）**

`internal/warmup/list_test.go`:
```go
package warmup

import (
	"testing"
	"time"
)

// seedDevice 插一个 account_devices 行(养号列表要 join 它取 health/registered_at)。
func seedDevice(t *testing.T, pool interface {
	Exec(...any)
}, jid string) { /* 见实现说明 */ }

func TestStoreListJoinsDevice(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	// 造一个 device + proxy(照 dispatch/testsupport seedAccount 的写法)。
	var proxyID int64
	_ = pool.QueryRow(ctx, `INSERT INTO proxy_pool (proxy_url,proxy_type,country_code,max_bindings,current_bindings)
		VALUES ('socks5://w','socks5','US',1,1) RETURNING id`).Scan(&proxyID)
	_, err := pool.Exec(ctx, `INSERT INTO account_devices
		(tenant_id,account_jid,phone_number,ban_status,proxy_id,registered_at,health_score,sent_today)
		VALUES (1,'w@s.whatsapp.net','15550001111','active',$1,now()-interval '10 days',80,3)`, proxyID)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
	_ = st.EnrollIfAbsent(ctx, "w@s.whatsapp.net", 1, LaneStandard, now)

	rows, total, err := st.List(ctx, ListFilter{Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("list want 1 got %d/%d err=%v", len(rows), total, err)
	}
	r := rows[0]
	if r.Health != 80 || r.Stage != StageWarming {
		t.Fatalf("row join wrong: %+v", r)
	}
	if r.BusinessQuotaRemaining <= 0 {
		t.Fatalf("10-day/health80 account should have business quota remaining, got %d", r.BusinessQuotaRemaining)
	}
}
```

- [ ] **Step 2: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestStoreListJoinsDevice -v`
Expected: 编译失败（`List`/`ListFilter`/`ListRow` 未定义）。

- [ ] **Step 3: 写实现（list.go）**

`internal/warmup/list.go`:
```go
package warmup

import (
	"context"
	"strings"
	"time"
)

// ListRow 是养号中心列表一行(warmup_profiles LEFT JOIN account_devices)。
type ListRow struct {
	AccountJID             string
	TenantID               int64
	Lane                   Lane
	Stage                  Stage
	WarmupMessagesSent     int
	RepliesReceived        int
	OnlineHours            float64
	Health                 int
	RegisteredAt           *time.Time
	BusinessQuotaRemaining int
	Paused                 bool
}

type ListFilter struct {
	Stage string
	Lane  string
	Q     string
	Limit int
	Offset int
}

// List 返回分页养号行 + 总数。BusinessQuotaRemaining 复用 SQL effective_quota。
func (s *Store) List(ctx context.Context, f ListFilter) ([]ListRow, int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	var where []string
	var args []any
	add := func(cond string, v any) { args = append(args, v); where = append(where, cond) }
	if f.Stage != "" {
		add("wp.stage=$"+itoa(len(args)+1), f.Stage)
	}
	if f.Lane != "" {
		add("wp.lane=$"+itoa(len(args)+1), f.Lane)
	}
	if f.Q != "" {
		add("wp.account_jid ILIKE $"+itoa(len(args)+1), "%"+f.Q+"%")
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM warmup_profiles wp`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := `
SELECT wp.account_jid, wp.tenant_id, wp.lane, wp.stage, wp.warmup_messages_sent,
       wp.replies_received,
       COALESCE(EXTRACT(EPOCH FROM (now()-wp.online_since))/3600, 0) AS online_hours,
       COALESCE(d.health_score, 0), d.registered_at,
       GREATEST(COALESCE(effective_quota(d.registered_at, d.health_score) - d.sent_today, 0), 0),
       wp.paused
  FROM warmup_profiles wp
  LEFT JOIN account_devices d ON d.account_jid = wp.account_jid` + cond +
		` ORDER BY wp.updated_at DESC LIMIT $` + itoa(len(args)+1) + ` OFFSET $` + itoa(len(args)+2)
	args = append(args, f.Limit, f.Offset)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []ListRow
	for rows.Next() {
		var r ListRow
		var lane, stage string
		if err := rows.Scan(&r.AccountJID, &r.TenantID, &lane, &stage, &r.WarmupMessagesSent,
			&r.RepliesReceived, &r.OnlineHours, &r.Health, &r.RegisteredAt, &r.BusinessQuotaRemaining, &r.Paused); err != nil {
			return nil, 0, err
		}
		r.Lane, r.Stage = Lane(lane), Stage(stage)
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// Overview 是养号总览卡数据。
type Overview struct {
	New     int `json:"new"`
	Warming int `json:"warming"`
	Mature  int `json:"mature"`
	Paused  int `json:"paused"`
}

func (s *Store) Overview(ctx context.Context) (Overview, error) {
	var o Overview
	err := s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE stage='NEW'),
       count(*) FILTER (WHERE stage='WARMING'),
       count(*) FILTER (WHERE stage='MATURE'),
       count(*) FILTER (WHERE paused)
  FROM warmup_profiles`).Scan(&o.New, &o.Warming, &o.Mature, &o.Paused)
	return o, err
}

// UpsertPolicy 写一条车道策略(后台热改)。
func (s *Store) UpsertPolicy(ctx context.Context, lane Lane, p Policy, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO warmup_policies (lane, min_warmup_messages, min_replies, min_online_hours,
	warming_cap, mature_base_cap, mature_max_cap, mature_ramp_step, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (lane) DO UPDATE SET min_warmup_messages=EXCLUDED.min_warmup_messages,
	min_replies=EXCLUDED.min_replies, min_online_hours=EXCLUDED.min_online_hours,
	warming_cap=EXCLUDED.warming_cap, mature_base_cap=EXCLUDED.mature_base_cap,
	mature_max_cap=EXCLUDED.mature_max_cap, mature_ramp_step=EXCLUDED.mature_ramp_step,
	updated_at=EXCLUDED.updated_at`,
		string(lane), p.MinWarmupMessages, p.MinReplies, p.MinOnlineHours,
		p.WarmingCap, p.MatureBaseCap, p.MatureMaxCap, p.MatureRampStep, now)
	return err
}

func (s *Store) ListPolicies(ctx context.Context) (map[Lane]Policy, error) {
	rows, err := s.pool.Query(ctx, `
SELECT lane, min_warmup_messages, min_replies, min_online_hours, warming_cap,
	mature_base_cap, mature_max_cap, mature_ramp_step FROM warmup_policies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Lane]Policy{}
	for rows.Next() {
		var lane string
		var p Policy
		if err := rows.Scan(&lane, &p.MinWarmupMessages, &p.MinReplies, &p.MinOnlineHours,
			&p.WarmingCap, &p.MatureBaseCap, &p.MatureMaxCap, &p.MatureRampStep); err != nil {
			return nil, err
		}
		out[Lane(lane)] = p
	}
	return out, rows.Err()
}

// itoa 是最小无依赖整数转字符串(避免 import strconv 仅为拼参数号)。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
```

- [ ] **Step 4: 跑 list 测试验证通过**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestStoreListJoinsDevice -v`
Expected: PASS。

- [ ] **Step 5: 写 API handler 测试**

`internal/api/warmup_api_test.go`（照 `audit_db_test.go` 起真实 Server + pg 的方式；若包内已有 `newTestServer`/`doJSON` helper 则复用，否则本测试用最简 httptest + 直接调 handler）。核心断言：list 返回 JSON 含 rows/total；action pause 后 profile.paused=true；PUT policy 改 warming_cap 后 GET 反映新值。

```go
package api

import (
	"net/http"
	"testing"
)

func TestWarmupListAndPauseAction(t *testing.T) {
	s, ctx := newWarmupTestServer(t) // helper: 起 pg + Server + Deps.Warmup
	seedWarmupAccount(t, ctx, s, "wa@s.whatsapp.net")

	// list
	w := doJSON(t, s, s.handleAdminWarmupList, "GET", "/admin/warmup", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list want 200 got %d: %s", w.Code, w.Body.String())
	}
	// pause action
	w2 := doJSON(t, s, s.handleAdminWarmupAction, "POST", "/admin/warmup/wa@s.whatsapp.net/action",
		map[string]any{"action": "pause"})
	if w2.Code != http.StatusOK {
		t.Fatalf("pause want 200 got %d: %s", w2.Code, w2.Body.String())
	}
	// 验库
	p, _ := s.deps.Warmup.Store().Get(ctx, "wa@s.whatsapp.net")
	if !p.Paused {
		t.Fatalf("expected paused after action")
	}
}
```

> 注：需给 `warmup.Service` 加一个 `Store() *Store` 访问器（测试与 handler 都要读库），以及本测试文件里的 helper `newWarmupTestServer`/`seedWarmupAccount`/`doJSON`（`doJSON` 若包内已存在同名 helper 直接复用；养号列表 handler 从 `c.Param("jid")` 取号，注意 jid 含 `@`，路由用通配 `*jid` 或 query 传参见实现）。

- [ ] **Step 6: 写 API handler 实现**

`internal/api/warmup_api.go`:
```go
// internal/api/warmup_api.go
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/acme/wadist/internal/warmup"
)

// handleAdminWarmupList: GET /admin/warmup — 分页养号行(join account_devices)。
func (s *Server) handleAdminWarmupList(c *gin.Context) {
	if s.deps.Warmup == nil {
		c.JSON(http.StatusOK, gin.H{"rows": []any{}, "total": 0})
		return
	}
	f := warmup.ListFilter{
		Stage: c.Query("stage"), Lane: c.Query("lane"), Q: c.Query("q"),
	}
	f.Limit = atoiDefault(c.Query("limit"), 50)
	f.Offset = atoiDefault(c.Query("offset"), 0)
	rows, total, err := s.deps.Warmup.Store().List(c.Request.Context(), f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rows": rows, "total": total})
}

// handleAdminWarmupOverview: GET /admin/warmup/overview。
func (s *Server) handleAdminWarmupOverview(c *gin.Context) {
	if s.deps.Warmup == nil {
		c.JSON(http.StatusOK, warmup.Overview{})
		return
	}
	o, err := s.deps.Warmup.Store().Overview(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, o)
}

// handleAdminWarmupAction: POST /admin/warmup/*jid/action {action,lane?}。
func (s *Server) handleAdminWarmupAction(c *gin.Context) {
	if s.deps.Warmup == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "warmup disabled"})
		return
	}
	jid := trimSlashParam(c.Param("jid"))
	var body struct {
		Action string `json:"action"`
		Lane   string `json:"lane"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad body"})
		return
	}
	ctx := c.Request.Context()
	var err error
	switch body.Action {
	case "pause":
		err = s.deps.Warmup.SetPaused(ctx, jid, true)
	case "resume":
		err = s.deps.Warmup.SetPaused(ctx, jid, false)
	case "promote":
		err = s.deps.Warmup.ForcePromote(ctx, jid)
	case "demote":
		err = s.deps.Warmup.Demote(ctx, jid, "manual")
	case "lane":
		err = s.deps.Warmup.SetLane(ctx, jid, warmup.Lane(body.Lane))
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown action"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.recordAudit(c, "warmup."+body.Action, jid)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleAdminWarmupGetPolicies: GET /admin/warmup/policies。
func (s *Server) handleAdminWarmupGetPolicies(c *gin.Context) {
	if s.deps.Warmup == nil {
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	m, err := s.deps.Warmup.Store().ListPolicies(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, m)
}

// handleAdminWarmupSetPolicy: PUT /admin/warmup/policies/:lane。
func (s *Server) handleAdminWarmupSetPolicy(c *gin.Context) {
	if s.deps.Warmup == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "warmup disabled"})
		return
	}
	lane := warmup.Lane(c.Param("lane"))
	var p warmup.Policy
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad body"})
		return
	}
	if err := s.deps.Warmup.Store().UpsertPolicy(c.Request.Context(), lane, p, timeNow()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	s.recordAudit(c, "warmup.policy", string(lane))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
```

> 实现补充：
> - `warmup.Service` 加 `func (s *Service) Store() *Store { return s.store }`。
> - `atoiDefault(str string, def int) int`、`trimSlashParam(string) string`（去掉 gin `*jid` 通配前导 `/`）、`timeNow()`：若包内已有等价 helper 则复用；否则在 `warmup_api.go` 内定义最小实现。`s.recordAudit(c, action, target)`：复用现有审计 helper（见 `agent_admin.go`/`instances_api.go` 的调用；若签名不同按现有签名适配）。

- [ ] **Step 7: 路由注册**

在 `internal/api/router.go` 的 `admin` 组内（Task 6 已加 `Deps.Warmup`）追加：
```go
		admin.GET("/warmup", s.handleAdminWarmupList)
		admin.GET("/warmup/overview", s.handleAdminWarmupOverview)
		admin.GET("/warmup/policies", s.handleAdminWarmupGetPolicies)
		admin.PUT("/warmup/policies/:lane", s.handleAdminWarmupSetPolicy)
		admin.POST("/warmup/*jid", s.handleAdminWarmupActionRoute) // *jid 尾含 /action
```
> jid 含 `@` 与点，用 gin 通配 `*jid` 捕获 `"/wa@s.whatsapp.net/action"`；`handleAdminWarmupActionRoute` 从中剥出 jid 与末段 `action` 校验后转调 `handleAdminWarmupAction`。或更简单：改用 query 传 jid（`POST /admin/warmup/action?jid=...`），避免通配歧义——**采用 query 方案**：路由 `admin.POST("/warmup/action", s.handleAdminWarmupAction)`，handler 从 `c.Query("jid")` 取号。前端（Task 9）按此对齐。

- [ ] **Step 8: 跑测试验证通过**

Run: `cd /var/klwa && go build ./... && go test ./internal/api/ -run TestWarmup -v && go test ./internal/warmup/ -v`
Expected: PASS。

- [ ] **Step 9: 提交**

```bash
git add internal/api/warmup_api.go internal/warmup/list.go internal/api/warmup_api_test.go internal/warmup/list_test.go internal/api/router.go internal/warmup/service.go
git commit -m "feat(p5a): admin warmup API (list/overview/actions/policies)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 8: worker 构造 warmup.Service 并注入 console Deps

**Files:**
- Modify: `cmd/wadist/main.go`（构造 `warmup.NewStore(mgr.SystemPool())` + `warmup.NewService`，注入 `api.Deps.Warmup`；console 与 worker 同进程或分进程按现状——本 Task 只保证 console 侧 Deps 有 Warmup）
- Modify: `cmd/console/main.go`（若 console 是独立进程，同样构造并注入）
- Test: 手动 `go build ./...`（接线无独立单测，靠编译 + 既有 API 测试覆盖）

**Interfaces:**
- Consumes: `warmup.NewStore`,`warmup.NewService`（Task 4/5）、`api.Deps.Warmup`（Task 6）。
- Produces: 运行时 `Deps.Warmup` 非 nil。

- [ ] **Step 1: 找到 console Server 构造点**

Run: `cd /var/klwa && grep -rn "api.Deps{\|api.NewServer\|Deps{" cmd/console/main.go cmd/wadist/main.go`
Expected: 定位 `api.Deps{...}` 字面量。

- [ ] **Step 2: 注入 warmup service**

在 console Server 构造处（`api.Deps{...}` 字面量）加字段：
```go
		Warmup: warmup.NewService(
			warmup.NewStore(mgr.SystemPool()),
			func() time.Time { return time.Now() },
		),
```
并 `import "github.com/acme/wadist/internal/warmup"`。

- [ ] **Step 3: 编译验证**

Run: `cd /var/klwa && go build ./...`
Expected: 成功，无未用 import。

- [ ] **Step 4: 提交**

```bash
git add cmd/console/main.go cmd/wadist/main.go
git commit -m "feat(p5a): wire warmup.Service into console Deps

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 9: 前端养号中心页 `/admin/warmup`

**Files:**
- Create: `frontend/app/admin/warmup/page.tsx`
- Create: `frontend/components/admin-warmup.tsx`
- Modify: `frontend/lib/i18n/dicts/admin.ts`（补 warmup 段 zh/en key）
- Modify: 侧边导航（找现有 admin 导航配置，加"养号中心"入口）
- Test: `cd frontend && node scripts/check-i18n-keys.mjs` + `npm run build`

**Interfaces:**
- Consumes: API `GET /admin/warmup`、`GET /admin/warmup/overview`、`POST /admin/warmup/action?jid=`、`GET /admin/warmup/policies`、`PUT /admin/warmup/policies/:lane`（Task 7）。
- Produces: 页面 + 组件（复用 `ProDataTable`/`StatCard`/`StatusBadge`/`useT`，镜像 `admin-instances.tsx` 结构）。

- [ ] **Step 1: 先读 Next.js 改版指南**

Run: `cd /var/klwa/frontend && ls node_modules/next/dist/docs/ && sed -n '1,60p' node_modules/next/dist/docs/*app-router* 2>/dev/null | head -60`
（确认 app 目录 page/client component 约定后再写。）

- [ ] **Step 2: 写页面（server component 壳）**

`frontend/app/admin/warmup/page.tsx`（镜像 `app/admin/instances/page.tsx`）：
```tsx
import { PageHeader } from "@/components/admin/page-header";
import { AdminWarmup } from "@/components/admin-warmup";

export default function AdminWarmupPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Resources"
        title={{ zh: "养号中心", en: "Warmup Center" }}
        description={{
          zh: "新号养号全生命周期:阶段/车道/号龄/健康度/养号进度、手动控制与车道策略配置。",
          en: "New-account warmup lifecycle: stage/lane/age/health/progress, manual controls, and lane policy config.",
        }}
      />
      <AdminWarmup />
    </div>
  );
}
```

- [ ] **Step 3: 写组件（client component）**

`frontend/components/admin-warmup.tsx`：镜像 `admin-instances.tsx`——`"use client"`，`useT()`，`api.get("/admin/warmup?...")` 拉列表 + `/admin/warmup/overview`，`ProDataTable`（columns: jid / lane / stage(StatusBadge) / 号龄 / health / 进度 `msgs/replies/onlineH` / 业务配额剩余 / paused），行内 DropdownMenu 动作（暂停/恢复/强制毕业/降级/改车道 → `api.post("/admin/warmup/action", {jid, action, lane})`），顶部 `MetricCardGroup` 展示 overview（NEW/WARMING/MATURE/paused 计数）。StatusBadge tone 映射：NEW=neutral，WARMING=warning，MATURE=success。

> 完整 JSX 按 `admin-instances.tsx` 的既定写法产出（列定义、DropdownMenu、toast、loading/error 态）。所有可见文案用 `t("admin.warmup.<key>")`，key 在 Step 4 落字典。动作成功后 `toast.success(t(...))` 并重拉列表。

- [ ] **Step 4: 补 i18n key（zh/en）**

在 `frontend/lib/i18n/dicts/admin.ts` 加 `warmup` 段（每个 key 同时给 zh/en），至少覆盖：`title/subtitle/stage.new/stage.warming/stage.mature/lane.fast/lane.standard/col.jid/col.age/col.health/col.progress/col.quota/col.paused/action.pause/action.resume/action.promote/action.demote/action.lane/overview.new/overview.warming/overview.mature/overview.paused/policy.title` 等。zh 用中文、en 用英文，值不得为占位。

- [ ] **Step 5: 加导航入口**

Run: `cd /var/klwa/frontend && grep -rln "admin/instances\|admin/resources" app/admin/layout.tsx components/admin/*.tsx | head`
在找到的导航配置里，"实例/资源"附近加 `{ href: "/admin/warmup", label: { zh: "养号中心", en: "Warmup" } }`（按现有导航项结构对齐）。

- [ ] **Step 6: 校验**

Run: `cd /var/klwa/frontend && node scripts/check-i18n-keys.mjs && npm run build`
Expected: i18n gate 通过（无缺失 literal key）；build 成功。人工核对 labelKey/map 间接 key（gate 盲区）无遗漏。

- [ ] **Step 7: 提交**

```bash
git add frontend/app/admin/warmup frontend/components/admin-warmup.tsx frontend/lib/i18n/dicts/admin.ts frontend/app/admin/layout.tsx
git commit -m "feat(p5a): warmup center admin page (list/overview/actions, i18n zh/en)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 10: 前端车道策略配置页

**Files:**
- Create: `frontend/components/admin-warmup-policies.tsx`
- Modify: `frontend/components/admin-warmup.tsx`（加"策略配置"Tab 或按钮打开 Dialog）
- Modify: `frontend/lib/i18n/dicts/admin.ts`（policy 表单字段 key）
- Test: `node scripts/check-i18n-keys.mjs` + `npm run build`

**Interfaces:**
- Consumes: `GET /admin/warmup/policies`、`PUT /admin/warmup/policies/:lane`。
- Produces: 策略编辑 UI（FAST/STANDARD 两车道，7 个数值字段可编辑保存）。

- [ ] **Step 1: 写策略组件**

`frontend/components/admin-warmup-policies.tsx`：`"use client"`，拉 `GET /admin/warmup/policies` → 渲染 FAST/STANDARD 两组数值输入（minWarmupMessages/minReplies/minOnlineHours/warmingCap/matureBaseCap/matureMaxCap/matureRampStep），"保存"→ `api.put("/admin/warmup/policies/"+lane, {...})` → toast + 重拉。字段标签走 i18n。数值校验：非负整数，matureMaxCap ≥ matureBaseCap（前端提示，后端不强制）。

- [ ] **Step 2: 在养号中心接入入口**

在 `admin-warmup.tsx` 顶部工具栏加"策略配置"按钮，点开 Dialog 内嵌 `AdminWarmupPolicies`（照 `admin-instance-wizard.tsx` 的 Dialog 用法）。

- [ ] **Step 3: 补 i18n key**

`admin.ts` warmup 段补 `policy.minWarmupMessages/minReplies/minOnlineHours/warmingCap/matureBaseCap/matureMaxCap/matureRampStep/save/saved` 等 zh/en。

- [ ] **Step 4: 校验 + 提交**

Run: `cd /var/klwa/frontend && node scripts/check-i18n-keys.mjs && npm run build`
Expected: 通过。
```bash
git add frontend/components/admin-warmup-policies.tsx frontend/components/admin-warmup.tsx frontend/lib/i18n/dicts/admin.ts
git commit -m "feat(p5a): warmup lane policy config UI (hot-editable thresholds)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

**✅ P5a 里程碑：** 养号中心可视化 + 手动控制 + 新号自动 enroll + 策略热配全部可用，不依赖账号池。此处应跑一次全量 `go test ./... && cd frontend && npm run build`，并做一次全分支复审（subagent-driven 的 whole-branch review）后再进 P5b。

---

# 阶段 P5b — 主动养号真跑（需账号池才真跑）

## Task 11: 脚本库加载与选择 `scripts.go`

**Files:**
- Create: `internal/warmup/scripts.go`
- Test: `internal/warmup/scripts_test.go`

**Interfaces:**
- Consumes: `Store.pool`（Task 4）。
- Produces:
  - `type Turn struct{From string `json:"from"`; Text string `json:"text"`}`
  - `type Script struct{ID int64; Lang string; Turns []Turn}`
  - `func (s *Store) LoadScripts(ctx, lang string) ([]Script, error)`（enabled，按 lang 过滤；lang 空取全部）
  - `func PickScript(scripts []Script, rng *rand.Rand) (Script, bool)`
  - `func RenderTurn(t Turn, rng *rand.Rand) string`（替换 `{name}`/`{emoji}` 为随机候选）

- [ ] **Step 1: 写失败测试**

`internal/warmup/scripts_test.go`:
```go
package warmup

import (
	"math/rand"
	"strings"
	"testing"
)

func TestLoadAndPickScript(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	scripts, err := st.LoadScripts(ctx, "pt")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("load pt scripts: n=%d err=%v", len(scripts), err)
	}
	if len(scripts[0].Turns) < 2 {
		t.Fatalf("seeded pt script should be multi-turn, got %d turns", len(scripts[0].Turns))
	}
	rng := rand.New(rand.NewSource(42))
	sc, ok := PickScript(scripts, rng)
	if !ok || len(sc.Turns) == 0 {
		t.Fatalf("pick failed")
	}
}

func TestRenderTurnSubstitutes(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	out := RenderTurn(Turn{From: "A", Text: "oi {name} {emoji}"}, rng)
	if strings.Contains(out, "{name}") || strings.Contains(out, "{emoji}") {
		t.Fatalf("placeholders not substituted: %q", out)
	}
}
```

- [ ] **Step 2: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/warmup/ -run 'TestLoadAndPickScript|TestRenderTurn' -v`
Expected: 编译失败（未定义）。

- [ ] **Step 3: 写实现**

`internal/warmup/scripts.go`:
```go
package warmup

import (
	"context"
	"encoding/json"
	"math/rand"
	"strings"
)

type Turn struct {
	From string `json:"from"`
	Text string `json:"text"`
}

type Script struct {
	ID    int64
	Lang  string
	Turns []Turn
}

// LoadScripts 读 enabled 脚本;lang 非空按 lang 过滤,空取全部。
func (s *Store) LoadScripts(ctx context.Context, lang string) ([]Script, error) {
	q := `SELECT id, lang, turns FROM warmup_scripts WHERE enabled`
	args := []any{}
	if lang != "" {
		q += ` AND lang=$1`
		args = append(args, lang)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Script
	for rows.Next() {
		var sc Script
		var raw []byte
		if err := rows.Scan(&sc.ID, &sc.Lang, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &sc.Turns); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// PickScript 随机挑一套脚本。
func PickScript(scripts []Script, rng *rand.Rand) (Script, bool) {
	if len(scripts) == 0 {
		return Script{}, false
	}
	return scripts[rng.Intn(len(scripts))], true
}

var nameCandidates = []string{"", "amigo", "tudo", "man", "cara"}
var emojiCandidates = []string{"🙂", "👍", "😄", "🤝", "✌️"}

// RenderTurn 替换占位符为随机候选(空名去掉多余空格)。
func RenderTurn(t Turn, rng *rand.Rand) string {
	out := t.Text
	out = strings.ReplaceAll(out, "{name}", nameCandidates[rng.Intn(len(nameCandidates))])
	out = strings.ReplaceAll(out, "{emoji}", emojiCandidates[rng.Intn(len(emojiCandidates))])
	return strings.TrimSpace(strings.ReplaceAll(out, "  ", " "))
}
```

- [ ] **Step 4: 跑测试验证通过**

Run: `cd /var/klwa && go test ./internal/warmup/ -run 'TestLoadAndPickScript|TestRenderTurn' -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/warmup/scripts.go internal/warmup/scripts_test.go
git commit -m "feat(p5b): warmup script library (load/pick/render multi-turn scripts)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 12: Evolution `SendTyping` 端点落地

**Files:**
- Modify: `internal/cluster/evolution_client.go`（把 SendTyping/SetPresence 的 TODO 实做）
- Test: `internal/cluster/evolution_client_test.go`（httptest 假 Evolution，验请求方法/路径/body）

**Interfaces:**
- Consumes: 现有 `EvolutionClient`（`SendText` 的 HTTP 范式）。
- Produces: `func (c *EvolutionClient) SendTyping(ctx, instanceName, to string, durationMs int) error`。

- [ ] **Step 1: 读现有 SendText + TODO**

Run: `cd /var/klwa && grep -n "SendText\|SendTyping\|SetPresence\|presence\|TODO.*[Vv]erif" internal/cluster/evolution_client.go`
（对齐 base URL / apikey header / JSON body 的既有写法。）

- [ ] **Step 2: 写失败测试**

`internal/cluster/evolution_client_test.go` 加：
```go
func TestSendTyping(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvolutionClient(srv.URL, "key", "") // 按现有构造签名适配
	if err := c.SendTyping(context.Background(), "inst-1", "5511999@s.whatsapp.net", 3000); err != nil {
		t.Fatalf("send typing: %v", err)
	}
	if !strings.Contains(gotPath, "inst-1") {
		t.Fatalf("path missing instance: %s", gotPath)
	}
	if !strings.Contains(gotBody, "composing") && !strings.Contains(gotBody, "5511999") {
		t.Fatalf("body wrong: %s", gotBody)
	}
}
```

- [ ] **Step 3: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/cluster/ -run TestSendTyping -v`
Expected: 编译失败（`SendTyping` 未定义）。

- [ ] **Step 4: 写实现**

按 Evolution v2 presence API（`POST /chat/sendPresence/{instance}` body `{"number":to,"presence":"composing","delay":durationMs}`；确切路径以 `SendText` 同款 base + 已验证的 Evolution v2 路由为准，参考 `docs/evolution-real-machine-verification-runbook.md`）实做 `SendTyping`，复用 `SendText` 的 request/header/error 处理封装（若有私有 `do()`/`post()` 助手则复用）。

- [ ] **Step 5: 跑测试验证通过**

Run: `cd /var/klwa && go test ./internal/cluster/ -run TestSendTyping -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/cluster/evolution_client.go internal/cluster/evolution_client_test.go
git commit -m "feat(p5b): Evolution SendTyping (composing presence) for warmup cadence

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 13: `PairAndWarm` 完整实现（配对 + 多轮 + 抖动 + 打字态）

**Files:**
- Create: `internal/warmup/pair.go`
- Test: `internal/warmup/pair_test.go`

**Interfaces:**
- Consumes: `Store`（ListByStage/LoadScripts/PolicyFor/Save）、`Script`/`Turn`/`PickScript`/`RenderTurn`（Task 11）、`DailyCap`（Task 3）。
- Produces:
  - `type Sender interface{ SendText(ctx context.Context, instanceName, to, text string) error; SendTyping(ctx context.Context, instanceName, to string, durationMs int) error }`
  - `type Account struct{JID, InstanceName, PhoneNumber, Lang string; Online bool}`
  - `type AccountLookup interface{ WarmupAccount(ctx context.Context, jid string) (Account, error) }`
  - `type Sleeper func(d time.Duration)`（测试注入 no-op）
  - `func (s *Service) PairAndWarm(ctx context.Context, batch int, rng *rand.Rand, sleep Sleeper) (pairs int, messages int, err error)`
  - Service 需持有 `sender Sender`、`accounts AccountLookup`（经 `NewService` 扩展或 `WithSender/WithAccounts` 注入——为不破坏 Task 5 签名，用 `WithSender(sender)`/`WithAccounts(lookup)` 链式 setter）。

- [ ] **Step 1: 写失败测试**

`internal/warmup/pair_test.go`:
```go
package warmup

import (
	"context"
	"math/rand"
	"testing"
	"time"
)

type fakeSender struct{ sent []string; typing int }

func (f *fakeSender) SendText(_ context.Context, inst, to, text string) error {
	f.sent = append(f.sent, inst+"->"+to+":"+text)
	return nil
}
func (f *fakeSender) SendTyping(_ context.Context, _, _ string, _ int) error { f.typing++; return nil }

type fakeAccounts map[string]Account

func (m fakeAccounts) WarmupAccount(_ context.Context, jid string) (Account, error) {
	a, ok := m[jid]
	if !ok {
		return Account{}, ErrNotFound
	}
	return a, nil
}

func TestPairAndWarm(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	for _, jid := range []string{"pa@s.whatsapp.net", "pb@s.whatsapp.net"} {
		_ = st.EnrollIfAbsent(ctx, jid, 1, LaneStandard, now)
	}
	accts := fakeAccounts{
		"pa@s.whatsapp.net": {JID: "pa@s.whatsapp.net", InstanceName: "ia", PhoneNumber: "551100", Lang: "pt", Online: true},
		"pb@s.whatsapp.net": {JID: "pb@s.whatsapp.net", InstanceName: "ib", PhoneNumber: "551101", Lang: "pt", Online: true},
	}
	fs := &fakeSender{}
	svc := NewService(st, fixedClock(now)).WithSender(fs).WithAccounts(accts)

	pairs, msgs, err := svc.PairAndWarm(ctx, 10, rand.New(rand.NewSource(7)), func(time.Duration) {})
	if err != nil || pairs != 1 || msgs < 2 {
		t.Fatalf("pair want 1 pair >=2 msgs got %d/%d err=%v", pairs, msgs, err)
	}
	if len(fs.sent) < 2 {
		t.Fatalf("expected >=2 sends, got %d", len(fs.sent))
	}
	if fs.typing == 0 {
		t.Fatalf("expected typing presence before sends")
	}
	// 计数已 bump
	p, _ := st.Get(ctx, "pa@s.whatsapp.net")
	if p.WarmupMessagesSent == 0 || p.WarmupSentToday == 0 {
		t.Fatalf("counters not bumped: %+v", p)
	}
}

func TestPairAndWarmSkipsOffline(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	_ = st.EnrollIfAbsent(ctx, "on@s.whatsapp.net", 1, LaneStandard, now)
	_ = st.EnrollIfAbsent(ctx, "off@s.whatsapp.net", 1, LaneStandard, now)
	accts := fakeAccounts{
		"on@s.whatsapp.net":  {JID: "on@s.whatsapp.net", InstanceName: "i1", PhoneNumber: "1", Lang: "pt", Online: true},
		"off@s.whatsapp.net": {JID: "off@s.whatsapp.net", InstanceName: "i2", PhoneNumber: "2", Lang: "pt", Online: false},
	}
	svc := NewService(st, fixedClock(now)).WithSender(&fakeSender{}).WithAccounts(accts)
	pairs, _, err := svc.PairAndWarm(ctx, 10, rand.New(rand.NewSource(1)), func(time.Duration) {})
	if err != nil || pairs != 0 {
		t.Fatalf("offline account must not pair: pairs=%d err=%v", pairs, err)
	}
}
```

- [ ] **Step 2: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestPairAndWarm -v`
Expected: 编译失败（`WithSender`/`WithAccounts`/`PairAndWarm` 未定义）。

- [ ] **Step 3: 写实现**

`internal/warmup/pair.go`:
```go
package warmup

import (
	"context"
	"math/rand"
	"time"
)

// Sender 抽象 Evolution 发送(SendText + 打字态)。
type Sender interface {
	SendText(ctx context.Context, instanceName, to, text string) error
	SendTyping(ctx context.Context, instanceName, to string, durationMs int) error
}

// Account 是养号互发所需的账号连接信息。
type Account struct {
	JID          string
	InstanceName string
	PhoneNumber  string
	Lang         string
	Online       bool
}

// AccountLookup 解析 jid → 连接信息(由 store.Manager 实现,见 Task 15 适配器)。
type AccountLookup interface {
	WarmupAccount(ctx context.Context, jid string) (Account, error)
}

// Sleeper 抽象抖动等待,测试注入 no-op。
type Sleeper func(d time.Duration)

// jitter 返回 3–90s 的随机间隔。
func jitter(rng *rand.Rand) time.Duration {
	return time.Duration(3+rng.Intn(88)) * time.Second
}

// WithSender/WithAccounts 注入 PairAndWarm 依赖(不破坏 NewService 签名)。
func (s *Service) WithSender(sender Sender) *Service   { s.sender = sender; return s }
func (s *Service) WithAccounts(l AccountLookup) *Service { s.accounts = l; return s }

// PairAndWarm 拉 WARMING 池,过滤(ONLINE + 未暂停 + 当日未超 warmingCap),两两配对,
// 每对随机挑脚本、逐句带打字态与随机抖动交替发送,并 bump 计数。返回配对数与消息数。
func (s *Service) PairAndWarm(ctx context.Context, batch int, rng *rand.Rand, sleep Sleeper) (int, int, error) {
	if s.sender == nil || s.accounts == nil {
		return 0, 0, nil // 未注入依赖(P5a-only 运行),空跑
	}
	profiles, err := s.store.ListByStage(ctx, StageWarming, batch)
	if err != nil {
		return 0, 0, err
	}
	now := s.clock()
	today := now.Truncate(24 * time.Hour)

	type eligible struct {
		p   Profile
		acc Account
	}
	var pool []eligible
	for _, p := range profiles {
		if p.Paused {
			continue
		}
		acc, err := s.accounts.WarmupAccount(ctx, p.AccountJID)
		if err != nil || !acc.Online {
			continue
		}
		pol, err := s.store.PolicyFor(ctx, p.Lane)
		if err != nil {
			continue
		}
		sentToday := p.WarmupSentToday
		if p.WarmupSentDate == nil || !p.WarmupSentDate.Equal(today) {
			sentToday = 0
		}
		if sentToday >= DailyCap(pol, StageWarming, 0) {
			continue
		}
		pool = append(pool, eligible{p: p, acc: acc})
	}

	pairs, messages := 0, 0
	for i := 0; i+1 < len(pool); i += 2 {
		a, b := pool[i], pool[i+1]
		scripts, err := s.store.LoadScripts(ctx, a.acc.Lang)
		if err != nil || len(scripts) == 0 {
			continue // 无匹配脚本:跳过(不 fallback 到写死单句)
		}
		sc, ok := PickScript(scripts, rng)
		if !ok {
			continue
		}
		for _, turn := range sc.Turns {
			var fromAcc, toAcc Account
			if turn.From == "A" {
				fromAcc, toAcc = a.acc, b.acc
			} else {
				fromAcc, toAcc = b.acc, a.acc
			}
			text := RenderTurn(turn, rng)
			_ = s.sender.SendTyping(ctx, fromAcc.InstanceName, toAcc.PhoneNumber, 1500+rng.Intn(2500))
			sleep(jitter(rng))
			if err := s.sender.SendText(ctx, fromAcc.InstanceName, toAcc.PhoneNumber, text); err != nil {
				continue // 单句失败跳过,不中断整批
			}
			messages++
		}
		s.bumpSent(ctx, a.p, today, now)
		s.bumpSent(ctx, b.p, today, now)
		pairs++
	}
	return pairs, messages, nil
}

// bumpSent 累加养号发送计数(warmup_messages_sent + 当日 warmup_sent_today)。
func (s *Service) bumpSent(ctx context.Context, p Profile, today, now time.Time) {
	if p.WarmupSentDate == nil || !p.WarmupSentDate.Equal(today) {
		p.WarmupSentToday = 0
	}
	p.WarmupMessagesSent++
	p.WarmupSentToday++
	d := today
	p.WarmupSentDate = &d
	_ = s.store.Save(ctx, p, now)
}
```
并在 `service.go` 的 `Service` struct 加字段 `sender Sender` 与 `accounts AccountLookup`。

- [ ] **Step 4: 跑测试验证通过**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestPairAndWarm -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/warmup/pair.go internal/warmup/service.go internal/warmup/pair_test.go
git commit -m "feat(p5b): PairAndWarm (paired multi-turn warmup, jitter + typing, cap-gated)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 14: webhook `messages.upsert` → RecordReply

**Files:**
- Modify: `internal/api/webhook_evolution.go`（加 `messages.upsert` case）
- Modify: `internal/api/webhook_evolution_types.go`（若入站字段解析缺失，补 `remoteJID()`/`isInbound()`）
- Test: `internal/api/webhook_reply_test.go`

**Interfaces:**
- Consumes: `warmup.Service.RecordReply`（Task 5）。
- Produces: `EvolutionWebhook` 用同一 `warmup` 字段（已在 Task 6 加）；新增 `warmupReplyRecorder` 能力（RecordReply）。

- [ ] **Step 1: 扩展 warmup 接口**

Task 6 的 `warmupEnroller` 只有 `Enroll`；这里让 webhook 也能 `RecordReply`。把字段类型保持 `*warmup.Service`（Task 6 Step 6 已收窄为具体类型），直接调 `h.warmup.RecordReply(...)`——无需新接口。

- [ ] **Step 2: 写失败测试**

`internal/api/webhook_reply_test.go`（fake 一个记录 RecordReply 的最小 service 替身不可行——字段是具体 `*warmup.Service`；故本测试起真实 pg + 真实 Service，enroll 两个池内号，投一条 inbound webhook，验收信方 replies_received+1）：
```go
package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMessagesUpsertRecordsWarmupReply(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, ctx := newWarmupTestServer(t) // 复用 Task 7 helper
	// enroll 池内两个号,并让 instance→jid 可解析(seed account_instances)。
	seedWarmupAccount(t, ctx, s, "recv@s.whatsapp.net")   // 绑定 instance "irecv"
	seedWarmupAccount(t, ctx, s, "send@s.whatsapp.net")   // 池内发信方

	wh := NewEvolutionWebhook("", s.deps.Receipt, s.deps.Mgr, nil, s.deps.QRCache).WithWarmup(s.deps.Warmup)
	r := gin.New()
	wh.Register(r)

	// inbound:实例 irecv 收到来自 send@ 的消息(fromMe=false)。
	body := `{"event":"messages.upsert","instance":"irecv","data":{"key":{"remoteJid":"send@s.whatsapp.net","fromMe":false,"id":"MID1"}}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	p, _ := s.deps.Warmup.Store().Get(ctx, "recv@s.whatsapp.net")
	if p.RepliesReceived != 1 {
		t.Fatalf("want replies_received=1 got %d", p.RepliesReceived)
	}
}
```
> `seedWarmupAccount` 需扩展为同时 seed `account_instances`(instance_name→jid 映射,供 `JIDForInstance` 解析)与 warmup profile。若 Task 7 的 helper 未 seed instance，本 Task 补上。

- [ ] **Step 3: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/api/ -run TestMessagesUpsertRecordsWarmupReply -v`
Expected: FAIL（无 `messages.upsert` case，replies 仍为 0）。

- [ ] **Step 4: 写实现**

在 `webhook_evolution.go` 的 `switch` 加：
```go
	case "messages.upsert":
		// 入站养号消息喂回复信号。messages.upsert 是 NESTED(data.key.{id,fromMe,
		// remoteJid});fromMe=true 是自己发的,不计。收信方=本实例 jid。
		if h.warmup == nil || w.Data.fromMe() {
			break
		}
		jid, ok, err := h.inst.JIDForInstance(ctx, w.Instance)
		if err != nil || !ok || jid == "" {
			break
		}
		// RecordReply 内部对非池内 jid 静默跳过(spec §5:只有池内号计信号)。
		_ = h.warmup.RecordReply(ctx, jid)
```
> 若 `w.Data.fromMe()` 对 messages.upsert 的 NESTED 结构解析不正确，在 `webhook_evolution_types.go` 校正 `fromMe()`（messages.upsert 用 `data.key.fromMe`，与 messages.update 的 FLAT `fromMe` 区分——参考该文件既有 doc comment）。

- [ ] **Step 5: 跑测试验证通过**

Run: `cd /var/klwa && go test ./internal/api/ -run TestMessagesUpsertRecordsWarmupReply -v`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/api/webhook_evolution.go internal/api/webhook_evolution_types.go internal/api/webhook_reply_test.go
git commit -m "feat(p5b): messages.upsert inbound -> warmup RecordReply signal

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 15: worker 养号 tick + AccountLookup 适配器 + 配置

**Files:**
- Create: `internal/warmup/runloop.go`（`RunLoop` 仿 `metrics.Sampler.RunLoop`）
- Create: `internal/warmup/lookup.go`（`store.Manager` → `AccountLookup` 适配器，或放 `internal/store`）
- Modify: `cmd/wadist/main.go`（构造 Service + Sender + Lookup，`sup.Go(runLoop)`）
- Modify: `internal/config/config.go`（加 `WarmupInterval`/`WarmupBatch`/`WarmupDefaultLane`）
- Test: `internal/warmup/runloop_test.go`

**Interfaces:**
- Consumes: `Service.PairAndWarm`/`EvaluateAndPromote`（Task 5/13）、`Sender`（Task 13，由 `cluster.EvolutionClient` 满足）、`AccountLookup`（Task 13）。
- Produces: `func (s *Service) TickOnce(ctx, rng, sleep) error`（一轮:先 promote 扫描再 PairAndWarm）、`func (s *Service) RunLoop(ctx, interval, rng) error`。

- [ ] **Step 1: 写失败测试**

`internal/warmup/runloop_test.go`:
```go
package warmup

import (
	"context"
	"math/rand"
	"testing"
	"time"
)

func TestTickOncePromotesAndPairs(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	// 一个够信号该毕业的号 + 一对能互发的号。
	_ = st.EnrollIfAbsent(ctx, "ripe@s.whatsapp.net", 1, LaneFast, now.Add(-3*time.Hour))
	rp, _ := st.Get(ctx, "ripe@s.whatsapp.net")
	rp.WarmupMessagesSent = 2 // FAST minMessages=2, minReplies=0, minOnlineHours=2
	_ = st.Save(ctx, rp, now)

	accts := fakeAccounts{"ripe@s.whatsapp.net": {JID: "ripe@s.whatsapp.net", InstanceName: "i", PhoneNumber: "1", Lang: "pt", Online: true}}
	svc := NewService(st, fixedClock(now)).WithSender(&fakeSender{}).WithAccounts(accts)

	if err := svc.TickOnce(ctx, rand.New(rand.NewSource(3)), func(time.Duration) {}); err != nil {
		t.Fatalf("tick: %v", err)
	}
	p, _ := st.Get(ctx, "ripe@s.whatsapp.net")
	if p.Stage != StageMature {
		t.Fatalf("ripe account should have matured, got %s", p.Stage)
	}
}
```

- [ ] **Step 2: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestTickOnce -v`
Expected: 编译失败（`TickOnce` 未定义）。

- [ ] **Step 3: 写实现**

`internal/warmup/runloop.go`:
```go
package warmup

import (
	"context"
	"log"
	"math/rand"
	"time"
)

// TickOnce 跑一轮养号:先毕业评估 WARMING 池,再 PairAndWarm。失败仅记日志、
// 不中断(养号故障绝不拖垮 worker,同 metrics.Sampler 理念)。
func (s *Service) TickOnce(ctx context.Context, rng *rand.Rand, sleep Sleeper) error {
	warming, err := s.store.ListByStage(ctx, StageWarming, 500)
	if err != nil {
		return err
	}
	for _, p := range warming {
		if _, err := s.EvaluateAndPromote(ctx, p.AccountJID); err != nil {
			log.Printf("warmup: promote %s: %v", p.AccountJID, err)
		}
	}
	pairs, msgs, err := s.PairAndWarm(ctx, 200, rng, sleep)
	if err != nil {
		return err
	}
	if pairs > 0 {
		log.Printf("warmup: paired %d, sent %d warmup messages", pairs, msgs)
	}
	return nil
}

// RunLoop 周期跑 TickOnce 直到 ctx 取消(仿 metrics.Sampler.RunLoop)。
func (s *Service) RunLoop(ctx context.Context, interval time.Duration, rng *rand.Rand) error {
	if interval <= 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := s.TickOnce(ctx, rng, time.Sleep); err != nil {
				log.Printf("warmup tick: %v", err)
			}
		}
	}
}
```

- [ ] **Step 4: 写 AccountLookup 适配器**

`internal/warmup/lookup.go`——定义查询，用 Service 已有的 `store.pool` 直接实现一个内置 lookup（避免跨包循环依赖 `internal/store`）：
```go
package warmup

import "context"

// DBLookup 用 warmup_profiles 关联的 account_devices/account_instances 解析连接信息。
// Lang 取自绑定代理的国家码映射(US->en, BR->pt, CN->zh, 其余->en)。
type DBLookup struct{ store *Store }

func NewDBLookup(store *Store) *DBLookup { return &DBLookup{store: store} }

func (l *DBLookup) WarmupAccount(ctx context.Context, jid string) (Account, error) {
	var a Account
	var country string
	var state *string
	err := l.store.pool.QueryRow(ctx, `
SELECT d.account_jid, COALESCE(ai.instance_name,''), COALESCE(d.phone_number,''),
       COALESCE(p.country_code,''), ai.state
  FROM account_devices d
  LEFT JOIN proxy_pool p ON p.id = d.proxy_id
  LEFT JOIN account_instances ai ON ai.jid = d.account_jid
 WHERE d.account_jid = $1`, jid).Scan(&a.JID, &a.InstanceName, &a.PhoneNumber, &country, &state)
	if err != nil {
		return Account{}, err
	}
	a.Lang = langForCountry(country)
	a.Online = state != nil && (*state == "open" || *state == "connected")
	return a, nil
}

func langForCountry(cc string) string {
	switch cc {
	case "BR", "PT":
		return "pt"
	case "CN", "HK", "TW":
		return "zh"
	default:
		return "en"
	}
}
```
> `phone_number` 若为加密列（见 0008 `phone_number_enc`），改用 `split_part(d.account_jid,'@',1)` 取号码（Evolution `to` 只需数字部分，与 FIX-3 `EnrollDeviceForInstance` 一致）。实现时以此为准，避免解密路径。

- [ ] **Step 5: 加配置字段**

`internal/config/config.go` 加（照现有 env 解析范式）：
```go
	WarmupInterval    time.Duration // WADIST_WARMUP_INTERVAL, 默认 5m, <=0 关
	WarmupGate        bool          // WADIST_WARMUP_GATE, 默认 false (P5c 用)
```
`WADIST_WARMUP_INTERVAL` 解析默认 `5m`；`WADIST_WARMUP_GATE` 解析 `on/true` → true。

- [ ] **Step 6: worker 挂载 tick**

`cmd/wadist/main.go` 在 metrics sampler 挂载附近加：
```go
	// 养号 tick(P5b):周期毕业评估 + 池内互发。SystemPool(跨租户);故障仅日志。
	warmupStore := warmup.NewStore(mgr.SystemPool())
	warmupSvc := warmup.NewService(warmupStore, func() time.Time { return time.Now() }).
		WithSender(evoClient). // *cluster.EvolutionClient 满足 warmup.Sender(SendText+SendTyping)
		WithAccounts(warmup.NewDBLookup(warmupStore))
	warmupRng := rand.New(rand.NewSource(time.Now().UnixNano()))
	sup.Go(func(lctx context.Context) error {
		return warmupSvc.RunLoop(lctx, cfg.WarmupInterval, warmupRng)
	})
	log.Printf("warmup engine on (interval=%s)", cfg.WarmupInterval)
```
> `evoClient` 是 worker 里已构造的 `*cluster.EvolutionClient`；若其 `SendText` 签名与 `warmup.Sender` 不完全一致，加薄适配器。`rand`/`time` import 若缺补上。

- [ ] **Step 7: 跑测试 + 编译**

Run: `cd /var/klwa && go test ./internal/warmup/ -run TestTickOnce -v && go build ./...`
Expected: PASS + 编译成功。

- [ ] **Step 8: 提交**

```bash
git add internal/warmup/runloop.go internal/warmup/lookup.go internal/config/config.go cmd/wadist/main.go
git commit -m "feat(p5b): warmup worker tick (RunLoop) + account lookup + config

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

**✅ P5b 里程碑：** 池内多轮脚本互发 + 打字态/抖动 + 回复信号闭环 + 定时 tick 全部就位。真机验证需先扫码接入 ≥2 个同国号（账号池），观察 `warmup_profiles` 计数增长与自动毕业。跑全量 `go test ./...` + 全分支复审后进 P5c。

---

# 阶段 P5c — 自动化闭环（MATURE 闸门 + 封号降级）

## Task 16: 业务发送 MATURE 闸门 + `WADIST_WARMUP_GATE` 开关

**Files:**
- Modify: `internal/dispatch/selectaccount.go`（按开关加 `stage='MATURE'` 过滤）
- Modify: `internal/dispatch/dispatcher.go`（`Dispatcher` 持 `warmupGate bool` 字段 + 构造注入）
- Modify: `cmd/wadist/main.go`（构造 Dispatcher 时传 `cfg.WarmupGate`）
- Test: `internal/dispatch/selectaccount_warmup_test.go`

**Interfaces:**
- Consumes: `warmup_profiles.stage`（Task 1）、`cfg.WarmupGate`（Task 15）。
- Produces: `Dispatcher.warmupGate` 字段；selectAccount 的 gate 分支。

- [ ] **Step 1: 写失败测试**

`internal/dispatch/selectaccount_warmup_test.go`（复用 dispatch testsupport 的 `seedAccount`；再 seed warmup_profiles）：
```go
package dispatch

import (
	"context"
	"testing"
	"time"
)

func seedWarmup(t *testing.T, ctx context.Context, pool interface {
	Exec(context.Context, string, ...any) (any, error)
}, jid, stage string) {
	t.Helper()
	// 用 pgxpool 直接插;签名按实际 pool 类型调整。
}

func TestSelectAccountWarmupGate(t *testing.T) {
	pool, ctx := pgPool(t)
	reg := time.Now().Add(-30 * 24 * time.Hour) // 老号,配额充足
	seedAccount(t, ctx, pool, "mat@s.whatsapp.net", "US", reg, 100, 0)
	seedAccount(t, ctx, pool, "warm@s.whatsapp.net", "US", reg, 100, 0)
	// mat 是 MATURE,warm 是 WARMING。
	_, _ = pool.Exec(ctx, `INSERT INTO warmup_profiles (account_jid,tenant_id,lane,stage) VALUES ('mat@s.whatsapp.net',1,'STANDARD','MATURE')`)
	_, _ = pool.Exec(ctx, `INSERT INTO warmup_profiles (account_jid,tenant_id,lane,stage) VALUES ('warm@s.whatsapp.net',1,'STANDARD','WARMING')`)

	// gate ON:只应选中 mat。多次抽样都不该是 warm。
	d := newTestDispatcher(t, pool, true) // helper 构造带 warmupGate=true 的 Dispatcher
	for i := 0; i < 8; i++ {
		jid := mustSelect(t, ctx, d, 1, "US")
		if jid == "warm@s.whatsapp.net" {
			t.Fatalf("WARMING account must not be selected under gate")
		}
	}

	// gate OFF:warm 也可能被选中(不强制,只验不 panic 且能选到号)。
	dOff := newTestDispatcher(t, pool, false)
	if jid := mustSelect(t, ctx, dOff, 1, "US"); jid == "" {
		t.Fatalf("gate off should still select some account")
	}
}
```
> `newTestDispatcher(t, pool, gate)` 与 `mustSelect` 是本测试的 helper：构造一个仅够跑 `selectAccount` 的 `Dispatcher`（其余依赖可 nil/桩），并开事务调用。若 dispatch 包已有构造 helper 则扩一个 gate 参数。

- [ ] **Step 2: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestSelectAccountWarmupGate -v`
Expected: 编译失败/FAIL（无 gate 字段、warm 被选中）。

- [ ] **Step 3: 写实现**

`internal/dispatch/selectaccount.go` 改为按 gate 拼 SQL：
```go
func (d *Dispatcher) selectAccount(ctx context.Context, tx pgx.Tx, tenantID int64, country string) (string, error) {
	gateJoin := ""
	if d.warmupGate {
		// 只选养号已毕业(MATURE)的号。缺 warmup_profiles 行的号视为未毕业,排除。
		gateJoin = ` JOIN warmup_profiles wp ON wp.account_jid = a.account_jid AND wp.stage = 'MATURE'`
	}
	var jid string
	err := tx.QueryRow(ctx, `
SELECT a.account_jid
  FROM account_devices a
  JOIN proxy_pool p ON p.id = a.proxy_id`+gateJoin+`
 WHERE a.tenant_id = $1
   AND a.ban_status = 'active'
   AND (a.quarantined_until IS NULL OR a.quarantined_until < now())
   AND p.country_code = $2
   AND (effective_quota(a.registered_at, a.health_score) - a.sent_today) > 0
 ORDER BY (effective_quota(a.registered_at, a.health_score) - a.sent_today) DESC, random()
 LIMIT 1`, tenantID, country).Scan(&jid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoCapacity
	}
	if err != nil {
		return "", fmt.Errorf("select account: %w", err)
	}
	return jid, nil
}
```
在 `Dispatcher` struct 加 `warmupGate bool`，构造器（`NewDispatcher`）加参数；`cmd/wadist/main.go` 传 `cfg.WarmupGate`。

- [ ] **Step 4: 跑测试验证通过 + 现有 dispatch 测试不回归**

Run: `cd /var/klwa && go test ./internal/dispatch/ -v`
Expected: 新测试 PASS，既有 selectAccount 测试（gate 默认 false）不回归。

- [ ] **Step 5: 提交**

```bash
git add internal/dispatch/selectaccount.go internal/dispatch/dispatcher.go cmd/wadist/main.go internal/dispatch/selectaccount_warmup_test.go
git commit -m "feat(p5c): business send MATURE gate behind WADIST_WARMUP_GATE (default off)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 17: 封号信号自动降级

**Files:**
- Modify: `internal/api/webhook_evolution.go`（`connection.update` down/logout 分支调 `warmup.Demote`）
- Modify: `internal/sendgate/*`（若 health 掉到阈值的路径更合适，在 `ApplyHealthSignal` 后挂钩——二选一，优先 webhook logout 明确信号）
- Test: `internal/api/webhook_demote_test.go`

**Interfaces:**
- Consumes: `warmup.Service.Demote`（Task 5）。
- Produces: webhook logout/down → 自动 Demote。

- [ ] **Step 1: 写失败测试**

`internal/api/webhook_demote_test.go`：enroll 并 promote 一个号到 MATURE，投一条 `connection.update` 且 `state` 为登出/下线态，验 stage 回到 WARMING。
```go
func TestLogoutDemotesWarmup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, ctx := newWarmupTestServer(t)
	seedWarmupAccount(t, ctx, s, "gone@s.whatsapp.net") // 绑定 instance "igone"
	// 提到 MATURE
	_ = s.deps.Warmup.ForcePromote(ctx, "gone@s.whatsapp.net")

	wh := NewEvolutionWebhook("", s.deps.Receipt, s.deps.Mgr, nil, s.deps.QRCache).WithWarmup(s.deps.Warmup)
	r := gin.New()
	wh.Register(r)
	body := `{"event":"connection.update","instance":"igone","data":{"state":"close"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	p, _ := s.deps.Warmup.Store().Get(ctx, "gone@s.whatsapp.net")
	if p.Stage != StageWarming {
		t.Fatalf("logout should demote MATURE->WARMING, got %s", p.Stage)
	}
}
```
> 注意 `Demote` 对非 MATURE 号会因非法迁移报错——实现里对 down 信号先判 stage 再降级，或吞掉 `ErrInvalidTransition`（WARMING/NEW 号收到 down 无需降级）。

- [ ] **Step 2: 跑测试验证失败**

Run: `cd /var/klwa && go test ./internal/api/ -run TestLogoutDemotesWarmup -v`
Expected: FAIL（stage 仍 MATURE）。

- [ ] **Step 3: 写实现**

在 `connection.update` case 的 `isDownState(w.Data.State)` 分支里追加（在 health 信号旁）：
```go
		if isDownState(w.Data.State) && h.warmup != nil {
			if jid := h.resolveJID(ctx, w); jid != "" {
				// 只降级已毕业号;非 MATURE 的非法迁移吞掉(无需降级)。
				if err := h.warmup.Demote(ctx, jid, "conn_down"); err != nil && !errors.Is(err, warmup.ErrInvalidTransition) {
					log.Printf("warmup demote %s: %v", jid, err)
				}
			}
		}
```
> `Demote` 对 WARMING/NEW 返回 `ErrInvalidTransition`（Task 2 迁移表：WARMING/NEW 无 DEMOTE），这里显式忽略该错。`errors`/`log` import 若缺补上。`h.resolveJID` 是 webhook 已有助手。

- [ ] **Step 4: 跑测试验证通过**

Run: `cd /var/klwa && go test ./internal/api/ -run TestLogoutDemotesWarmup -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/api/webhook_evolution.go internal/api/webhook_demote_test.go
git commit -m "feat(p5c): auto-demote MATURE->WARMING on connection down/logout

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 18: 前端脚本库管理 + 收尾集成 + 文档

**Files:**
- Create: `internal/api/warmup_scripts_api.go`（脚本 CRUD：`GET/POST/DELETE /admin/warmup/scripts`）
- Create: `frontend/components/admin-warmup-scripts.tsx`（脚本增删启停 UI）
- Modify: `internal/api/router.go`（脚本路由）
- Modify: `.env.example`（补 `WADIST_WARMUP_INTERVAL`/`WADIST_WARMUP_GATE`）
- Create: `docs/warmup-runbook.md`（灰度上线步骤）
- Test: `internal/api/warmup_scripts_api_test.go` + 前端 build

**Interfaces:**
- Consumes: `warmup.Store`（scripts CRUD 新增方法 `ListAllScripts`/`CreateScript`/`SetScriptEnabled`/`DeleteScript`）。
- Produces: 脚本管理端到端。

- [ ] **Step 1: Store 加脚本 CRUD（TDD）**

`internal/warmup/scripts.go` 加 `CreateScript(ctx, lang string, turns []Turn) (int64, error)`、`ListAllScripts(ctx) ([]Script, error)`（含 disabled）、`SetScriptEnabled(ctx, id int64, enabled bool) error`、`DeleteScript(ctx, id int64) error`；`internal/warmup/scripts_test.go` 加对应测试（创建→列出→禁用→删除断言）。先写测试跑失败，再实现，再跑通过。

- [ ] **Step 2: API handlers + 路由（TDD）**

`warmup_scripts_api.go`：`handleAdminWarmupListScripts`/`Create`/`SetEnabled`/`Delete`，router 注册 `admin.GET/POST/PUT/DELETE("/warmup/scripts...")`。`warmup_scripts_api_test.go` 验创建+列出。

- [ ] **Step 3: 前端脚本管理 UI**

`admin-warmup-scripts.tsx`：列出脚本（lang / turns 预览 / enabled 开关 / 删除），新增脚本表单（lang 选择 + 多轮 turn 编辑器：from A/B + text，至少 2 轮）。接入养号中心的"脚本库"Tab。i18n 补 key，`check-i18n-keys.mjs` + build 过。

- [ ] **Step 4: 文档 + env**

`docs/warmup-runbook.md` 写灰度步骤：①部署（含 0024 迁移，`make deploy` postgres 护栏）②扫码接入 ≥2 个同国号 ③观察 `/admin/warmup` 计数增长与自动毕业 ④确认养号互发不计费（wallet_ledger 无养号条目）⑤`WADIST_WARMUP_GATE=on` 灰度开闸 ⑥验证业务发送只走 MATURE 号 ⑦回退：`WADIST_WARMUP_GATE=off`。`.env.example` 在 Evolution 段后补：
```
# ── 养号引擎(P5)──
WADIST_WARMUP_INTERVAL=5m      # 养号 tick 周期,<=0 关闭养号引擎
WADIST_WARMUP_GATE=off         # on=业务发送只选 MATURE 号(灰度默认 off,回退零风险)
```

- [ ] **Step 5: 全量校验 + 提交**

Run: `cd /var/klwa && go test ./... && cd frontend && node scripts/check-i18n-keys.mjs && npm run build`
Expected: 全绿。
```bash
git add internal/api/warmup_scripts_api.go internal/warmup/scripts.go internal/warmup/scripts_test.go internal/api/warmup_scripts_api_test.go frontend/components/admin-warmup-scripts.tsx frontend/lib/i18n/dicts/admin.ts internal/api/router.go .env.example docs/warmup-runbook.md
git commit -m "feat(p5c): warmup script management UI/API + runbook + env

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

**✅ P5c 里程碑：** MATURE 闸门（开关兜底）+ 封号自动降级 + 脚本库热管理 + runbook 齐备。养号系统全链路闭环。**上线前必须**：全分支终审 + 灰度扫码账号池真机验证（按 `docs/warmup-runbook.md`），`WADIST_WARMUP_GATE` 确认默认 off，账号池与毕业验证通过后再开闸。

---

## Self-Review（计划自查）

**Spec 覆盖核对（spec §→task）：**
- §3.1 warmup_profiles → Task 1 ✅ / §3.2 policies → Task 1+7+10 ✅ / §3.3 scripts → Task 1+11+18 ✅
- §4 引擎(state/policy/service/scripts) → Task 2/3/5/11 ✅
- §4.3 脚本库+抖动+打字态 → Task 11/12/13 ✅
- §5 回复信号(messages.upsert) → Task 14 ✅
- §6 调度器 tick + 自动 enroll → Task 15 + Task 6 ✅
- §7.1 MATURE 闸门 → Task 16 ✅ / §7.2 特性开关 → Task 15(config)+16 ✅ / §7.3 封号降级 → Task 17 ✅
- §8 后台养号中心 → Task 9/10/18 ✅
- §9 错误处理(单对失败跳过/空脚本告警/gate 空) → Task 13/16 ✅
- §10 测试 → 各 Task 内 TDD ✅
- §11 分段 P5a/b/c → 三阶段 ✅
- §13 不计费/tenant 轴/Layer1 不动 → Global Constraints + Task 1 ✅

**类型一致性核对：** `Stage`/`Lane`/`Event`/`Policy`/`Profile`/`Service`/`Store`/`Sender`/`Account`/`AccountLookup`/`Turn`/`Script` 在定义 Task（2/3/4/5/11/13）与消费 Task（6/7/13/15/16/17）中签名一致。`Service.Store()` 访问器在 Task 7 引入，Task 14/16/17 复用。`WithWarmup`/`WithSender`/`WithAccounts` 链式 setter 保持 `NewService`/`NewEvolutionWebhook` 原签名。

**占位符扫描：** 无 TBD/TODO；每个 code step 附完整代码。少数"按现有 helper 适配"处（`recordAudit`/`doJSON`/`atoiDefault`/构造签名）已显式标注复用既有实现或本地最小实现，非占位。

**已知需实现期确认的接缝（非占位，标注给实现者）：** Evolution SendTyping 确切路由（以真机 runbook 为准，Task 12）；`phone_number` 加密列 → 用 `split_part(jid)`（Task 15）；jid 路由传参改 query（Task 7 Step 7 已定 query 方案）；console/worker 是否同进程（Task 8 按实际构造点注入）。
