# M7 PgBouncer 隔离 + admission 指数退避 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 让存储层兼容 PgBouncer transaction 模式（simple/exec 协议 + 删 lockPool + 强制 redis 所有权），并给 admission 加段级指数退避（收 wa_warning 时加宽该段 min_gap）。

**Architecture:** A 组（store）：`WADIST_PG_POOL_MODE=pgbouncer` 时对所有 pgxpool 设 `DefaultQueryExecMode`（exec/simple）、跳过 lockPool、fail-fast 强制 `OwnershipBackend=redis`。B 组（sendgate）：`Admission.RecordWarning` 写 `backoff:{seg}` 键（×2 cap 8 TTL 300s），`admitLuaBackoff` 读段键取 max 乘数放大 min_gap。两组正交、各自门控、默认=今日行为。

**Tech Stack:** Go 1.26, pgx/v5 + pgxpool, go-redis (Lua), PgBouncer (compose only)。

## Global Constraints

- **零爆炸半径**：`WADIST_PG_POOL_MODE=direct`（默认）→ pgx 默认 CacheStatement + 创建 lockPool + pg 所有权可用 = 逐字今日。`WADIST_BACKOFF_ON=off`（默认）→ Admission 用原 `admitLua`（2 KEYS）逐字今日、RecordWarning 不写键。
- **正确性不变式**：防双开（pgbouncer 模式 redis fence，`chaos_redis_test.go` 平价）、`balance=Σledger`、`message_id` 幂等、`sent_today` 对称——simple/exec 不改 SQL 语义，退避只改 pacing 时序。
- **包分层**：`store` 层改 config/manager；`sendgate` 层改 admission；`cmd/wadist` 接线。
- **默认值**：PoolMode `direct`、QueryMode `exec`、BackoffOn off、Factor 2、Max 8、TTL 300000ms。
- **B 组 wiring 范围收敛**：段级退避机制做全（RecordWarning 变参段键 + admitLuaBackoff 读 cc/net 两段键），但本里程碑 **wiring 只接 cc 段**（`pl.Country`，两路径现成可用）；**代理 /24 段 wiring 延后**（需热路径代理解析，当前未 plumb）。占位 net 键从不被写入 → mult 恒 1 → cc-only 生效、net 无副作用。机制支持后续无改动补 /24。

---

### Task 1: store/config PoolMode+QueryMode 字段 + 校验

**Files:**
- Modify: `internal/store/config.go`（字段 + `withDefaults` 默认 + 新 `validate()`）
- Modify: `internal/config/config.go`（进程层字段 + Load + Store 投影）
- Test: `internal/store/config_test.go`（新建或追加）

**Interfaces:**
- Produces: `store.Config.PoolMode string`、`store.Config.QueryMode string`；`func (c *Config) validate() error`（pgbouncer→非 redis 报错、MaxConns>200 钳位）。`config.Config.PgPoolMode`、`config.Config.PgQueryMode`。

- [ ] **Step 1: Write the failing test**

`internal/store/config_test.go`：

```go
package store

import (
	"strings"
	"testing"
)

func TestConfigValidate_PgbouncerRequiresRedis(t *testing.T) {
	c := Config{PoolMode: "pgbouncer", OwnershipBackend: "pg"}
	c.withDefaults()
	err := c.validate()
	if err == nil || !strings.Contains(err.Error(), "redis") {
		t.Fatalf("want error mentioning redis; got %v", err)
	}
}

func TestConfigValidate_PgbouncerWithRedisOK(t *testing.T) {
	c := Config{PoolMode: "pgbouncer", OwnershipBackend: "redis"}
	c.withDefaults()
	if err := c.validate(); err != nil {
		t.Fatalf("pgbouncer+redis should validate; got %v", err)
	}
}

func TestConfigValidate_CapsMaxConns(t *testing.T) {
	c := Config{PoolMode: "pgbouncer", OwnershipBackend: "redis", MaxOpenConns: 500}
	c.withDefaults()
	if err := c.validate(); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.MaxOpenConns != 200 {
		t.Fatalf("MaxOpenConns=%d; want capped to 200", c.MaxOpenConns)
	}
}

func TestConfigDefaults_DirectUnchanged(t *testing.T) {
	c := Config{}
	c.withDefaults()
	if c.PoolMode != "direct" || c.QueryMode != "exec" {
		t.Fatalf("defaults PoolMode=%q QueryMode=%q; want direct/exec", c.PoolMode, c.QueryMode)
	}
	if err := c.validate(); err != nil {
		t.Fatalf("direct mode must validate; got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestConfigValidate -v` and `-run TestConfigDefaults`
Expected: FAIL（`PoolMode`/`QueryMode`/`validate` 未定义）。

- [ ] **Step 3: Implement**

`internal/store/config.go` 结构体加字段（`OwnershipBackend` 附近）：

```go
	// PoolMode selects the PG connection strategy: "direct" (default) or
	// "pgbouncer" (transaction-pooled: simple/exec protocol, no lockPool,
	// requires OwnershipBackend=="redis").
	PoolMode string
	// QueryMode is the pgx exec mode under pgbouncer: "exec" (default) | "simple".
	QueryMode string
```

`withDefaults()` 末尾加：

```go
	if c.PoolMode == "" {
		c.PoolMode = "direct"
	}
	if c.QueryMode == "" {
		c.QueryMode = "exec"
	}
```

新增 `validate`（同文件）：

```go
// validate enforces cross-field constraints after defaults are applied.
// pgbouncer transaction pooling is incompatible with session-level advisory
// locks, so it requires the redis ownership backend; MaxOpenConns is capped at
// the PgBouncer client-conn ceiling.
func (c *Config) validate() error {
	if c.PoolMode == "pgbouncer" {
		if c.OwnershipBackend != "redis" {
			return fmt.Errorf("store: pgbouncer mode requires WADIST_OWNERSHIP_BACKEND=redis, got %q", c.OwnershipBackend)
		}
		if c.MaxOpenConns > 200 {
			c.MaxOpenConns = 200
		}
	}
	return nil
}
```

（确保 `config.go` 已 import `fmt`；若无则加。）

`internal/config/config.go`：结构体加 `PgPoolMode string`、`PgQueryMode string`；`Load()` 加 `cfg.PgPoolMode = getenv("WADIST_PG_POOL_MODE","direct")`、`cfg.PgQueryMode = getenv("WADIST_PG_QUERY_MODE","exec")`；`Store()` 投影加 `PoolMode: c.PgPoolMode, QueryMode: c.PgQueryMode,`。

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/store/ -run 'TestConfigValidate|TestConfigDefaults' -v`
Expected: PASS（4 例）。

- [ ] **Step 5: Build**

Run: `go build ./...`
Expected: 成功。

- [ ] **Step 6: Commit**

```bash
git add internal/store/config.go internal/config/config.go internal/store/config_test.go
git commit -m "feat(store): PoolMode/QueryMode config + pgbouncer validation (M7 t1)"
```

---

### Task 2: Manager 池构造按模式设 QueryExecMode + 跳过 lockPool

**Files:**
- Modify: `internal/store/manager.go`（`applyQueryMode` helper；三池 ParseConfig 后各调；lockPool 门控；`AcquireDeviceLock` nil 守卫；`NewManager` 调 `validate()`）
- Modify: `internal/store/lock.go`（`AcquireDeviceLock` 开头 nil 守卫）
- Test: `internal/store/config_test.go`（`applyQueryMode` 单测——纯函数无容器）

**Interfaces:**
- Consumes: `Config.PoolMode`/`QueryMode`（Task 1）。
- Produces: `func applyQueryMode(poolCfg *pgxpool.Config, poolMode, queryMode string)`。

**说明**：`NewManager` 在 `cfg.withDefaults()`（manager.go:66）后加 `if err := cfg.validate(); err != nil { return nil, err }`。三处 `pgxpool.ParseConfig`（bizPool:117、tenantPool:161、systemPool:182）之后各调 `applyQueryMode(poolCfg, cfg.PoolMode, cfg.QueryMode)`。lockPool 块（136-156）整体包进 `if cfg.PoolMode != "pgbouncer" { ... }`，pgbouncer 时 `lockPool` 保持 nil。所有 `lockPool.Close()` 的错误清理路径需判 nil（`if lockPool != nil { lockPool.Close() }`）。

- [ ] **Step 1: Write the failing test**

追加到 `internal/store/config_test.go`：

```go
import "github.com/jackc/pgx/v5"

func TestApplyQueryMode(t *testing.T) {
	base := func() *pgxpool.Config {
		c, err := pgxpool.ParseConfig("postgres://u:p@localhost:5432/db")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// direct: leave pgx default untouched (QueryExecModeCacheStatement == 0 value)
	d := base()
	applyQueryMode(d, "direct", "simple")
	if d.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeCacheStatement {
		t.Fatalf("direct changed exec mode to %v; want default CacheStatement", d.ConnConfig.DefaultQueryExecMode)
	}
	// pgbouncer + exec
	e := base()
	applyQueryMode(e, "pgbouncer", "exec")
	if e.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Fatalf("pgbouncer+exec got %v; want Exec", e.ConnConfig.DefaultQueryExecMode)
	}
	// pgbouncer + simple
	s := base()
	applyQueryMode(s, "pgbouncer", "simple")
	if s.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeSimpleProtocol {
		t.Fatalf("pgbouncer+simple got %v; want SimpleProtocol", s.ConnConfig.DefaultQueryExecMode)
	}
}
```

> 实现者核对：`pgx.QueryExecModeCacheStatement` 是 pgx 默认（零值）。若 pgxpool.ParseConfig 需要 import `github.com/jackc/pgx/v5/pgxpool`，加上。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestApplyQueryMode -v`
Expected: FAIL（`applyQueryMode` 未定义）。

- [ ] **Step 3: Implement**

`internal/store/manager.go` 加 helper（import `github.com/jackc/pgx/v5`）：

```go
// applyQueryMode sets the pgx exec mode for PgBouncer transaction-pool
// compatibility. direct mode leaves the pgx default (CacheStatement) untouched.
func applyQueryMode(poolCfg *pgxpool.Config, poolMode, queryMode string) {
	if poolMode != "pgbouncer" {
		return
	}
	if queryMode == "simple" {
		poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	} else {
		poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	}
}
```

`NewManager` 在 `cfg.withDefaults()` 后：

```go
	cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
```

bizPool（117 附近）：`poolCfg.HealthCheckPeriod = ...` 之后加 `applyQueryMode(poolCfg, cfg.PoolMode, cfg.QueryMode)`。tenantPool（`tCfg.MaxConns=...` 后）、systemPool（`sCfg.MaxConns=...` 后）同样各加一行。

lockPool 块门控：把 136–156 的 lockPool 创建整段包进：
```go
	var lockPool *pgxpool.Pool
	if cfg.PoolMode != "pgbouncer" {
		lockPoolCfg, err := pgxpool.ParseConfig(cfg.DSN)
		... (原逻辑)
		lockPool, err = pgxpool.NewWithConfig(ctx, lockPoolCfg)
		...
	}
```
所有后续 `lockPool.Close()` 清理点改 `if lockPool != nil { lockPool.Close() }`。

`internal/store/lock.go` 的 `AcquireDeviceLock`（约 lock.go:30）开头加：
```go
	if m.lockPool == nil {
		return nil, fmt.Errorf("store: advisory lock unavailable in pgbouncer mode (use redis ownership)")
	}
```

- [ ] **Step 4: Run tests + build**

Run: `go test ./internal/store/ -run 'TestApplyQueryMode|TestConfig'`
Expected: PASS。

Run: `go build ./...`
Expected: 成功。

- [ ] **Step 5: Run the store package (short) to catch regressions**

Run: `go test -short ./internal/store/`
Expected: PASS（direct 模式默认，既有测试不受影响）。

- [ ] **Step 6: Commit**

```bash
git add internal/store/manager.go internal/store/lock.go internal/store/config_test.go
git commit -m "feat(store): apply QueryExecMode + skip lockPool in pgbouncer mode (M7 t2)"
```

---

### Task 3: 高风险查询 simple/exec 协议集成测

**Files:**
- Test: `internal/store/pgbouncer_protocol_test.go`（新建）

**说明**：本 task 无生产代码，只加集成测证明现有查询在 pgbouncer 模式（simple 与 exec）下正确。simple 协议对普通 PG 也生效，无需真 PgBouncer。构 Manager 需 redis（pgbouncer 强制 redis 所有权）——沿用现有集成测起 redis testcontainer 的方式（参考 `chaos_redis_test.go` 如何提供 `cfg.Redis`）。跑一组高风险语句：enum cast（`ban_status::text`）、`$n::interval`、array/jsonb 若有。代表性查询直接复用现有 Manager/SendGate 方法（如 `ListActiveAccounts`、`ClaimAccount`、billing Hold、`SendGate` 读账号），断言无 `unnamed prepared statement`/隐式转换错误。

- [ ] **Step 1: Write the integration test**

`internal/store/pgbouncer_protocol_test.go`：

```go
package store

import (
	"context"
	"testing"
)

// runProtocolSmoke builds a pgbouncer-mode Manager with the given query mode and
// exercises a representative set of high-risk queries (enum casts, ::interval,
// parameterized selects) to prove they succeed under the simple/exec protocol —
// which is what PgBouncer transaction mode requires. Simple protocol works
// against plain Postgres too, so no real PgBouncer is needed here.
func runProtocolSmoke(t *testing.T, queryMode string) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration")
	}
	// Reuse the existing redis+pg test harness. pgbouncer mode mandates redis
	// ownership; mirror chaos_redis_test.go's Manager construction, overriding:
	//   cfg.PoolMode = "pgbouncer"; cfg.QueryMode = queryMode; cfg.OwnershipBackend = "redis"
	m := newPgbouncerTestManager(t, queryMode) // helper: see Step 3
	ctx := context.Background()

	// enum-cast + parameterized select (SendGate-style load).
	seedAccountDevice(t, ctx, m, "jid-proto")
	if _, err := m.ListActiveAccounts(ctx); err != nil {
		t.Fatalf("[%s] ListActiveAccounts: %v", queryMode, err)
	}
	// UPDATE ... ::interval style is exercised via SendGate.ApplyHealthSignal in
	// its own package; here assert the account CRUD + claim path (owner_node update).
	if err := m.ClaimAccount(ctx, "jid-proto", "node-proto"); err != nil {
		t.Fatalf("[%s] ClaimAccount: %v", queryMode, err)
	}
	if err := m.MarkAccountLoggedOut(ctx, "jid-proto"); err != nil {
		t.Fatalf("[%s] MarkAccountLoggedOut: %v", queryMode, err)
	}
}

func TestPgbouncerProtocol_Simple(t *testing.T) { runProtocolSmoke(t, "simple") }
func TestPgbouncerProtocol_Exec(t *testing.T)   { runProtocolSmoke(t, "exec") }
```

- [ ] **Step 2: Add the test harness helper**

在同文件加 `newPgbouncerTestManager(t, queryMode string) *Manager`：复制现有 `newTestManager`/`chaos_redis_test.go` 的 Manager 构造（PG DSN + redis testcontainer 地址），但设 `cfg.PoolMode="pgbouncer"`、`cfg.QueryMode=queryMode`、`cfg.OwnershipBackend="redis"`、`cfg.Redis=<test redis client>`。实现者读现有测试拿到确切的 DSN/redis 提供方式与 migration 应用（如 `readMigration0007`）。若无现成 redis 测试客户端 helper，用现有 chaos_redis_test.go 的模式起一个。

- [ ] **Step 3: Run the integration tests**

Run: `go test ./internal/store/ -run 'TestPgbouncerProtocol' -v`
Expected: PASS（两模式；若 simple 协议在某查询上暴露隐式转换错误，这正是本 task 要抓的——记录到报告并在该查询加显式 cast 修复，然后重跑）。

- [ ] **Step 4: Commit**

```bash
git add internal/store/pgbouncer_protocol_test.go
git commit -m "test(store): pgbouncer simple/exec protocol smoke over high-risk queries (M7 t3)"
```

---

### Task 4: Admission.RecordWarning + 退避小 Lua

**Files:**
- Modify: `internal/sendgate/admission.go`（`BackoffParams`、`NewAdmission` 扩参、`RecordWarning`、退避 Lua）
- Modify: `cmd/wadist/main.go`（`NewAdmission` 调用先传默认 off params——编译对齐，真正接线 Task 6）
- Test: `internal/sendgate/admission_backoff_test.go`（新建）

**Interfaces:**
- Produces:
  - `type BackoffParams struct { On bool; Factor, Max int; TTL time.Duration }`
  - `func NewAdmission(rdb *goredis.Client, bp BackoffParams) *Admission`
  - `func (a *Admission) RecordWarning(ctx context.Context, segKeys ...string) error`

**说明**：退避键值=乘数整数。小 Lua 原子：`cur = tonumber(GET key) or 1; nv = math.min(MAX, cur*FACTOR); SET key nv EX ttl; return nv`。`On==false` → RecordWarning 直接 nil。段键由调用方构造（`backoff:cc:{cc}`）。

- [ ] **Step 1: Write failing tests**

`internal/sendgate/admission_backoff_test.go`（沿用该包既有 redis testcontainer helper——参考 `admission_test.go` 如何起 rdb）：

```go
package sendgate

import (
	"context"
	"testing"
	"time"
)

func TestRecordWarning_DoublesToCap(t *testing.T) {
	rdb := newTestRedis(t) // 复用 admission_test.go 的 redis helper
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: true, Factor: 2, Max: 8, TTL: 300 * time.Second})
	key := "backoff:cc:BR"
	want := []int{2, 4, 8, 8}
	for i, w := range want {
		if err := a.RecordWarning(ctx, key); err != nil {
			t.Fatalf("warning %d: %v", i, err)
		}
		got, err := rdb.Get(ctx, key).Int()
		if err != nil {
			t.Fatalf("get after %d: %v", i, err)
		}
		if got != w {
			t.Fatalf("after warning %d mult=%d; want %d", i, got, w)
		}
	}
}

func TestRecordWarning_OffWritesNothing(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: false, Factor: 2, Max: 8, TTL: 300 * time.Second})
	if err := a.RecordWarning(ctx, "backoff:cc:BR"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if n, _ := rdb.Exists(ctx, "backoff:cc:BR").Result(); n != 0 {
		t.Fatalf("key exists=%d; want 0 (off writes nothing)", n)
	}
}

func TestRecordWarning_SetsTTL(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: true, Factor: 2, Max: 8, TTL: 300 * time.Second})
	_ = a.RecordWarning(ctx, "backoff:cc:BR")
	ttl, _ := rdb.TTL(ctx, "backoff:cc:BR").Result()
	if ttl <= 0 || ttl > 300*time.Second {
		t.Fatalf("ttl=%v; want (0,300s]", ttl)
	}
}

func TestRecordWarning_MultiSegIndependent(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: true, Factor: 2, Max: 8, TTL: 300 * time.Second})
	_ = a.RecordWarning(ctx, "backoff:cc:BR", "backoff:net:1.2.3.0/24")
	br, _ := rdb.Get(ctx, "backoff:cc:BR").Int()
	nt, _ := rdb.Get(ctx, "backoff:net:1.2.3.0/24").Int()
	if br != 2 || nt != 2 {
		t.Fatalf("br=%d net=%d; want both 2", br, nt)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/sendgate/ -run TestRecordWarning -v`
Expected: FAIL（`BackoffParams`/`RecordWarning`/新 `NewAdmission` 签名未定义）。

- [ ] **Step 3: Implement**

`internal/sendgate/admission.go`：

```go
const backoffLua = `
local cur = tonumber(redis.call('GET', KEYS[1]) or '1')
local nv = cur * tonumber(ARGV[1])
if nv > tonumber(ARGV[2]) then nv = tonumber(ARGV[2]) end
redis.call('SET', KEYS[1], nv, 'PX', tonumber(ARGV[3]))
return nv`

type BackoffParams struct {
	On     bool
	Factor int
	Max    int
	TTL    time.Duration
}

type Admission struct {
	rdb     *goredis.Client
	admit   *goredis.Script
	backoff *goredis.Script
	bp      BackoffParams
}

func NewAdmission(rdb *goredis.Client, bp BackoffParams) *Admission {
	return &Admission{
		rdb:     rdb,
		admit:   goredis.NewScript(admitLua),
		backoff: goredis.NewScript(backoffLua),
		bp:      bp,
	}
}

// RecordWarning multiplicatively increases each segment's backoff multiplier
// (capped at bp.Max) with a fresh TTL, so admission widens that segment's
// min_gap. No-op when backoff is disabled.
func (a *Admission) RecordWarning(ctx context.Context, segKeys ...string) error {
	if !a.bp.On {
		return nil
	}
	for _, k := range segKeys {
		if err := a.backoff.Run(ctx, a.rdb, []string{k},
			a.bp.Factor, a.bp.Max, a.bp.TTL.Milliseconds()).Err(); err != nil {
			return fmt.Errorf("record warning %s: %w", k, err)
		}
	}
	return nil
}
```

`cmd/wadist/main.go:139` 改为 `adm := sendgate.NewAdmission(rdb, sendgate.BackoffParams{})`（默认 off；真正接线 Task 6）。

- [ ] **Step 4: Run tests pass + build**

Run: `go test ./internal/sendgate/ -run TestRecordWarning -v`
Expected: PASS（4 例）。
Run: `go build ./...`
Expected: 成功。

- [ ] **Step 5: Commit**

```bash
git add internal/sendgate/admission.go cmd/wadist/main.go internal/sendgate/admission_backoff_test.go
git commit -m "feat(sendgate): Admission.RecordWarning segment backoff keys (M7 t4)"
```

---

### Task 5: admitLuaBackoff 读段键放大 min_gap + Admit 扩段参

**Files:**
- Modify: `internal/sendgate/admission.go`（`admitLuaBackoff` 脚本、`NewAdmission` 按 On 选 admit 脚本、`Admit` 扩 `segKeys`）
- Modify: `internal/sendgate/sendgate.go`（`SendGate.Admit` 传段键——见说明）
- Modify: `internal/dispatch/worker.go`（`w.gate.Admit` 传 `pl.Country`）
- Test: `internal/sendgate/admission_backoff_test.go`（追加 admit 放大测试）

**说明**：`admitLuaBackoff` = 在 `admitLua` 基础上，KEYS 增 [3]=backoff:cc、[4]=backoff:net，pacing 判定前 `local mult = math.max(1, tonumber(redis.call('GET',KEYS[3]) or 1), tonumber(redis.call('GET',KEYS[4]) or 1)); local gap = tonumber(ARGV[2]) * mult`，用 `gap` 替 `ARGV[2]`。`NewAdmission`：`bp.On` → admit=admitLuaBackoff，否则原 admitLua。`Admit` 扩为 `Admit(ctx, jid, quota, minGap, now, ccKey, netKey string)`：off 时 ccKey/netKey 传空、脚本只引 KEYS[1,2]（原脚本忽略多余 KEYS——但 go-redis Script 传 4 KEYS 给 2-KEY 脚本是安全的，Lua 不引用即忽略）。**为稳妥**：off 时仍传 4 个 KEYS（day, pace, "backoff:cc:", "backoff:net:"），原 admitLua 不引用 [3][4] → 行为不变。on 时传真实段键。

**wiring 范围**（见 Global Constraints）：cc 段=`pl.Country`；net 段本里程碑传占位常量（如空 `backoff:net:` 前缀键，从不被 RecordWarning 写 → mult 1）。`SendGate.Admit` 增一个 `cc string` 入参，内部构 `ccKey="backoff:cc:"+cc`、`netKey="backoff:net:"`（占位）。`worker.go:34` 改传 `pl.Country`。

- [ ] **Step 1: Write failing test**

追加到 `admission_backoff_test.go`：

```go
func TestAdmit_BackoffWidensMinGap(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: true, Factor: 2, Max: 8, TTL: 300 * time.Second})
	base := time.Unix(1_000_000, 0).UTC()
	minGap := 100 * time.Millisecond
	ccKey, netKey := "backoff:cc:BR", "backoff:net:x"

	// first send admits.
	if tk, _, err := a.Admit(ctx, "j1", 100, minGap, base, ccKey, netKey); err != nil || tk == nil {
		t.Fatalf("first admit: tk=%v err=%v", tk, err)
	}
	// set cc backoff mult=4 → effective gap 400ms.
	_ = a.RecordWarning(ctx, ccKey) // 2
	_ = a.RecordWarning(ctx, ccKey) // 4
	// a send 300ms later: without backoff (gap 100ms) it WOULD pass; with mult 4
	// (gap 400ms) it must be paced-rejected.
	_, reason, err := a.Admit(ctx, "j1", 100, minGap, base.Add(300*time.Millisecond), ccKey, netKey)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if reason != "pacing" {
		t.Fatalf("reason=%q; want pacing (backoff widened gap to 400ms)", reason)
	}
}

func TestAdmit_OffModeUnchanged(t *testing.T) {
	rdb := newTestRedis(t)
	ctx := context.Background()
	a := NewAdmission(rdb, BackoffParams{On: false})
	base := time.Unix(2_000_000, 0).UTC()
	// even with a backoff key present, off-mode admit ignores it.
	rdb.Set(ctx, "backoff:cc:BR", 8, 0)
	if tk, _, err := a.Admit(ctx, "j2", 100, 100*time.Millisecond, base, "backoff:cc:BR", "backoff:net:x"); err != nil || tk == nil {
		t.Fatalf("first: %v", err)
	}
	// 150ms later > 100ms base gap → passes (backoff ignored in off mode).
	if tk, _, err := a.Admit(ctx, "j2", 100, 100*time.Millisecond, base.Add(150*time.Millisecond), "backoff:cc:BR", "backoff:net:x"); err != nil || tk == nil {
		t.Fatalf("second should pass in off mode; tk=%v err=%v", tk, err)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/sendgate/ -run 'TestAdmit_Backoff|TestAdmit_OffMode' -v`
Expected: FAIL（`Admit` 新签名未定义）。

- [ ] **Step 3: Implement**

`admission.go` 加 `admitLuaBackoff`（复制 `admitLua`，在 `local last=...` 前插 mult/gap 计算，pacing 比较用 `gap`）：

```go
const admitLuaBackoff = `
local sent = tonumber(redis.call('GET', KEYS[1]) or '0')
if sent >= tonumber(ARGV[1]) then return {0, 'daily_quota'} end
local mult = math.max(1, tonumber(redis.call('GET', KEYS[3]) or '1'), tonumber(redis.call('GET', KEYS[4]) or '1'))
local gap = tonumber(ARGV[2]) * mult
local last = tonumber(redis.call('GET', KEYS[2]) or '0')
if tonumber(ARGV[3]) - last < gap then return {0, 'pacing'} end
redis.call('INCR', KEYS[1])
if redis.call('TTL', KEYS[1]) < 0 then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[4])) end
redis.call('SET', KEYS[2], ARGV[3])
return {1, 'ok'}`
```

`NewAdmission`：`admit` 脚本按 `bp.On` 选：`script := admitLua; if bp.On { script = admitLuaBackoff }; admit: goredis.NewScript(script)`。

`Admit` 改签名并总传 4 KEYS：

```go
func (a *Admission) Admit(ctx context.Context, jid string, quota int, minGap time.Duration, now time.Time, ccKey, netKey string) (*Ticket, string, error) {
	...
	res, err := a.admit.Run(ctx, a.rdb,
		[]string{dayKey, paceKey, ccKey, netKey},
		quota, minGap.Milliseconds(), u.UnixMilli(), ttl).Result()
	...
}
```
（off 模式 admit=admitLua 只引 KEYS[1,2]，多传的 [3][4] 被忽略——行为不变。）

`sendgate.go` `SendGate.Admit` 增 `cc string` 入参，内部：
```go
ccKey := "backoff:cc:" + cc
netKey := "backoff:net:" // 占位（/24 wiring 延后）
ticket, reason, err := g.adm.Admit(ctx, jid, quota, jitteredGap(g.baseGap), now, ccKey, netKey)
```

`worker.go:34`：`dec, err := w.gate.Admit(ctx, pl.JID, pl.Country, time.Now())`（核对 SendGate.Admit 参数顺序，把 cc 放在合适位置并同步签名）。

- [ ] **Step 4: Run tests pass + full sendgate package + build**

Run: `go test ./internal/sendgate/ -v`
Expected: PASS（含既有 admission 测试——off 默认逐字旧行为）。
Run: `go build ./...`
Expected: 成功（worker.go 调用同步）。

- [ ] **Step 5: Commit**

```bash
git add internal/sendgate/admission.go internal/sendgate/sendgate.go internal/dispatch/worker.go internal/sendgate/admission_backoff_test.go
git commit -m "feat(sendgate): admitLuaBackoff widens min_gap by segment multiplier (M7 t5)"
```

---

### Task 6: config 接线 + wa_warning 调 RecordWarning + PgBouncer compose

**Files:**
- Modify: `cmd/wadist/main.go`（`NewAdmission` 传真 BackoffParams；wa_warning 路径调 `RecordWarning`；PoolMode/QueryMode 已由 Store() 投影，确认生效）
- Modify: `internal/config/config.go`（Backoff* 字段 + Load）
- Modify: `internal/dispatch/worker.go`（wa_warning 分支调 `gate` 的退避——见说明）
- Create: `docker-compose.adopt.yml` 的 PgBouncer 服务（若文件已存在则追加服务）
- Test: 无新增（机制测试在 t4/t5）

**说明**：`config.Config` 加 `BackoffOn bool`、`BackoffFactor int`、`BackoffMax int`、`BackoffTTL time.Duration`；`Load()`：`getenv("WADIST_BACKOFF_ON","off")!="off"`、`intEnv("WADIST_BACKOFF_FACTOR",2)`、`intEnv("WADIST_BACKOFF_MAX",8)`、`msEnv("WADIST_BACKOFF_TTL_MS",300000)`。`main.go` `NewAdmission(rdb, sendgate.BackoffParams{On: cfg.BackoffOn, Factor: cfg.BackoffFactor, Max: cfg.BackoffMax, TTL: cfg.BackoffTTL})`。

wa_warning→RecordWarning 接线：`worker.go` wa_warning 分支（worker.go:77）已有 `pl.Country`。SendGate 需暴露一个转发到 `adm.RecordWarning` 的方法（保持 worker 只依赖 gate）：`SendGate.RecordSegmentWarning(ctx, cc string) error` → `g.adm.RecordWarning(ctx, "backoff:cc:"+cc)`。worker 在 ApplyHealthSignal("wa_warning") 后调 `w.gate.RecordSegmentWarning(ctx, pl.Country)`（off 时 no-op）。

PgBouncer compose 服务（`docker-compose.adopt.yml`）：
```yaml
  pgbouncer:
    image: edoburu/pgbouncer:latest
    environment:
      DB_HOST: postgres
      DB_NAME: wadist
      POOL_MODE: transaction
      MAX_CLIENT_CONN: "1000"
      DEFAULT_POOL_SIZE: "50"
      AUTH_TYPE: scram-sha-256
    depends_on: [postgres]
    ports: ["6432:6432"]
    # NOTE: 本机未集成测；上线随 two-file（-f prod.yml -f adopt.yml）翻开。
    # app 侧须置 WADIST_PG_POOL_MODE=pgbouncer + WADIST_OWNERSHIP_BACKEND=redis
    # 且 WADIST_POSTGRES_DSN 指向 pgbouncer:6432。
```
> 实现者核对 `docker-compose.adopt.yml` 现有服务命名/网络与 `postgres` 服务名，使 DB_HOST/depends_on 对齐；auth 细节按现有 compose 的 PG 凭据方式对齐（如挂 userlist.txt），做不到完整 auth 就留 TODO 注释并保证 compose 语法有效（`docker compose config` 可解析）。

- [ ] **Step 1: config 字段 + Load**

按说明加 `config.Config` 四字段 + Load 行。`go build ./...` 应过。

- [ ] **Step 2: SendGate.RecordSegmentWarning + worker 接线**

`sendgate.go` 加：
```go
// RecordSegmentWarning bumps the recipient country's admission backoff on a
// wa_warning. No-op when backoff is disabled (Admission handles the gate).
func (g *SendGate) RecordSegmentWarning(ctx context.Context, cc string) error {
	return g.adm.RecordWarning(ctx, "backoff:cc:"+cc)
}
```
`worker.go` wa_warning 分支（77 附近）在 `ApplyHealthSignal(...,"wa_warning",...)` 后加 `_ = w.gate.RecordSegmentWarning(ctx, pl.Country)`。

- [ ] **Step 3: main.go NewAdmission 真 params**

改 `adm := sendgate.NewAdmission(rdb, sendgate.BackoffParams{On: cfg.BackoffOn, Factor: cfg.BackoffFactor, Max: cfg.BackoffMax, TTL: cfg.BackoffTTL})`；加启动 log `if cfg.BackoffOn { log.Printf("admission backoff on (factor=%d max=%d ttl=%s)", ...) }`；`if cfg.PgPoolMode=="pgbouncer" { log.Printf("pg pool mode=pgbouncer query=%s", cfg.PgQueryMode) }`。

- [ ] **Step 4: PgBouncer compose 服务**

按说明加服务；`docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml config >/dev/null` 验证可解析（若本机无 compose 则 `go` 侧不阻塞，仅确保 YAML 合法）。

- [ ] **Step 5: Build + targeted tests + gate subset**

Run: `go build ./...` → 成功。
Run: `go test ./internal/config/ ./internal/sendgate/ ./internal/dispatch/ ./internal/store/ -short`
Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/sendgate/sendgate.go internal/dispatch/worker.go cmd/wadist/main.go docker-compose.adopt.yml
git commit -m "feat(wadist): wire pgbouncer pool mode + admission backoff + adopt.yml PgBouncer service (M7 t6)"
```

---

## Self-Review 记录

- **Spec 覆盖**：A 组 config+校验(t1)/池构造+lockPool(t2)/协议集成测(t3)；B 组 RecordWarning(t4)/admitLuaBackoff(t5)；接线+compose(t6) 对齐 spec §3–§5、§7 六 task 表。
- **类型一致**：`BackoffParams{On,Factor,Max,TTL}` 在 t4 定义、t4/t5/t6 用；`NewAdmission(rdb, bp)` 新签名 t4 起、t6 传真值；`Admit(...,ccKey,netKey)` t5 定义、t5 worker 调用同步；`applyQueryMode(cfg,poolMode,queryMode)` t2。
- **零爆炸半径**：PoolMode direct→applyQueryMode 早返回、lockPool 照建；BackoffOn off→NewAdmission 用原 admitLua、RecordWarning no-op、Admit 多传 KEYS 被忽略。默认全 direct/off。
- **范围收敛已标注**：/24 段 wiring 延后（占位 net 键恒 mult 1），机制支持后补。
- **无占位符**：每 code step 均含完整代码/命令/期望；集成测 harness 与 compose auth 两处显式标注「照现有代码/compose 对齐」。
