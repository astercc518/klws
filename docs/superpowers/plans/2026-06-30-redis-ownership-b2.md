# Redis 租约所有权（B2）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用 Redis 租约 + fencing token 取代 PG advisory 锁作为账号所有权机制，拆掉 `MaxLockConns` 并发天花板（~2000 → 受内存限的 ~1.5 万），且经 `cluster.DeviceLockHandle` 接口缝隔离，使 `cluster`/`dispatch`/`sendgate`/`billing` 零改动。

**Architecture:** 在 `internal/store` 引入可插拔 `Ownership` 策略接口，现有 PG 逻辑逐字搬为 `pgOwnership`（默认、行为不变），新增 `redisOwnership`（3 个原子 Lua：Acquire CAS / StillOwner / Release + 节点心跳 TTL）与 `shadowOwnership`（PG 权威 + Redis 影子比对）。后端由 `WADIST_OWNERSHIP_BACKEND ∈ {pg,redis,shadow}` 选择。迁移走 shadow → canary → cutover，owner_node 列于 cutover 后单独迁移移除。

**Tech Stack:** Go 1.26、pgx/v5（pgxpool）、go-redis/v9（Lua EVAL）、whatsmeow、testcontainers（PG16 + Redis7）、Prometheus。

## Global Constraints

- 红线：**不得修改** `internal/billing`/`internal/dispatch`/`internal/sendgate` 的业务逻辑，及 `internal/store`/`internal/cluster` 的**计费/防封/RLS/防双开事务语义**。本计划允许改 `internal/store`（所有权机制）与 `cmd/wadist`、`internal/config`、`internal/metrics`、`docker-compose*.yml`（非红线）。`internal/cluster`/`internal/node` 逻辑**零改动**（仅 node 新增测试）。
- 接口缝：`cluster.DeviceLockHandle interface { Healthy(ctx) bool; Release(ctx) }` 不变；所有锁句柄必须满足它。
- `backend=pg` 时行为必须与现状**字节一致**（现有测试全绿）。
- Redis 错误一律 **fail-closed**（`Healthy` 返回 false）。
- Redis 客户端库导入别名统一：`goredis "github.com/redis/go-redis/v9"`（与 `internal/sendgate/admission.go` 一致）。
- 每个 Lua 脚本用 `goredis.NewScript` 预编译。
- 提交粒度：每个 Task 末尾提交一次；`make gate` 在合并前必须绿。
- owner_node 列移除是**最后一个 Task，且仅在生产 cutover 后执行**——计划内其余 Task 全程保留该列。

---

## File Structure

- Create `internal/store/ownership.go` — `Ownership` + `LockHandle` 接口、后端选择 `newOwnership`。
- Create `internal/store/ownership_pg.go` — `pgOwnership`（搬运现有 advisory 锁 + cluster_nodes + owner_node 逻辑）。
- Create `internal/store/ownership_redis.go` — `redisOwnership` + `redisLockHandle` + 3 个 Lua。
- Create `internal/store/ownership_shadow.go` — `shadowOwnership`（PG 权威 + Redis 影子 + 分歧计数回调）。
- Create `internal/store/ownership_redis_test.go`、`ownership_race_test.go`、`ownership_shadow_test.go`、`redis_helper_test.go`。
- Modify `internal/store/manager.go` — `Manager` 加 `ownership` 字段；`AcquireDeviceLock`/`UpsertNodeHeartbeat`/`StaleOwnedAccounts`/`ListUnownedActiveAccounts`/`DeregisterNode` 委托。
- Modify `internal/store/config.go` — `Config` 加 `OwnershipBackend string`、`Redis *goredis.Client`。
- Modify `internal/config/config.go` — 加 `OwnershipBackend` env + `cfg.Store()` 填充。
- Modify `cmd/wadist/main.go` — rdb 前移并注入 store。
- Modify `internal/metrics/metrics.go` — 加 `ownershipShadowDivergence` 计数。
- Create `internal/node/chaos_redis_test.go` — Redis 接管平价测试。
- Modify `docker-compose.yml` / `docker-compose.prod.yml` — Redis 开 AOF、提 maxmemory。
- Create `migrations/0017_drop_owner_node.sql` — **最后 Task，cutover 后执行**。

---

## Task 1: 抽出 `Ownership` 接口并把现有 PG 逻辑搬为 `pgOwnership`（零行为变化）

**Files:**
- Create: `internal/store/ownership.go`
- Create: `internal/store/ownership_pg.go`
- Modify: `internal/store/manager.go`（加字段 + 委托）
- Test: 复用现有 `internal/store/*_test.go`、`internal/node/chaos_test.go`（必须保持绿）

**Interfaces:**
- Produces:
  - `type LockHandle interface { Healthy(ctx context.Context) bool; Release(ctx context.Context) }`
  - `type Ownership interface { Acquire(ctx context.Context, jid, nodeID string) (LockHandle, error); Heartbeat(ctx context.Context, nodeID string) error; Deregister(ctx context.Context, nodeID string) error; StaleOwned(ctx context.Context, staleness time.Duration) ([]string, error); Unowned(ctx context.Context) ([]string, error) }`
  - `Manager.ownership Ownership`（私有字段）

- [ ] **Step 1: 写接口文件**（`internal/store/ownership.go`）

```go
// internal/store/ownership.go
package store

import (
	"context"
	"time"
)

// LockHandle 是一次账号所有权抢占的句柄。它必须满足 cluster.DeviceLockHandle
// (Healthy/Release)，从而 cluster/node 无需感知底层是 PG 锁还是 Redis 租约。
type LockHandle interface {
	Healthy(ctx context.Context) bool
	Release(ctx context.Context)
}

// Ownership 是可插拔的账号所有权后端：PG advisory 锁 或 Redis 租约。
type Ownership interface {
	// Acquire 抢占 jid 的独占权；被活跃节点持有时返回 ErrDeviceLocked。
	Acquire(ctx context.Context, jid, nodeID string) (LockHandle, error)
	// Heartbeat 刷新本节点活性。
	Heartbeat(ctx context.Context, nodeID string) error
	// Deregister 优雅注销本节点（清其所有权台账）。
	Deregister(ctx context.Context, nodeID string) error
	// StaleOwned 返回归属于已过期节点的账号（接管候选）。
	StaleOwned(ctx context.Context, staleness time.Duration) ([]string, error)
	// Unowned 返回 active 但无主的账号。
	Unowned(ctx context.Context) ([]string, error)
}
```

- [ ] **Step 2: 写 `pgOwnership`，把现有逻辑逐字搬入**（`internal/store/ownership_pg.go`）

```go
// internal/store/ownership_pg.go
package store

import (
	"context"
	"time"
)

// pgOwnership 是现状所有权实现：PG session 级 advisory 锁 + cluster_nodes 心跳
// + account_devices.owner_node 台账。行为与重构前字节一致。
type pgOwnership struct{ m *Manager }

func (o *pgOwnership) Acquire(ctx context.Context, jid, nodeID string) (LockHandle, error) {
	return o.m.acquirePGLock(ctx, jid) // 现 AcquireDeviceLock 的函数体，见 Step 3
}
func (o *pgOwnership) Heartbeat(ctx context.Context, nodeID string) error {
	return o.m.upsertNodeHeartbeatPG(ctx, nodeID)
}
func (o *pgOwnership) Deregister(ctx context.Context, nodeID string) error {
	return o.m.deregisterNodePG(ctx, nodeID)
}
func (o *pgOwnership) StaleOwned(ctx context.Context, staleness time.Duration) ([]string, error) {
	return o.m.staleOwnedAccountsPG(ctx, staleness)
}
func (o *pgOwnership) Unowned(ctx context.Context) ([]string, error) {
	return o.m.listUnownedActiveAccountsPG(ctx)
}
```

- [ ] **Step 3: 重命名现有实现体为私有 PG 方法**

在 `internal/store/lock.go` 把 `func (m *Manager) AcquireDeviceLock(...)` **重命名**为 `func (m *Manager) acquirePGLock(...)`（函数体不动，返回 `*DeviceLock`，它已满足 `LockHandle`）。
在 `internal/store/node.go` 把 `UpsertNodeHeartbeat`→`upsertNodeHeartbeatPG`、`DeregisterNode`→`deregisterNodePG`、`StaleOwnedAccounts`→`staleOwnedAccountsPG`、`ListUnownedActiveAccounts`→`listUnownedActiveAccountsPG`（函数体不动）。
`ClaimAccount` **保持 public 不变**（owner_node 镜像，shadow 期需要）。

- [ ] **Step 4: 在 `manager.go` 加 `ownership` 字段、默认 pg、补回 public 委托方法**

`Manager` struct 加字段：

```go
	ownership Ownership
```

在 `newManager` 末尾（返回 mgr 前）默认装 pg 后端：

```go
	m.ownership = &pgOwnership{m: m}
```

新增/恢复 public 委托（放 `internal/store/node.go` 末尾）：

```go
func (m *Manager) AcquireDeviceLock(ctx context.Context, jid string) (LockHandle, error) {
	return m.ownership.Acquire(ctx, jid, m.cfg.NodeID)
}
func (m *Manager) UpsertNodeHeartbeat(ctx context.Context, nodeID string) error {
	return m.ownership.Heartbeat(ctx, nodeID)
}
func (m *Manager) DeregisterNode(ctx context.Context, nodeID string) error {
	return m.ownership.Deregister(ctx, nodeID)
}
func (m *Manager) StaleOwnedAccounts(ctx context.Context, staleness time.Duration) ([]string, error) {
	return m.ownership.StaleOwned(ctx, staleness)
}
func (m *Manager) ListUnownedActiveAccounts(ctx context.Context) ([]string, error) {
	return m.ownership.Unowned(ctx)
}
```

> 注意：`AcquireDeviceLock` 现返回 `LockHandle`（接口）而非 `*DeviceLock`。`node/orchestrator.go` 用它做 `cluster.DeviceLockHandle` 透传——接口兼容，无需改 node。chaos_test.go 直接调 `m.AcquireDeviceLock` 后用 `KillConnForTest`：该测试需要具体 `*DeviceLock`，故在 chaos_test.go 内对返回值做类型断言 `lockA.(*store.DeviceLock)`（仅测试改动，Step 5 验证）。

- [ ] **Step 5: 跑全量回归，确认零行为变化**

Run: `go build ./... && go test ./internal/store/... ./internal/node/... -run 'Lock|Takeover|Node' -count=1`
Expected: PASS（若 chaos_test.go 因 `AcquireDeviceLock` 返回类型变化编译失败，按 Step 4 注释加类型断言 `lockA := mustPGLock(t, m.AcquireDeviceLock(ctx, jid))`，其中 helper 做 `.(*DeviceLock)`）。

- [ ] **Step 6: 提交**

```bash
git add internal/store/ownership.go internal/store/ownership_pg.go internal/store/lock.go internal/store/node.go internal/store/manager.go internal/node/chaos_test.go
git commit -m "refactor(store): extract Ownership interface; move PG logic to pgOwnership (no behavior change)"
```

---

## Task 2: Redis 测试夹具 + 3 个 Lua + `redisLockHandle`

**Files:**
- Create: `internal/store/redis_helper_test.go`
- Create: `internal/store/ownership_redis.go`
- Test: `internal/store/ownership_redis_test.go`

**Interfaces:**
- Consumes: `LockHandle`（Task 1）
- Produces:
  - `type redisLockHandle struct{ rdb *goredis.Client; jid, nodeID string; fence int64 }`，满足 `LockHandle`
  - Lua 脚本变量 `luaAcquire`, `luaStillOwner`, `luaRelease`（`*goredis.Script`）
  - key 构造函数 `ownerKey(jid)`, `hbKey(node)`, `ownedKey(node)`, `fenceKey(jid)`

- [ ] **Step 1: 写 Redis 测试夹具**（testcontainers，沿用仓库风格）

```go
// internal/store/redis_helper_test.go
package store

import (
	"context"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func newTestRedis(t *testing.T) *goredis.Client {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:7",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForListeningPort("6379/tcp"),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "6379/tcp")
	rdb := goredis.NewClient(&goredis.Options{Addr: host + ":" + port.Port()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}
```

Run: `go test ./internal/store/ -run TestNothingYet -count=1`（编译夹具）
Expected: PASS（无匹配测试，编译通过即可）

- [ ] **Step 2: 写失败测试 — Acquire 设置 owner+fence，StillOwner 真，Release 清除**

```go
// internal/store/ownership_redis_test.go
package store

import (
	"context"
	"testing"
	"time"
)

func TestRedisAcquireStillOwnerRelease(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	o := newRedisOwnership(rdb)

	if err := o.Heartbeat(ctx, "node-A"); err != nil { t.Fatal(err) }
	h, err := o.Acquire(ctx, "jid-1", "node-A")
	if err != nil { t.Fatalf("acquire: %v", err) }
	if !h.Healthy(ctx) { t.Fatal("freshly acquired must be Healthy") }

	got, _ := rdb.Get(ctx, ownerKey("jid-1")).Result()
	if got != "node-A:1" { t.Fatalf("owner=%q want node-A:1", got) }

	h.Release(ctx)
	if n, _ := rdb.Exists(ctx, ownerKey("jid-1")).Result(); n != 0 {
		t.Fatal("owner key must be gone after Release")
	}
	_ = time.Second
}
```

Run: `go test ./internal/store/ -run TestRedisAcquireStillOwnerRelease -count=1`
Expected: FAIL（`newRedisOwnership`/`ownerKey` 未定义）

- [ ] **Step 3: 实现 Lua + handle + 构造**（`internal/store/ownership_redis.go`）

```go
// internal/store/ownership_redis.go
package store

import (
	"context"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const hbTTL = 30 * time.Second // = NodeStaleness；心跳间隔应 = hbTTL/3

func ownerKey(jid string) string  { return "owner:" + jid }
func hbKey(node string) string    { return "node:hb:" + node }
func ownedKey(node string) string { return "owned:" + node }
func fenceKey(jid string) string  { return "fence:" + jid }

// KEYS=[owner:{jid}, fence:{jid}, owned:{me}] ARGV=[me, "node:hb:"]
var luaAcquire = goredis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if cur then
  local node = string.match(cur, "^(.-):")
  if redis.call('EXISTS', ARGV[2]..node) == 1 then return {0, cur} end
end
local fence = redis.call('INCR', KEYS[2])
redis.call('SET', KEYS[1], ARGV[1]..':'..fence)
redis.call('SADD', KEYS[3], ARGV[3])
return {1, fence}`)

// KEYS=[owner:{jid}, node:hb:{me}] ARGV=[me, fence]
var luaStillOwner = goredis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1]..':'..ARGV[2] then return 0 end
if redis.call('EXISTS', KEYS[2]) == 0 then return 0 end
return 1`)

// KEYS=[owner:{jid}, owned:{me}] ARGV=[me, fence, jid]
var luaRelease = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1]..':'..ARGV[2] then
  redis.call('DEL', KEYS[1]); redis.call('SREM', KEYS[2], ARGV[3])
end
return 1`)

type redisLockHandle struct {
	rdb    *goredis.Client
	jid    string
	nodeID string
	fence  int64
}

func (h *redisLockHandle) Healthy(ctx context.Context) bool {
	v, err := luaStillOwner.Run(ctx, h.rdb,
		[]string{ownerKey(h.jid), hbKey(h.nodeID)}, h.nodeID, h.fence).Int()
	if err != nil { return false } // fail-closed
	return v == 1
}

func (h *redisLockHandle) Release(ctx context.Context) {
	_ = luaRelease.Run(ctx, h.rdb,
		[]string{ownerKey(h.jid), ownedKey(h.nodeID)}, h.nodeID, h.fence, h.jid).Err()
}

var _ LockHandle = (*redisLockHandle)(nil)

type redisOwnership struct{ rdb *goredis.Client }

func newRedisOwnership(rdb *goredis.Client) *redisOwnership { return &redisOwnership{rdb: rdb} }

func (o *redisOwnership) Acquire(ctx context.Context, jid, nodeID string) (LockHandle, error) {
	res, err := luaAcquire.Run(ctx, o.rdb,
		[]string{ownerKey(jid), fenceKey(jid), ownedKey(nodeID)}, nodeID, hbKeyPrefix, jid).Slice()
	if err != nil { return nil, err }
	ok, _ := res[0].(int64)
	if ok == 0 { return nil, ErrDeviceLocked }
	fence, _ := res[1].(int64)
	return &redisLockHandle{rdb: o.rdb, jid: jid, nodeID: nodeID, fence: fence}, nil
}

const hbKeyPrefix = "node:hb:"

// helper for tests/observability
func splitOwner(v string) (node string, rest string) {
	i := strings.Index(v, ":")
	if i < 0 { return v, "" }
	return v[:i], v[i+1:]
}
```

> 注：`luaAcquire` 的 `KEYS[3]=owned:{me}`，`ARGV[3]=jid`（加入 owned 集）。上面 `Run(..., nodeID, hbKeyPrefix, jid)` 即 ARGV=[me, "node:hb:", jid]，与脚本 `ARGV[1]/ARGV[2]/ARGV[3]` 对应。

- [ ] **Step 4: 跑测试转绿**

Run: `go test ./internal/store/ -run TestRedisAcquireStillOwnerRelease -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/store/redis_helper_test.go internal/store/ownership_redis.go internal/store/ownership_redis_test.go
git commit -m "feat(store): redis ownership Lua scripts + lock handle (Acquire/StillOwner/Release)"
```

---

## Task 3: `redisOwnership` 的 Heartbeat / Deregister / StaleOwned / Unowned

**Files:**
- Modify: `internal/store/ownership_redis.go`
- Test: `internal/store/ownership_redis_test.go`

**Interfaces:**
- Consumes: `redisOwnership`, key helpers（Task 2）；`Manager.ListActiveAccounts`（既有，PG account roster）
- Produces: `redisOwnership` 满足 `Ownership`；`newRedisOwnership` 需要 `roster func(ctx) ([]string, error)` 以列举 active 账号

- [ ] **Step 1: 写失败测试 — 心跳 TTL、死节点 StaleOwned、无主 Unowned**

```go
func TestRedisHeartbeatStaleUnowned(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	roster := func(context.Context) ([]string, error) { return []string{"jid-1", "jid-2"}, nil }
	o := newRedisOwnershipWithRoster(rdb, roster)

	_ = o.Heartbeat(ctx, "node-A")
	if n, _ := rdb.Exists(ctx, hbKey("node-A")).Result(); n != 1 { t.Fatal("hb key missing") }
	ttl, _ := rdb.TTL(ctx, hbKey("node-A")).Result()
	if ttl <= 0 || ttl > hbTTL { t.Fatalf("hb ttl=%v", ttl) }

	if _, err := o.Acquire(ctx, "jid-1", "node-A"); err != nil { t.Fatal(err) }
	// jid-2 unowned
	un, _ := o.Unowned(ctx)
	if len(un) != 1 || un[0] != "jid-2" { t.Fatalf("unowned=%v want [jid-2]", un) }

	// kill node-A heartbeat → jid-1 becomes stale
	rdb.Del(ctx, hbKey("node-A"))
	st, _ := o.StaleOwned(ctx, hbTTL)
	if len(st) != 1 || st[0] != "jid-1" { t.Fatalf("stale=%v want [jid-1]", st) }
}
```

Run: `go test ./internal/store/ -run TestRedisHeartbeatStaleUnowned -count=1`
Expected: FAIL（`newRedisOwnershipWithRoster`/`Heartbeat`/`StaleOwned`/`Unowned` 未定义）

- [ ] **Step 2: 实现四方法 + roster 注入**

在 `ownership_redis.go`：

```go
type redisOwnership struct {
	rdb    *goredis.Client
	roster func(ctx context.Context) ([]string, error) // active 账号花名册（PG）
}

func newRedisOwnership(rdb *goredis.Client) *redisOwnership { return &redisOwnership{rdb: rdb} }
func newRedisOwnershipWithRoster(rdb *goredis.Client, roster func(context.Context) ([]string, error)) *redisOwnership {
	return &redisOwnership{rdb: rdb, roster: roster}
}

func (o *redisOwnership) Heartbeat(ctx context.Context, nodeID string) error {
	return o.rdb.Set(ctx, hbKey(nodeID), "1", hbTTL).Err()
}

func (o *redisOwnership) Deregister(ctx context.Context, nodeID string) error {
	jids, err := o.rdb.SMembers(ctx, ownedKey(nodeID)).Result()
	if err != nil { return err }
	pipe := o.rdb.TxPipeline()
	for _, jid := range jids {
		pipe.Del(ctx, ownerKey(jid))
	}
	pipe.Del(ctx, ownedKey(nodeID))
	pipe.Del(ctx, hbKey(nodeID))
	_, err = pipe.Exec(ctx)
	return err
}

func (o *redisOwnership) StaleOwned(ctx context.Context, _ time.Duration) ([]string, error) {
	// 遍历所有 node 的 owned 集；其 hb 缺失即 stale。单机/少节点规模可接受。
	nodes, err := o.rdb.Keys(ctx, "owned:*").Result()
	if err != nil { return nil, err }
	var out []string
	for _, ok := range nodes {
		node := strings.TrimPrefix(ok, "owned:")
		if n, _ := o.rdb.Exists(ctx, hbKey(node)).Result(); n == 1 { continue }
		jids, _ := o.rdb.SMembers(ctx, ok).Result()
		out = append(out, jids...)
	}
	return out, nil
}

func (o *redisOwnership) Unowned(ctx context.Context) ([]string, error) {
	if o.roster == nil { return nil, nil }
	all, err := o.roster(ctx)
	if err != nil { return nil, err }
	var out []string
	for _, jid := range all {
		if n, _ := o.rdb.Exists(ctx, ownerKey(jid)).Result(); n == 0 {
			out = append(out, jid)
		}
	}
	return out, nil
}
```

> 规模说明：`Keys("owned:*")` 仅遍历节点数（个位数），非 10 万账号，安全。`Unowned` 的 roster 遍历在接管扫描频率（~15s）下对 10 万账号是 10 万次 EXISTS——**改进项**：用 `MGET`/pipeline 批量（Task 3 Step 4 优化）。

- [ ] **Step 3: 跑测试转绿**

Run: `go test ./internal/store/ -run TestRedisHeartbeatStaleUnowned -count=1`
Expected: PASS

- [ ] **Step 4: 把 Unowned 的 EXISTS 循环改 pipeline 批量（性能）**

```go
func (o *redisOwnership) Unowned(ctx context.Context) ([]string, error) {
	if o.roster == nil { return nil, nil }
	all, err := o.roster(ctx)
	if err != nil { return nil, err }
	pipe := o.rdb.Pipeline()
	cmds := make([]*goredis.IntCmd, len(all))
	for i, jid := range all { cmds[i] = pipe.Exists(ctx, ownerKey(jid)) }
	if _, err := pipe.Exec(ctx); err != nil { return nil, err }
	var out []string
	for i, c := range cmds {
		if c.Val() == 0 { out = append(out, all[i]) }
	}
	return out, nil
}
```

Run: `go test ./internal/store/ -run TestRedisHeartbeatStaleUnowned -count=1`
Expected: PASS（行为不变，批量化）

- [ ] **Step 5: 提交**

```bash
git add internal/store/ownership_redis.go internal/store/ownership_redis_test.go
git commit -m "feat(store): redis ownership heartbeat/deregister/stale/unowned"
```

---

## Task 4: 并发不变量 — Acquire 竞态、GC-pause fencing

**Files:**
- Test: `internal/store/ownership_race_test.go`

**Interfaces:**
- Consumes: `newRedisOwnership`, `redisOwnership.Acquire/Heartbeat`（Task 2/3）

- [ ] **Step 1: 写竞态测试 — 两节点抢同一 jid，恰一个成功**

```go
// internal/store/ownership_race_test.go
package store

import (
	"context"
	"sync"
	"testing"
)

func TestRedisAcquireRace(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	o := newRedisOwnership(rdb)
	_ = o.Heartbeat(ctx, "A"); _ = o.Heartbeat(ctx, "B")

	var wg sync.WaitGroup
	var okCount int32
	var mu sync.Mutex
	for _, node := range []string{"A", "B"} {
		node := node
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := o.Acquire(ctx, "race-jid", node); err == nil {
				mu.Lock(); okCount++; mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okCount != 1 { t.Fatalf("exactly one acquire must win, got %d", okCount) }
}
```

Run: `go test ./internal/store/ -run TestRedisAcquireRace -count=5`
Expected: PASS（Lua 原子性保证恰一个）

- [ ] **Step 2: 写 GC-pause fencing 测试 — 旧 owner 醒来不再 Healthy**

```go
func TestRedisFencingGCPause(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	rdb := newTestRedis(t)
	o := newRedisOwnership(rdb)
	_ = o.Heartbeat(ctx, "A")

	hA, err := o.Acquire(ctx, "jid-x", "A")
	if err != nil { t.Fatal(err) }
	if !hA.Healthy(ctx) { t.Fatal("A should be healthy") }

	// 模拟 A 卡死：hb 过期 → B 接管
	rdb.Del(ctx, hbKey("A"))
	_ = o.Heartbeat(ctx, "B")
	hB, err := o.Acquire(ctx, "jid-x", "B")
	if err != nil { t.Fatalf("B acquire after A stale: %v", err) }
	_ = hB

	// A 醒来（hb 恢复）：仍不得 Healthy（fence 已被 B 顶替）
	_ = o.Heartbeat(ctx, "A")
	if hA.Healthy(ctx) { t.Fatal("stale owner A must NOT be healthy after B took over") }
}
```

Run: `go test ./internal/store/ -run TestRedisFencingGCPause -count=5`
Expected: PASS

- [ ] **Step 3: 提交**

```bash
git add internal/store/ownership_race_test.go
git commit -m "test(store): redis ownership race + GC-pause fencing invariants"
```

---

## Task 5: 后端选择接线（config → store → cmd/wadist）

**Files:**
- Modify: `internal/store/config.go`
- Modify: `internal/store/manager.go`
- Modify: `internal/config/config.go`
- Modify: `cmd/wadist/main.go`
- Test: `internal/store/ownership_select_test.go`

**Interfaces:**
- Consumes: `pgOwnership`、`redisOwnership`（Task 1-3）
- Produces: `Config.OwnershipBackend string`、`Config.Redis *goredis.Client`；`newOwnership(m) Ownership`

- [ ] **Step 1: 写失败测试 — backend=redis 时 Manager 使用 redisOwnership**

```go
// internal/store/ownership_select_test.go
package store

import "testing"

func TestNewOwnershipSelect(t *testing.T) {
	if got := backendName(&pgOwnership{}); got != "pg" { t.Fatalf("pg name=%s", got) }
	if got := backendName(&redisOwnership{}); got != "redis" { t.Fatalf("redis name=%s", got) }
	if got := backendName(&shadowOwnership{}); got != "shadow" { t.Fatalf("shadow name=%s", got) }
}
```

> `shadowOwnership` 在 Task 6 实现；本测试可先只断言 pg/redis 两行，Task 6 再补 shadow 行。

Run: `go test ./internal/store/ -run TestNewOwnershipSelect -count=1`
Expected: FAIL（`backendName` 未定义）

- [ ] **Step 2: Config 加字段**（`internal/store/config.go`）

```go
	OwnershipBackend string         // "pg"(默认) | "redis" | "shadow"
	Redis            *goredis.Client // redis/shadow 后端必需
```

`config.go` 顶部加 import `goredis "github.com/redis/go-redis/v9"`。`withDefaults` 加：

```go
	if c.OwnershipBackend == "" { c.OwnershipBackend = "pg" }
```

- [ ] **Step 3: manager.go 接线 `newOwnership` + `backendName`**

在 `newManager` 把 `m.ownership = &pgOwnership{m: m}` 替换为：

```go
	m.ownership = newOwnership(m)
```

新增（`ownership.go`）：

```go
func newOwnership(m *Manager) Ownership {
	switch m.cfg.OwnershipBackend {
	case "redis":
		return newRedisOwnershipWithRoster(m.cfg.Redis, m.ListActiveAccounts)
	case "shadow":
		return newShadowOwnership(&pgOwnership{m: m},
			newRedisOwnershipWithRoster(m.cfg.Redis, m.ListActiveAccounts), m.cfg.OnShadowDivergence)
	default:
		return &pgOwnership{m: m}
	}
}

func backendName(o Ownership) string {
	switch o.(type) {
	case *pgOwnership: return "pg"
	case *redisOwnership: return "redis"
	case *shadowOwnership: return "shadow"
	default: return "unknown"
	}
}
```

> `m.ListActiveAccounts` 签名 `func(ctx) ([]string, error)` 正是 roster 形状。`OnShadowDivergence func(op string)` 是 Config 新字段（Task 6 用，先加占位 `OnShadowDivergence func(op string)` 到 Config，默认 nil）。

- [ ] **Step 4: config/config.go 加 env + Store() 填充**

`Load()` 内加：

```go
		OwnershipBackend: getenv("WADIST_OWNERSHIP_BACKEND", "pg"),
```

`Config` struct 加 `OwnershipBackend string`。`cfg.Store()` 内加：

```go
		OwnershipBackend: c.OwnershipBackend,
		// Redis 由 cmd/wadist 注入（见下）
```

- [ ] **Step 5: cmd/wadist 把 rdb 前移并注入 store**

把 `rdb := goredis.NewClient(...)` 这一行**移到 `store.Init` 之前**，并改 Init 调用：

```go
	rdb := goredis.NewClient(&goredis.Options{Addr: cfg.RedisAddr})
	sc := cfg.Store()
	sc.Redis = rdb
	mgr, err := store.Init(ctx, sc, logger)
```

（删除后面原本第二次创建 rdb 的行，复用此 rdb。）

- [ ] **Step 6: 跑测试 + 构建**

Run: `go build ./... && go test ./internal/store/ -run TestNewOwnershipSelect -count=1`
Expected: PASS（shadow 行若未实现先注释，Task 6 恢复）

- [ ] **Step 7: 提交**

```bash
git add internal/store/config.go internal/store/manager.go internal/store/ownership.go internal/store/ownership_select_test.go internal/config/config.go cmd/wadist/main.go
git commit -m "feat(store): pluggable ownership backend selection via WADIST_OWNERSHIP_BACKEND"
```

---

## Task 6: `shadowOwnership` — PG 权威 + Redis 影子 + 分歧指标

**Files:**
- Create: `internal/store/ownership_shadow.go`
- Modify: `internal/metrics/metrics.go`（加计数）
- Modify: `internal/store/config.go`（`OnShadowDivergence func(op string)`）
- Modify: `cmd/wadist/main.go`（接 metric 回调）
- Test: `internal/store/ownership_shadow_test.go`

**Interfaces:**
- Consumes: `Ownership`、`LockHandle`（Task 1）、`pgOwnership`、`redisOwnership`
- Produces: `shadowOwnership`（满足 `Ownership`）；`newShadowOwnership(authoritative, shadow Ownership, onDiverge func(op string)) *shadowOwnership`

- [ ] **Step 1: 写失败测试 — Acquire 以 PG 为准；分歧触发回调**

```go
// internal/store/ownership_shadow_test.go
package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeOwnership struct {
	acquireErr error
	healthy    bool
}
func (f *fakeOwnership) Acquire(ctx context.Context, jid, node string) (LockHandle, error) {
	if f.acquireErr != nil { return nil, f.acquireErr }
	return fakeHandle{healthy: f.healthy}, nil
}
func (f *fakeOwnership) Heartbeat(context.Context, string) error { return nil }
func (f *fakeOwnership) Deregister(context.Context, string) error { return nil }
func (f *fakeOwnership) StaleOwned(context.Context, time.Duration) ([]string, error) { return nil, nil }
func (f *fakeOwnership) Unowned(context.Context) ([]string, error) { return nil, nil }

type fakeHandle struct{ healthy bool }
func (h fakeHandle) Healthy(context.Context) bool { return h.healthy }
func (h fakeHandle) Release(context.Context)       {}

func TestShadowAuthoritativeAndDivergence(t *testing.T) {
	ctx := context.Background()
	var diverged []string
	// PG 授权成功；Redis 影子失败 → 记一次 acquire 分歧，但返回 PG 结果(成功)
	s := newShadowOwnership(
		&fakeOwnership{acquireErr: nil},
		&fakeOwnership{acquireErr: errors.New("redis boom")},
		func(op string) { diverged = append(diverged, op) },
	)
	h, err := s.Acquire(ctx, "jid", "node")
	if err != nil || h == nil { t.Fatalf("authoritative(PG) success must propagate: %v", err) }
	if len(diverged) != 1 || diverged[0] != "acquire" {
		t.Fatalf("expected one acquire divergence, got %v", diverged)
	}
}
```

Run: `go test ./internal/store/ -run TestShadowAuthoritativeAndDivergence -count=1`
Expected: FAIL（`newShadowOwnership` 未定义）

- [ ] **Step 2: 实现 shadowOwnership**

```go
// internal/store/ownership_shadow.go
package store

import (
	"context"
	"time"
)

// shadowOwnership 以 authoritative(PG) 为准返回结果，同时影子运行 shadow(Redis)
// 并比对决策；不一致时调 onDiverge(op)。用于零风险灰度验证 Redis 逻辑。
type shadowOwnership struct {
	auth, shadow Ownership
	onDiverge    func(op string)
}

func newShadowOwnership(auth, shadow Ownership, onDiverge func(op string)) *shadowOwnership {
	if onDiverge == nil { onDiverge = func(string) {} }
	return &shadowOwnership{auth: auth, shadow: shadow, onDiverge: onDiverge}
}

func (s *shadowOwnership) Acquire(ctx context.Context, jid, nodeID string) (LockHandle, error) {
	authH, authErr := s.auth.Acquire(ctx, jid, nodeID)
	_, shErr := s.shadow.Acquire(ctx, jid, nodeID)
	if (authErr == nil) != (shErr == nil) { s.onDiverge("acquire") }
	return authH, authErr // 始终返回权威结果
}
func (s *shadowOwnership) Heartbeat(ctx context.Context, nodeID string) error {
	_ = s.shadow.Heartbeat(ctx, nodeID)
	return s.auth.Heartbeat(ctx, nodeID)
}
func (s *shadowOwnership) Deregister(ctx context.Context, nodeID string) error {
	_ = s.shadow.Deregister(ctx, nodeID)
	return s.auth.Deregister(ctx, nodeID)
}
func (s *shadowOwnership) StaleOwned(ctx context.Context, st time.Duration) ([]string, error) {
	a, err := s.auth.StaleOwned(ctx, st)
	if sh, e2 := s.shadow.StaleOwned(ctx, st); e2 == nil && len(sh) != len(a) { s.onDiverge("stale") }
	return a, err
}
func (s *shadowOwnership) Unowned(ctx context.Context) ([]string, error) {
	a, err := s.auth.Unowned(ctx)
	if sh, e2 := s.shadow.Unowned(ctx); e2 == nil && len(sh) != len(a) { s.onDiverge("unowned") }
	return a, err
}
```

- [ ] **Step 3: 加分歧计数到 metrics.go**

`Metrics` struct 加字段 `ownershipDivergence *prometheus.CounterVec // label: op`。在 `New` 内：

```go
	m.ownershipDivergence = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_ownership_shadow_divergence_total",
		Help: "shadow-mode ownership decision divergences",
	}, []string{"op"})
```

加进 `reg.MustRegister(...)` 列表；新增导出方法：

```go
func (m *Metrics) ObserveOwnershipDivergence(op string) { m.ownershipDivergence.WithLabelValues(op).Inc() }
```

- [ ] **Step 4: cmd/wadist 接回调**

在 `Config.Store()` 注入前（cmd/wadist）：`sc.OnShadowDivergence = m.ObserveOwnershipDivergence`。
（需把 metrics `m` 的构造移到 store.Init 之前，或用闭包延迟绑定；最简：`sc.OnShadowDivergence = func(op string){ m.ObserveOwnershipDivergence(op) }` 并保证 `m` 已建。）

`Config` 加字段 `OnShadowDivergence func(op string)`。

- [ ] **Step 5: 跑测试**

Run: `go test ./internal/store/ -run 'Shadow|NewOwnershipSelect' -count=1 && go build ./...`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/store/ownership_shadow.go internal/store/ownership_shadow_test.go internal/store/config.go internal/metrics/metrics.go cmd/wadist/main.go
git commit -m "feat(store): shadow ownership backend with divergence metric"
```

---

## Task 7: Redis 接管 chaos 平价测试

**Files:**
- Create: `internal/node/chaos_redis_test.go`

**Interfaces:**
- Consumes: `store.Manager`（backend=redis）、`store.ExpireNodeForTest`、`node.Orchestrator`、`cluster.Registry/Supervisor`

- [ ] **Step 1: 加测试钩子 `ExpireNodeForTest`**（`internal/store/ownership_redis.go`）

```go
// ExpireNodeForTest 删除节点心跳，模拟进程猝死（Redis 后端的 KillConnForTest 等价物）。
// 仅供集成测试使用。
func (m *Manager) ExpireNodeForTest(ctx context.Context, nodeID string) error {
	if m.cfg.Redis == nil { return nil }
	return m.cfg.Redis.Del(ctx, hbKey(nodeID)).Err()
}
```

- [ ] **Step 2: 写 chaos 平价测试**

```go
// internal/node/chaos_redis_test.go
package node

import (
	"context"
	"testing"
	"time"

	"github.com/acme/wadist/internal/cluster"
	"github.com/acme/wadist/internal/store"
	"github.com/hibiken/asynq"
)

func TestTakeoverChaos_Redis(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	redisAddr := startRedis(t)
	m := newTestManagerRedis(t, redisAddr) // backend=redis 的测试 Manager（见 Step 3）

	const jid = "chaos-redis-1"
	seedAccountDevice(t, ctx, m, jid)

	_ = m.UpsertNodeHeartbeat(ctx, "node-A")
	if _, err := m.AcquireDeviceLock(ctx, jid); err != nil { t.Fatalf("A acquire: %v", err) }
	// CHAOS：node-A 猝死
	if err := m.ExpireNodeForTest(ctx, "node-A"); err != nil { t.Fatal(err) }

	regB := cluster.NewRegistry()
	supB := cluster.NewSupervisor(regB, cluster.SupervisorOpts{Limit: 4, ShutdownTimeout: time.Second})
	t.Cleanup(supB.Shutdown)
	factoryB := func(_ context.Context, jid string, lock cluster.DeviceLockHandle) (*cluster.Session, error) {
		return cluster.NewSession(jid, &fakeConn{connected: true}, lock), nil
	}
	// node-B 必须有自己的活心跳，否则 acquire 后 StillOwner 立即 false
	mB := newTestManagerRedisAs(t, redisAddr, "node-B")
	_ = mB.UpsertNodeHeartbeat(ctx, "node-B")
	orchB := NewOrchestrator(mB, regB, supB, "node-B", factoryB, time.Second, nil)

	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	t.Cleanup(func() { _ = client.Close() })
	enq := NewTakeoverEnqueuer(client, 30*time.Second)
	if n, err := orchB.scanOnce(ctx, 30*time.Second, enq); err != nil || n < 1 {
		t.Fatalf("scanOnce n=%d err=%v", n, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		sess, err := orchB.StartAccountWithLock(ctx, jid)
		if err == nil && sess != nil { break }
		if time.Now().After(deadline) { t.Fatalf("B takeover failed: %v", err) }
		time.Sleep(100 * time.Millisecond)
	}

	owner, _ := redisGet(t, redisAddr, "owner:"+jid)
	if want := "node-B:"; len(owner) < len(want) || owner[:len(want)] != want {
		t.Fatalf("redis owner=%q want prefix node-B:", owner)
	}
	if _, ok := regB.Get(jid); !ok { t.Fatal("session missing in node-B registry") }
}
```

> `newTestManagerRedis`/`newTestManagerRedisAs`/`redisGet` 为本测试新增 helper（构造 backend=redis 的 store.Manager，nodeID 分别为 node-A/node-B，共享同一 Redis）。`seedAccountDevice`/`startRedis`/`fakeConn` 复用现有 node 测试夹具。

- [ ] **Step 3: 写测试 helper**

放入 `internal/node/chaos_redis_test.go`，镜像同包 `newTestManager(t)`（PG testcontainer + 迁移）但追加 redis 后端配置：

```go
import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func newTestManagerRedis(t *testing.T, redisAddr string) *store.Manager {
	return newTestManagerRedisAs(t, redisAddr, "node-A")
}

func newTestManagerRedisAs(t *testing.T, redisAddr, nodeID string) *store.Manager {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("app_test"), postgres.WithUsername("test"), postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(wait.ForAll(
			wait.ForListeningPort("5432/tcp"),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		).WithStartupTimeout(60*time.Second)))
	if err != nil { t.Fatalf("start postgres: %v", err) }
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil { t.Fatalf("dsn: %v", err) }

	rdb := goredis.NewClient(&goredis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = rdb.Close() })
	m, err := store.NewManager(ctx, store.Config{
		DSN: dsn, OwnershipBackend: "redis", Redis: rdb, NodeID: nodeID,
	}, waLog.Noop)
	if err != nil { t.Fatalf("newManager: %v", err) }
	t.Cleanup(m.Close)

	files, _ := filepath.Glob("../../migrations/*.sql")
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, ".down.sql") { continue }
		b, err := os.ReadFile(f)
		if err != nil { t.Fatalf("read %s: %v", f, err) }
		if _, err := m.BizPool().Exec(ctx, string(b)); err != nil { t.Fatalf("apply %s: %v", f, err) }
	}
	return m
}

func redisGet(t *testing.T, redisAddr, key string) (string, error) {
	t.Helper()
	rdb := goredis.NewClient(&goredis.Options{Addr: redisAddr})
	defer rdb.Close()
	return rdb.Get(context.Background(), key).Result()
}
```

- [ ] **Step 4: 跑平价测试 `-count=5`**

Run: `go test ./internal/node/ -run TestTakeoverChaos_Redis -count=5`
Expected: PASS（5 次全绿，无双 owner）

- [ ] **Step 5: 确认 PG 版仍绿**

Run: `go test ./internal/node/ -run TestTakeoverChaos -count=5`
Expected: PASS（pg 路径未回归）

- [ ] **Step 6: 提交**

```bash
git add internal/store/ownership_redis.go internal/node/chaos_redis_test.go
git commit -m "test(node): redis takeover chaos parity (-count=5)"
```

---

## Task 8: Redis AOF + maxmemory（compose）

**Files:**
- Modify: `docker-compose.yml`、`docker-compose.prod.yml`

**Interfaces:** 无代码接口；基础设施硬化。

- [ ] **Step 1: 改 redis command（两文件）**

把现有：
```yaml
    command: ["redis-server", "--maxmemory", "512mb", "--maxmemory-policy", "noeviction"]
```
改为：
```yaml
    command: ["redis-server", "--maxmemory", "2gb", "--maxmemory-policy", "noeviction", "--appendonly", "yes", "--appendfsync", "everysec"]
```

- [ ] **Step 2: 本地验证 redis 起得来且 AOF 生效**

Run: `docker compose -f docker-compose.yml up -d redis && docker compose -f docker-compose.yml exec redis redis-cli config get appendonly`
Expected: `appendonly` `yes`

- [ ] **Step 3: 提交**

```bash
git add docker-compose.yml docker-compose.prod.yml
git commit -m "ops(redis): enable AOF (everysec) + raise maxmemory to 2gb for ownership lease durability"
```

---

## Task 9（**仅 cutover 后执行**）: 移除 owner_node 列 + cluster_nodes 表

**Files:**
- Create: `migrations/0017_drop_owner_node.sql`
- Modify: `internal/store/node.go`（删 `ClaimAccount` 的 owner_node 写，或整删）、`internal/node/orchestrator.go`（删 `ClaimAccount` 调用）、`pgOwnership`（cluster_nodes 相关方法可整删）

**前置条件（硬门槛）：** `WADIST_OWNERSHIP_BACKEND=redis` 已在生产稳定运行、shadow 分歧指标连续 N 小时为 0、回滚窗口已过。**未满足不得执行本 Task。**

- [ ] **Step 1: 写迁移**

```sql
-- migrations/0017_drop_owner_node.sql
ALTER TABLE account_devices DROP COLUMN IF EXISTS owner_node;
DROP TABLE IF EXISTS cluster_nodes;
```

- [ ] **Step 2: 删 owner_node 代码引用**

删 `internal/node/orchestrator.go:74` 的 `o.mgr.ClaimAccount(...)` 调用块（redis 后端 owner 已由 Acquire 设置）；删 `internal/store/node.go` 的 `ClaimAccount`、`upsertNodeHeartbeatPG`、`deregisterNodePG`、`staleOwnedAccountsPG`、`listUnownedActiveAccountsPG`、`pgOwnership` 整体（pg 后端已退役）。`newOwnership` 的 `default`/`pg`/`shadow` 分支随之移除，仅留 redis。

- [ ] **Step 3: 迁移幂等性 + 全量测试**

Run: `make migrate_twice && go test ./... -short`
Expected: PASS（迁移可重复执行；short 模式不需容器）

- [ ] **Step 4: 提交**

```bash
git add migrations/0017_drop_owner_node.sql internal/store/node.go internal/store/ownership.go internal/store/ownership_pg.go internal/node/orchestrator.go
git commit -m "chore(store): drop owner_node column + cluster_nodes after redis ownership cutover"
```

---

## 验收（合并前）

- [ ] `make gate`（tidy + vet + test-race + labels + vuln）绿。
- [ ] `make chaos`（含 `TestTakeoverChaos` pg 版 `-count=5`）绿。
- [ ] `go test ./internal/store/ ./internal/node/ -count=5` 绿（含 redis race/fencing/chaos parity）。
- [ ] `backend=pg` 行为与改造前一致（现有测试全绿）。
- [ ] 未改 `cluster`/`dispatch`/`sendgate`/`billing` 逻辑（git diff 核对）。
- [ ] Task 9 默认**不执行**，待生产 cutover 后单独放行。

## Self-Review 备注

- 规模风险：`StaleOwned` 用 `KEYS owned:*`（仅节点数）安全；`Unowned` 已 pipeline 批量。生产若节点多，`KEYS` 可改 `SCAN`。
- send 路由（多节点）不在本计划：单机一个 registry，跨节点路由留国家分片阶段。
- fence-on-send 与 presence/typing 属反指纹 spec（`cluster`），单独计划；本计划只提供 `DeviceLockHandle.Healthy()` 的 Redis 实现供其调用。
