# M2: Redis 所有权硬化 + 代理 ZSET 冷却圈 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 硬化已实现的 Redis 租约所有权后端（解耦硬编码 TTL），并新增一个 Redis ZSET「冷却圈」代理分配器，替换 PG `FOR UPDATE SKIP LOCKED` 的选代理逻辑，去掉高密度发送时的 PG 选代理竞争，同时保留代理粘性、容量上限、死代理排除，PG 仍为持久真相源。

**Architecture:** 两部分都**配置门控、默认走旧路径（零爆炸半径）**，与 M1 一致。所有权后端仍由 `WADIST_OWNERSHIP_BACKEND`（默认 `pg`）选择——M2 只解耦其硬编码 `hbTTL`，不改编译默认（redis 化是运维经 compose env 完成的 cutover，符合既定 shadow→canary→cutover 纪律）。代理分配新增 `WADIST_PROXY_BACKEND`（默认 `pg`）：`redis` 时用 `proxy:avail:{cc}` ZSET（score=下次可用时间戳）做 O(logN) 冷却分配，`proxy:free` hash 记容量，`proxy:meta` hash 缓存 url/type/cc；PG `proxy_pool`/`account_devices` 仍同步单行写（按主键，无扫描竞争）作持久真相源，节点启动时从 PG 重建 Redis 热索引。

**Tech Stack:** Go 1.26、`github.com/redis/go-redis/v9`（Lua `redis.NewScript`）、PostgreSQL 16(pgx/v5)、testcontainers-go（redis + postgres 集成测试）。

## Global Constraints

- 默认行为不变（零爆炸半径）：`WADIST_OWNERSHIP_BACKEND` 默认 `pg`、`WADIST_PROXY_BACKEND` 默认 `pg`；未设时 M2 代码路径不生效，线上 pg 行为逐字不变。
- **PG 是持久真相源**：`proxy_pool`（存在性/url/type/cc/is_alive/max_bindings/current_bindings/usage_count）+ `account_devices.proxy_id/proxy_url_cache`（粘性绑定记录）。Redis 结构是**派生的热索引**，节点启动时从 PG 重建。
- **代理粘性不变**：账号首次 warm 才 `BindProxy`，之后复用 `proxy_url_cache`，仅代理死亡才重挑（由现有 `control.StickyBindProxy` 保证，M2 不改它）。
- **冷却语义**：一个代理被分配（bind）或释放（release）后，score 置 `now + cooldown`（默认 60s），在冷却期内不被 `ZRANGEBYSCORE -inf now` 选中——满足「同一 IP 不在 60s 内复用」。
- **容量**：`proxy:free` hash 字段=proxyID、值=剩余空闲槽（= max_bindings − current_bindings）；剩余槽 >0 时代理才在 ZSET 中。
- **死代理排除**：`ReportProxyFailure` 达阈值 → 从 ZSET `ZREM` + 删 free/meta 字段；`ReportProxySuccess` 复活 → 重新入 ZSET。
- Redis 需 AOF（已由部署开启）。所有权 TTL 与心跳：心跳间隔必须 < TTL（否则租约过期误判）。
- whatsmeow/pgx/redis 版本沿用 go.mod 现值。
- TDD；集成测试用 testcontainers（store 包已有 redis + pg 测试助手，复用；`internal/node` 已有 `startRedis`/`TestTakeoverChaos_Redis`）。每个 Task 末尾该包 `go test -race` 通过再提交；改 `internal/store` 后跑 `make gate`。

---

## 文件结构

- Modify `internal/store/config.go` — 新增 `OwnershipTTL time.Duration`、`ProxyBackend string`、`ProxyCooldown time.Duration`；`withDefaults` 默认 30s / "pg" / 60s。
- Modify `internal/store/ownership_redis.go:12,89` — 去掉 `const hbTTL`，`redisOwnership` 加 `hbTTL time.Duration` 字段，构造函数注入，`Heartbeat` 用字段。
- Modify `internal/store/ownership.go:33-40` — `newRedisOwnershipWithRoster` 传入 `cfg.OwnershipTTL`。
- Modify `cmd/wadist/main.go` — `sc.OwnershipTTL = cfg.NodeStaleness`；启动守卫 `HeartbeatInterval < NodeStaleness`；装配 proxy backend（B6 已在 Manager 内部，无需 main 改动，除非加日志）。
- Create `internal/store/proxy_redis.go` — Redis 冷却圈分配器：key schema、Lua bind、release、report、boot rebuild、`redisProxyAllocator` 类型。
- Create `internal/store/proxy_redis_test.go` — 分配器集成测试（testcontainers redis）。
- Modify `internal/store/proxy.go` — `BindProxy`/`ReleaseProxy`/`ReportProxyFailure`/`ReportProxySuccess` 按 `cfg.ProxyBackend` 分派到 pg（现有）或 redis 分配器；保持签名不变。
- Modify `internal/store/manager.go` — `newManager` 在 `ProxyBackend=="redis"` 时构造 `redisProxyAllocator` 并 boot-rebuild。
- Create `internal/store/proxy_zset_integration_test.go` — 端到端（pg+redis）冷却/容量/死代理/重建/粘性。

---

### Task 1: (A1) 解耦 Redis 所有权 TTL（去硬编码 hbTTL）

**Files:**
- Modify: `internal/store/config.go` (add `OwnershipTTL`)
- Modify: `internal/store/ownership_redis.go:12,67-90`
- Modify: `internal/store/ownership.go:33-40`
- Modify: `cmd/wadist/main.go` (set `sc.OwnershipTTL` + startup guard)
- Test: `internal/store/ownership_redis_test.go` (extend)

**Interfaces:**
- Consumes: nothing new.
- Produces: `Config.OwnershipTTL time.Duration` (default 30s); `newRedisOwnershipWithRoster(rdb, roster, ttl)` — NEW 3rd param.

- [ ] **Step 1: Write the failing test** — TTL is configurable, not the const 30s.

```go
// internal/store/ownership_redis_test.go (add)
func TestRedisOwnership_ConfigurableTTL(t *testing.T) {
	rdb := newTestRedis(t) // existing store redis helper (redis_helper_test.go)
	o := newRedisOwnershipWithRoster(rdb, nil, 5*time.Second)
	if err := o.Heartbeat(context.Background(), "nodeX"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	ttl, err := rdb.TTL(context.Background(), hbKey("nodeX")).Result()
	if err != nil {
		t.Fatalf("ttl: %v", err)
	}
	if ttl <= 0 || ttl > 5*time.Second {
		t.Fatalf("hb TTL = %v; want ~5s (configured, not hardcoded 30s)", ttl)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/ -run TestRedisOwnership_ConfigurableTTL -v`
Expected: FAIL — `newRedisOwnershipWithRoster` takes 2 args, not 3 (compile error).

- [ ] **Step 3: Implement**

In `internal/store/config.go` add to `Config`:
```go
// OwnershipTTL is the Redis heartbeat key TTL for the redis/shadow ownership
// backends (a node is considered dead when its hb key expires). Should equal
// the cluster NodeStaleness. Heartbeat interval MUST be < OwnershipTTL.
OwnershipTTL time.Duration
```
In `withDefaults()`:
```go
if c.OwnershipTTL <= 0 {
	c.OwnershipTTL = 30 * time.Second
}
```

In `internal/store/ownership_redis.go`: delete `const hbTTL = 30 * time.Second` (line 12); add field + use it:
```go
type redisOwnership struct {
	rdb    *goredis.Client
	roster func(ctx context.Context) ([]string, error)
	hbTTL  time.Duration
}

func newRedisOwnershipWithRoster(rdb *goredis.Client, roster func(context.Context) ([]string, error), hbTTL time.Duration) *redisOwnership {
	if hbTTL <= 0 {
		hbTTL = 30 * time.Second
	}
	return &redisOwnership{rdb: rdb, roster: roster, hbTTL: hbTTL}
}

// (delete the old newRedisOwnership 2-arg constructor if unused, or keep it
//  delegating with the 30s default; grep first: `grep -rn newRedisOwnership internal`)

func (o *redisOwnership) Heartbeat(ctx context.Context, nodeID string) error {
	return o.rdb.Set(ctx, hbKey(nodeID), "1", o.hbTTL).Err()
}
```

In `internal/store/ownership.go` `newOwnership`, pass `m.cfg.OwnershipTTL`:
```go
case "redis":
	return newRedisOwnershipWithRoster(m.cfg.Redis, m.ListActiveAccounts, m.cfg.OwnershipTTL)
case "shadow":
	return newShadowOwnership(&pgOwnership{m: m}, newRedisOwnershipWithRoster(m.cfg.Redis, m.ListActiveAccounts, m.cfg.OwnershipTTL), m.cfg.OnShadowDivergence)
```

In `cmd/wadist/main.go` (near where `sc := cfg.Store()` is built, ~line 91): add `sc.OwnershipTTL = cfg.NodeStaleness` and a startup guard:
```go
sc.OwnershipTTL = cfg.NodeStaleness
if cfg.HeartbeatInterval >= cfg.NodeStaleness {
	log.Printf("warn: HeartbeatInterval(%s) >= NodeStaleness(%s); ownership leases may expire before refresh", cfg.HeartbeatInterval, cfg.NodeStaleness)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /var/klwa && go test ./internal/store/ -run TestRedisOwnership -race -v && go test ./internal/node/ -run TestTakeoverChaos_Redis -race`
Expected: PASS (config-driven TTL; existing redis chaos parity still green).

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/config.go internal/store/ownership_redis.go internal/store/ownership.go cmd/wadist/main.go internal/store/ownership_redis_test.go
git commit -m "feat(store): decouple redis ownership hbTTL from hardcode (= NodeStaleness)"
```

---

### Task 2: (B1) 代理后端配置（WADIST_PROXY_BACKEND / cooldown）

**Files:**
- Modify: `internal/store/config.go`
- Modify: `internal/config/config.go` (read env)
- Test: `internal/store/config_test.go`

**Interfaces:**
- Produces: `Config.ProxyBackend string` (default "pg"), `Config.ProxyCooldown time.Duration` (default 60s).

- [ ] **Step 1: Write the failing test**

```go
// internal/store/config_test.go (add)
func TestConfig_ProxyDefaults(t *testing.T) {
	var c Config
	c.withDefaults()
	if c.ProxyBackend != "pg" {
		t.Fatalf("ProxyBackend default = %q; want pg", c.ProxyBackend)
	}
	if c.ProxyCooldown != 60*time.Second {
		t.Fatalf("ProxyCooldown default = %v; want 60s", c.ProxyCooldown)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/ -run TestConfig_ProxyDefaults -v`
Expected: FAIL — fields undefined.

- [ ] **Step 3: Implement**

`internal/store/config.go` add to `Config`:
```go
// ProxyBackend selects proxy allocation: "pg" (FOR UPDATE SKIP LOCKED, default)
// or "redis" (proxy:avail:{cc} ZSET cooldown ring). PG stays the durable source.
ProxyBackend string
// ProxyCooldown is how long a proxy is ineligible for re-selection after a
// bind/release (anti-ban: don't reuse an IP within this window).
ProxyCooldown time.Duration
```
`withDefaults()`:
```go
if c.ProxyBackend == "" {
	c.ProxyBackend = "pg"
}
if c.ProxyCooldown <= 0 {
	c.ProxyCooldown = 60 * time.Second
}
```
`internal/config/config.go` in the `Store()` construction add:
```go
ProxyBackend:  os.Getenv("WADIST_PROXY_BACKEND"),
ProxyCooldown: envDurationMs("WADIST_PROXY_COOLDOWN_MS"), // 0 → store default; reuse existing ms-env helper or inline parse
```
(If no ms-env helper exists, parse inline: read `WADIST_PROXY_COOLDOWN_MS`, `strconv.Atoi`, `time.Duration(n)*time.Millisecond`; leave 0 on empty/error so `withDefaults` applies 60s.)

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/ -run TestConfig_ProxyDefaults -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/config.go internal/store/config_test.go internal/config/config.go
git commit -m "feat(store): WADIST_PROXY_BACKEND + cooldown config (default pg)"
```

---

### Task 3: (B2) proxy_redis.go — key schema + 原子 bind Lua

**Files:**
- Create: `internal/store/proxy_redis.go`
- Test: `internal/store/proxy_redis_test.go`

**Interfaces:**
- Produces:
  - keys: `func availKey(cc string) string` → `"proxy:avail:"+cc`; consts `proxyFreeKey = "proxy:free"`, `proxyMetaKey = "proxy:meta"`.
  - `type redisProxyAllocator struct { rdb *goredis.Client; cooldown time.Duration }`
  - `func newRedisProxyAllocator(rdb *goredis.Client, cooldown time.Duration) *redisProxyAllocator`
  - `func (a *redisProxyAllocator) pick(ctx context.Context, cc string, nowMs int64) (proxyID int64, meta string, err error)` — returns `(0,"",ErrNoProxyAvailable)` on miss. `meta` is `url|type|cc`. Atomically: pick lowest-score eligible member, cool it (or ZREM if last free slot), decrement free.

- [ ] **Step 1: Write the failing test** (seed one proxy, pick cools it so a second immediate pick misses)

```go
// internal/store/proxy_redis_test.go
package store

import (
	"context"
	"testing"
)

func TestRedisProxy_PickCoolsAndCapacity(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t) // store redis helper
	a := newRedisProxyAllocator(rdb, 60_000) // 60s cooldown (ms)

	// seed: proxy 7, cc=US, 1 free slot, eligible now, meta.
	seedProxyRedis(t, rdb, 7, "US", 1, "socks5://p7", 0)

	id, meta, err := a.pick(ctx, "US", 1000)
	if err != nil || id != 7 || meta != "socks5://p7|socks5|US" {
		t.Fatalf("pick = %d,%q,%v; want 7,meta,nil", id, meta, err)
	}
	// immediate second pick: proxy 7 is now cooling (score=1000+60000) AND had only
	// 1 slot → removed from ZSET; must miss.
	if _, _, err := a.pick(ctx, "US", 1001); err != ErrNoProxyAvailable {
		t.Fatalf("second pick err = %v; want ErrNoProxyAvailable", err)
	}
}

// seedProxyRedis writes the hot-index entries a boot-rebuild would create.
func seedProxyRedis(t *testing.T, rdb redisCmdable, id int64, cc string, free int, url string, nextMs int64) {
	t.Helper()
	ctx := context.Background()
	member := itoa(id)
	if err := rdb.ZAdd(ctx, availKey(cc), goredisZ(float64(nextMs), member)).Err(); err != nil {
		t.Fatal(err)
	}
	rdb.HSet(ctx, proxyFreeKey, member, free)
	rdb.HSet(ctx, proxyMetaKey, member, url+"|socks5|"+cc)
}
```
> Note: `newTestRedis`, `redisCmdable`, `goredisZ`, `itoa` — use the store package's existing redis test helper + small local shims; if `goredisZ` isn't handy, inline `goredis.Z{Score: float64(nextMs), Member: member}` and import goredis in the test.

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/ -run TestRedisProxy_PickCoolsAndCapacity -v`
Expected: FAIL — `newRedisProxyAllocator`/`availKey` undefined.

- [ ] **Step 3: Implement**

```go
// internal/store/proxy_redis.go
package store

import (
	"context"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

func availKey(cc string) string { return "proxy:avail:" + cc }

const (
	proxyFreeKey = "proxy:free" // hash: field=proxyID, val=free slot count
	proxyMetaKey = "proxy:meta" // hash: field=proxyID, val="url|type|cc"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// pickLua atomically selects the lowest-score eligible proxy in avail:{cc}
// (score<=now), decrements its free-slot counter, and either re-adds it with a
// fresh cooldown score (if slots remain) or removes it (if it was the last slot).
// KEYS=[avail:{cc}, proxy:free, proxy:meta] ARGV=[nowMs, cooldownMs]
// returns {proxyID, meta} or {} on miss.
var pickLua = goredis.NewScript(`
local ids = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, 1)
if #ids == 0 then return {} end
local id = ids[1]
local free = tonumber(redis.call('HGET', KEYS[2], id) or '0') - 1
redis.call('HSET', KEYS[2], id, free)
if free <= 0 then
  redis.call('ZREM', KEYS[1], id)
else
  redis.call('ZADD', KEYS[1], tonumber(ARGV[1]) + tonumber(ARGV[2]), id)
end
local meta = redis.call('HGET', KEYS[3], id)
return {id, meta}`)

type redisProxyAllocator struct {
	rdb      *goredis.Client
	cooldown time.Duration
}

func newRedisProxyAllocator(rdb *goredis.Client, cooldown time.Duration) *redisProxyAllocator {
	return &redisProxyAllocator{rdb: rdb, cooldown: cooldown}
}

func (a *redisProxyAllocator) pick(ctx context.Context, cc string, nowMs int64) (int64, string, error) {
	res, err := pickLua.Run(ctx, a.rdb,
		[]string{availKey(cc), proxyFreeKey, proxyMetaKey},
		nowMs, a.cooldown.Milliseconds()).Slice()
	if err != nil {
		return 0, "", err
	}
	if len(res) < 2 {
		return 0, "", ErrNoProxyAvailable
	}
	idStr, _ := res[0].(string)
	meta, _ := res[1].(string)
	id, perr := strconv.ParseInt(idStr, 10, 64)
	if perr != nil {
		return 0, "", perr
	}
	return id, meta, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/ -run TestRedisProxy_PickCoolsAndCapacity -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/proxy_redis.go internal/store/proxy_redis_test.go
git commit -m "feat(store): redis proxy cooldown-ring pick (atomic Lua: eligible+cool+capacity)"
```

---

### Task 4: (B3) release + report failure/success 的 Redis 更新

**Files:**
- Modify: `internal/store/proxy_redis.go`
- Test: `internal/store/proxy_redis_test.go`

**Interfaces:**
- Produces on `*redisProxyAllocator`:
  - `func (a *redisProxyAllocator) release(ctx, proxyID int64, cc string, nowMs int64) error` — HINCRBY free +1, ZADD avail:{cc} score=now+cooldown (re-eligible after cooldown).
  - `func (a *redisProxyAllocator) markDead(ctx, proxyID int64, cc string) error` — ZREM avail:{cc} + HDEL free/meta.
  - `func (a *redisProxyAllocator) markAlive(ctx, proxyID int64, cc, meta string, freeSlots int, nowMs int64) error` — re-add: HSET free, HSET meta, ZADD score=now (eligible now).

- [ ] **Step 1: Write the failing test**

```go
func TestRedisProxy_ReleaseAndDead(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)
	a := newRedisProxyAllocator(rdb, 60_000)
	seedProxyRedis(t, rdb, 7, "US", 1, "socks5://p7", 0)

	id, _, _ := a.pick(ctx, "US", 1000) // cools + removes (1 slot)
	if id != 7 { t.Fatalf("pick=%d", id) }

	// release makes it re-eligible after cooldown (score=1000+60000).
	if err := a.release(ctx, 7, "US", 1000); err != nil { t.Fatalf("release: %v", err) }
	if _, _, err := a.pick(ctx, "US", 1001); err != ErrNoProxyAvailable {
		t.Fatalf("still cooling, want miss; got %v", err)
	}
	if _, _, err := a.pick(ctx, "US", 61_001); err != nil {
		t.Fatalf("after cooldown pick: %v", err)
	}

	// markDead excludes it entirely.
	seedProxyRedis(t, rdb, 9, "US", 1, "socks5://p9", 0)
	if err := a.markDead(ctx, 9, "US"); err != nil { t.Fatalf("markDead: %v", err) }
	if _, _, err := a.pick(ctx, "US", 200_000); err != ErrNoProxyAvailable {
		t.Fatalf("dead proxy must be excluded; got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/ -run TestRedisProxy_ReleaseAndDead -v`
Expected: FAIL — methods undefined.

- [ ] **Step 3: Implement**

```go
// append to internal/store/proxy_redis.go
func (a *redisProxyAllocator) release(ctx context.Context, proxyID int64, cc string, nowMs int64) error {
	member := itoa(proxyID)
	pipe := a.rdb.TxPipeline()
	pipe.HIncrBy(ctx, proxyFreeKey, member, 1)
	pipe.ZAdd(ctx, availKey(cc), goredis.Z{Score: float64(nowMs) + float64(a.cooldown.Milliseconds()), Member: member})
	_, err := pipe.Exec(ctx)
	return err
}

func (a *redisProxyAllocator) markDead(ctx context.Context, proxyID int64, cc string) error {
	member := itoa(proxyID)
	pipe := a.rdb.TxPipeline()
	pipe.ZRem(ctx, availKey(cc), member)
	pipe.HDel(ctx, proxyFreeKey, member)
	pipe.HDel(ctx, proxyMetaKey, member)
	_, err := pipe.Exec(ctx)
	return err
}

func (a *redisProxyAllocator) markAlive(ctx context.Context, proxyID int64, cc, meta string, freeSlots int, nowMs int64) error {
	member := itoa(proxyID)
	pipe := a.rdb.TxPipeline()
	pipe.HSet(ctx, proxyFreeKey, member, freeSlots)
	pipe.HSet(ctx, proxyMetaKey, member, meta)
	pipe.ZAdd(ctx, availKey(cc), goredis.Z{Score: float64(nowMs), Member: member})
	_, err := pipe.Exec(ctx)
	return err
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/ -run TestRedisProxy_ReleaseAndDead -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/proxy_redis.go internal/store/proxy_redis_test.go
git commit -m "feat(store): redis proxy release/markDead/markAlive ZSET ops"
```

---

### Task 5: (B4) 从 PG 启动重建 Redis 热索引

**Files:**
- Modify: `internal/store/proxy_redis.go`
- Test: `internal/store/proxy_redis_rebuild_test.go`

**Interfaces:**
- Produces: `func (a *redisProxyAllocator) rebuildFromPG(ctx context.Context, pool *pgxpool.Pool, nowMs int64) (int, error)` — clears any stale `proxy:avail:*`/`proxy:free`/`proxy:meta`, then for each alive proxy with `max_bindings - current_bindings > 0` seeds free/meta/ZADD(score=now). Returns count seeded.

- [ ] **Step 1: Write the failing test** (testcontainers PG + redis; insert proxies, rebuild, pick works)

```go
func TestRedisProxy_RebuildFromPG(t *testing.T) {
	ctx := context.Background()
	m := newManagerWithSchema(t) // existing store test helper: real PG + migrations
	rdb := newTestRedis(t)
	a := newRedisProxyAllocator(rdb, 60_000)

	// two US proxies: one alive w/ free slot, one dead.
	mustExec(t, m.bizPool, `INSERT INTO proxy_pool (proxy_url,proxy_type,country_code,is_alive,max_bindings,current_bindings) VALUES
		('socks5://alive','socks5','US',true,2,0), ('socks5://dead','socks5','US',false,1,0)`)

	n, err := a.rebuildFromPG(ctx, m.bizPool, 5000)
	if err != nil || n != 1 {
		t.Fatalf("rebuild = %d,%v; want 1 (only alive)", n, err)
	}
	id, meta, err := a.pick(ctx, "US", 5001)
	if err != nil || meta == "" || id == 0 {
		t.Fatalf("pick after rebuild: %d,%q,%v", id, meta, err)
	}
}
```
> `newManagerWithSchema`, `mustExec` — existing store test helpers (see proxy_test.go / testsupport_test.go). If names differ, use the actual helper that spins PG + runs migrations.

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/ -run TestRedisProxy_RebuildFromPG -v`
Expected: FAIL — `rebuildFromPG` undefined.

- [ ] **Step 3: Implement**

```go
// append to internal/store/proxy_redis.go
import "github.com/jackc/pgx/v5/pgxpool" // add to imports

func (a *redisProxyAllocator) rebuildFromPG(ctx context.Context, pool *pgxpool.Pool, nowMs int64) (int, error) {
	// clear stale hot index (idempotent boot).
	iter := a.rdb.Scan(ctx, 0, "proxy:avail:*", 0).Iterator()
	var availKeys []string
	for iter.Next(ctx) {
		availKeys = append(availKeys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return 0, err
	}
	pipe := a.rdb.TxPipeline()
	if len(availKeys) > 0 {
		pipe.Del(ctx, availKeys...)
	}
	pipe.Del(ctx, proxyFreeKey, proxyMetaKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}

	rows, err := pool.Query(ctx, `
SELECT id, proxy_url, proxy_type::text, country_code, (max_bindings - current_bindings) AS free
  FROM proxy_pool
 WHERE is_alive = TRUE AND max_bindings > current_bindings`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	seed := a.rdb.TxPipeline()
	n := 0
	for rows.Next() {
		var id int64
		var url, ptype, cc string
		var free int
		if err := rows.Scan(&id, &url, &ptype, &cc, &free); err != nil {
			return 0, err
		}
		member := itoa(id)
		seed.HSet(ctx, proxyFreeKey, member, free)
		seed.HSet(ctx, proxyMetaKey, member, url+"|"+ptype+"|"+cc)
		seed.ZAdd(ctx, availKey(cc), goredis.Z{Score: float64(nowMs), Member: member})
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if _, err := seed.Exec(ctx); err != nil {
		return 0, err
	}
	return n, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/ -run TestRedisProxy_RebuildFromPG -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/proxy_redis.go internal/store/proxy_redis_rebuild_test.go
git commit -m "feat(store): rebuild redis proxy hot-index from PG on boot"
```

---

### Task 6: (B5) redisProxyAllocator 的 BindProxy/Release（Redis 选 + PG 持久写）

**Files:**
- Modify: `internal/store/proxy_redis.go`
- Test: `internal/store/proxy_redis_bind_test.go`

**Interfaces:**
- Consumes: B2 `pick`, B3 `release`.
- Produces on `*redisProxyAllocator`:
  - `func (a *redisProxyAllocator) bind(ctx context.Context, pool *pgxpool.Pool, accountJID, cc string, nowMs int64) (*ProxyBinding, error)` — Redis `pick` → then single-tx PG durable write: `UPDATE proxy_pool SET current_bindings+1, usage_count+1 WHERE id=$pid`, `UPDATE account_devices SET proxy_id=$pid, proxy_url_cache=$url WHERE account_jid=$jid`. Returns `*ProxyBinding{ProxyID,ProxyURL,ProxyType,Country}` parsed from meta. On PG failure, Redis-`release` the pick to avoid leaking capacity, return error.
  - `func (a *redisProxyAllocator) releaseBinding(ctx, pool, accountJID string, nowMs int64) error` — read account's proxy_id+cc from PG, PG single-tx decrement + clear binding, Redis `release`. Idempotent when unbound.
- Assumes the account is currently unbound (StickyBindProxy guards); if already bound, releaseBinding-then-bind.

- [ ] **Step 1: Write the failing test**

```go
func TestRedisProxy_BindWritesPGAndBinding(t *testing.T) {
	ctx := context.Background()
	m := newManagerWithSchema(t)
	rdb := newTestRedis(t)
	a := newRedisProxyAllocator(rdb, 60_000)
	mustExec(t, m.bizPool, `INSERT INTO account_devices (account_jid, tenant_id) VALUES ('acc1', 1)`)
	mustExec(t, m.bizPool, `INSERT INTO proxy_pool (proxy_url,proxy_type,country_code,is_alive,max_bindings,current_bindings) VALUES ('socks5://p','socks5','US',true,1,0)`)
	if _, err := a.rebuildFromPG(ctx, m.bizPool, 1000); err != nil { t.Fatal(err) }

	b, err := a.bind(ctx, m.bizPool, "acc1", "US", 1000)
	if err != nil || b == nil || b.Country != "US" || b.ProxyURL != "socks5://p" {
		t.Fatalf("bind = %+v,%v", b, err)
	}
	// PG durable: account bound + counter incremented.
	var pid *int64
	var cur int
	m.bizPool.QueryRow(ctx, `SELECT a.proxy_id, p.current_bindings FROM account_devices a JOIN proxy_pool p ON p.id=a.proxy_id WHERE a.account_jid='acc1'`).Scan(&pid, &cur)
	if pid == nil || cur != 1 {
		t.Fatalf("PG not durably updated: pid=%v cur=%d", pid, cur)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/ -run TestRedisProxy_BindWritesPGAndBinding -v`
Expected: FAIL — `bind` undefined.

- [ ] **Step 3: Implement**

```go
// append to internal/store/proxy_redis.go
import (
	"fmt"
	"strings"
	"github.com/jackc/pgx/v5"
)

func parseMeta(meta string) (url, ptype, cc string, ok bool) {
	parts := strings.SplitN(meta, "|", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func (a *redisProxyAllocator) bind(ctx context.Context, pool *pgxpool.Pool, accountJID, cc string, nowMs int64) (*ProxyBinding, error) {
	id, meta, err := a.pick(ctx, cc, nowMs)
	if err != nil {
		return nil, err // ErrNoProxyAvailable on miss
	}
	url, ptype, mcc, ok := parseMeta(meta)
	if !ok {
		_ = a.release(ctx, id, cc, nowMs) // return the slot; corrupt meta
		return nil, fmt.Errorf("proxy meta corrupt for id %d: %q", id, meta)
	}
	err = pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE proxy_pool SET current_bindings=current_bindings+1, usage_count=usage_count+1 WHERE id=$1`, id); e != nil {
			return e
		}
		ct, e := tx.Exec(ctx, `UPDATE account_devices SET proxy_id=$1, proxy_url_cache=$2 WHERE account_jid=$3`, id, url, accountJID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() == 0 {
			return ErrAccountMissing
		}
		return nil
	})
	if err != nil {
		_ = a.release(ctx, id, cc, nowMs) // PG failed → don't leak capacity in Redis
		return nil, err
	}
	return &ProxyBinding{ProxyID: id, ProxyURL: url, ProxyType: ptype, Country: mcc}, nil
}

func (a *redisProxyAllocator) releaseBinding(ctx context.Context, pool *pgxpool.Pool, accountJID string, nowMs int64) error {
	var oldID *int64
	var cc string
	err := pool.QueryRow(ctx, `
SELECT a.proxy_id, COALESCE(p.country_code,'')
  FROM account_devices a LEFT JOIN proxy_pool p ON p.id=a.proxy_id
 WHERE a.account_jid=$1`, accountJID).Scan(&oldID, &cc)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrAccountMissing
		}
		return err
	}
	if oldID == nil {
		return nil // already unbound (idempotent)
	}
	err = pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE proxy_pool SET current_bindings=GREATEST(current_bindings-1,0) WHERE id=$1`, *oldID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE account_devices SET proxy_id=NULL, proxy_url_cache=NULL WHERE account_jid=$1`, accountJID)
		return e
	})
	if err != nil {
		return err
	}
	return a.release(ctx, *oldID, cc, nowMs)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/ -run TestRedisProxy_BindWritesPGAndBinding -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/proxy_redis.go internal/store/proxy_redis_bind_test.go
git commit -m "feat(store): redis proxy bind/release with PG durable single-row writes"
```

---

### Task 7: (B6) Manager 分派 + 启动重建接线

**Files:**
- Modify: `internal/store/manager.go` (construct allocator + rebuild in newManager)
- Modify: `internal/store/proxy.go` (`BindProxy`/`ReleaseProxy`/`ReportProxyFailure`/`ReportProxySuccess` dispatch by backend)
- Test: `internal/store/proxy_dispatch_test.go`

**Interfaces:**
- Consumes: B5 `bind`/`releaseBinding`, B3 `markDead`/`markAlive`, B4 `rebuildFromPG`.
- Produces: `Manager.proxyAlloc *redisProxyAllocator` (nil when pg backend). Public `BindProxy(ctx, jid, cc)` / `ReleaseProxy(ctx, jid)` unchanged signatures; internally route.

- [ ] **Step 1: Write the failing test** (redis backend Manager binds via ZSET; pg backend unchanged)

```go
func TestManager_ProxyBackendDispatch(t *testing.T) {
	ctx := context.Background()
	m := newManagerWithSchemaProxyRedis(t) // helper: schema + cfg.ProxyBackend="redis" + cfg.Redis
	mustExec(t, m.bizPool, `INSERT INTO account_devices (account_jid, tenant_id) VALUES ('acc1',1)`)
	mustExec(t, m.bizPool, `INSERT INTO proxy_pool (proxy_url,proxy_type,country_code,is_alive,max_bindings,current_bindings) VALUES ('socks5://p','socks5','US',true,1,0)`)
	// newManager should have rebuilt the hot index at Init; if the helper builds
	// Manager before inserting proxies, call m.proxyAlloc.rebuildFromPG manually.
	if m.proxyAlloc != nil {
		_, _ = m.proxyAlloc.rebuildFromPG(ctx, m.bizPool, nowMsForTest())
	}
	b, err := m.BindProxy(ctx, "acc1", "US")
	if err != nil || b.ProxyURL != "socks5://p" {
		t.Fatalf("BindProxy via redis = %+v,%v", b, err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/ -run TestManager_ProxyBackendDispatch -v`
Expected: FAIL — `proxyAlloc` field / helper undefined.

- [ ] **Step 3: Implement**

In `internal/store/manager.go`: add field `proxyAlloc *redisProxyAllocator` to `Manager`; in `newManager`, after pools are built:
```go
if cfg.ProxyBackend == "redis" && cfg.Redis != nil {
	m.proxyAlloc = newRedisProxyAllocator(cfg.Redis, cfg.ProxyCooldown)
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	if _, err := m.proxyAlloc.rebuildFromPG(rctx, m.bizPool, time.Now().UnixMilli()); err != nil {
		rcancel()
		m.Close()
		return nil, fmt.Errorf("rebuild redis proxy index: %w", err)
	}
	rcancel()
}
```
> `time.Now().UnixMilli()` is fine in production code (the ban on Date.now is a workflow-script constraint, not a Go constraint).

In `internal/store/proxy.go`, route at the top of each method (keep the existing pg body as the default branch):
```go
func (m *Manager) BindProxy(ctx context.Context, accountJID, countryCode string) (*ProxyBinding, error) {
	if m.proxyAlloc != nil {
		return m.proxyAlloc.bind(ctx, m.bizPool, accountJID, countryCode, time.Now().UnixMilli())
	}
	// ... existing pg SKIP LOCKED implementation unchanged ...
}
func (m *Manager) ReleaseProxy(ctx context.Context, accountJID string) error {
	if m.proxyAlloc != nil {
		return m.proxyAlloc.releaseBinding(ctx, m.bizPool, accountJID, time.Now().UnixMilli())
	}
	// ... existing pg implementation unchanged ...
}
```
For `ReportProxyFailure`/`ReportProxySuccess`: keep the existing PG update (durable), and when `m.proxyAlloc != nil` ALSO update Redis:
- failure → if it returns `dead==true`, look up the proxy's cc (`SELECT country_code FROM proxy_pool WHERE id=$1`) and `m.proxyAlloc.markDead(ctx, id, cc)`.
- success → look up cc + free (`max_bindings-current_bindings`) + build meta, `m.proxyAlloc.markAlive(...)` (revive into the ring).

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/ -run TestManager_ProxyBackendDispatch -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/manager.go internal/store/proxy.go internal/store/proxy_dispatch_test.go
git commit -m "feat(store): dispatch proxy bind/release/report to redis ring (pg default) + boot rebuild"
```

---

### Task 8: (B7) 端到端集成测试（冷却/容量/死代理/重建/粘性）+ make gate

**Files:**
- Create: `internal/store/proxy_zset_integration_test.go`

**Interfaces:**
- Consumes: everything above via public `Manager.BindProxy`/`ReleaseProxy`/`ReportProxyFailure`/`GetBoundProxy`.

- [ ] **Step 1: Write the integration test**

```go
func TestProxyRedis_EndToEnd(t *testing.T) {
	ctx := context.Background()
	m := newManagerWithSchemaProxyRedis(t)
	mustExec(t, m.bizPool, `INSERT INTO account_devices (account_jid,tenant_id) VALUES ('a1',1),('a2',1)`)
	mustExec(t, m.bizPool, `INSERT INTO proxy_pool (proxy_url,proxy_type,country_code,is_alive,max_bindings,current_bindings) VALUES ('socks5://only','socks5','US',true,1,0)`)
	if _, err := m.proxyAlloc.rebuildFromPG(ctx, m.bizPool, time.Now().UnixMilli()); err != nil { t.Fatal(err) }

	// a1 binds the only US proxy.
	b1, err := m.BindProxy(ctx, "a1", "US")
	if err != nil { t.Fatalf("a1 bind: %v", err) }

	// cooldown: a2 cannot get the same proxy immediately (it's cooling + capacity 0).
	if _, err := m.BindProxy(ctx, "a2", "US"); err != ErrNoProxyAvailable {
		t.Fatalf("a2 bind should miss (cooldown+capacity); got %v", err)
	}

	// stickiness: GetBoundProxy reads PG durable binding for a1.
	pb, err := m.GetBoundProxy(ctx, "a1")
	if err != nil || pb.ProxyURL != b1.ProxyURL {
		t.Fatalf("GetBoundProxy sticky mismatch: %+v,%v", pb, err)
	}

	// dead-proxy exclusion: kill the proxy → even after release it's excluded.
	for i := 0; i < proxyFailureThreshold; i++ {
		_, _ = m.ReportProxyFailure(ctx, b1.ProxyID)
	}
	_ = m.ReleaseProxy(ctx, "a1")
	if _, err := m.BindProxy(ctx, "a2", "US"); err != ErrNoProxyAvailable {
		t.Fatalf("dead proxy must stay excluded from ring; got %v", err)
	}
}
```
> `newManagerWithSchemaProxyRedis(t)` — a test helper building a Manager with real PG (migrations) + a testcontainers redis + `cfg.ProxyBackend="redis"`. Add it to the store test support file (mirror `newManagerWithSchema` + the redis helper used by `TestRedisProxy_*`).

- [ ] **Step 2: Run to verify it fails / then passes**

Run: `cd /var/klwa && go test ./internal/store/ -run TestProxyRedis_EndToEnd -race -v`
Expected: FAIL first if helper missing → add helper → PASS.

- [ ] **Step 3: Full gate (touches shared store package)**

Run: `cd /var/klwa && go build ./... && go vet ./... && make gate`
Expected: `make gate` (tidy+vet+test-race+labels+vuln) GREEN. Default `WADIST_PROXY_BACKEND` unset → pg path → existing proxy tests unchanged.

- [ ] **Step 4: Commit**

```bash
cd /var/klwa
git add internal/store/proxy_zset_integration_test.go internal/store/*testsupport*_test.go
git commit -m "test(store): redis proxy ring e2e (cooldown/capacity/dead/sticky) + gate green"
```

---

## Self-Review 结论（作者已核对）

- **Spec 覆盖**（§4.1 决策/§4.2/§9/§10 M2）：Redis 锁硬化 = A1（hbTTL 解耦 + 启动守卫；redis chaos 平价已由现存 `TestTakeoverChaos_Redis` 覆盖，无需重建）；代理 ZSET 冷却圈 = B2（pick 冷却+容量）、B3（release/dead/alive）、B4（PG 重建）、B5（bind/release + PG 持久写）、B6（分派 + 启动接线）、B7（e2e：冷却/容量/死代理/重建/粘性）。「PG 真相源」= B4/B5 单行写 + 启动重建；「粘性不变」= 不改 StickyBindProxy，B7 验证 GetBoundProxy 读 PG。
- **占位符扫描**：无 TODO/“类似上文”。测试助手名（`newTestRedis`/`newManagerWithSchema`/`mustExec`）标注为「复用现有 store 测试助手，名字不符时用真实等价物」——这是对现有测试设施的对齐，非占位。
- **类型一致性**：`redisProxyAllocator`、`availKey`/`proxyFreeKey`/`proxyMetaKey`、`pick`/`release`/`markDead`/`markAlive`/`rebuildFromPG`/`bind`/`releaseBinding`、`Manager.proxyAlloc` 跨 Task 一致；`BindProxy`/`ReleaseProxy` 公开签名不变（只内部分派）。
- **零爆炸半径**：两后端默认 pg；未设 env 时 A1 仅改 redis 路径（pg 不受影响），B* 的 `proxyAlloc==nil` 时全部走原 pg 实现。

## 已知风险与后续（非阻塞，记入执行账本）

- **Redis/PG 容量双写非跨系统原子**：bind 时 Redis pick 成功但 PG 写失败 → 已 `release` 归还槽（不泄漏）；PG 成功但进程崩溃 → PG 持久为准，Redis 冷却该代理（最多闲置一个 cooldown，启动重建自愈）。失败模式均良性。
- **无周期性 Redis↔PG 对账**：M2 靠启动重建纠偏；运行期漂移的周期 reconcile 留作后续（可复用 control 面模式）。
- **max_bindings>1**：容量用 `proxy:free` hash 支持，但目标是 1:1 账号:代理粘性，>1 为兼容路径，e2e 未专门覆盖多槽并发（可后续加）。
- **markAlive/markDead 的 cc 依赖**：ReportProxy* 需一次 `SELECT country_code`（单行，低频，仅代理死/活翻转时）。
