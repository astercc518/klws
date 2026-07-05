# M6 反封层 / 有界 Ghost Reaper + Boot 斜坡 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 夺回 whatsmeow 连接生命周期控制权，在硬上限下强制回收 ghost 连接（防 10 万常驻账号泄漏 FD/goroutine/代理 socket），外加启动 warm 速率斜坡削平重连风暴。

**Architecture:** 新增 `LivenessConn` capability 接口（沿用 `PresenceConn`/`SessionSender` 类型断言惯例）暴露 whatsmeow socket 存活；`NewWAConn` 门控关闭 auto-reconnect 并装生命周期事件处理器；`internal/node` 新增纯逻辑 Ghost Reaper（回调注入、假时钟可测）按 `ghost_ttl` dwell + `ghost_max` 并发上限回收；WorkingSet 加启动 warm 斜坡。全部配置门控、默认=今日行为。

**Tech Stack:** Go 1.26, whatsmeow, pgx/v5, go-redis, x/sync/errgroup, Prometheus。

## Global Constraints

- **零爆炸半径**：所有新能力配置门控，默认 `WADIST_GHOST_REAPER=off`、`WADIST_BOOT_RAMP_MS=0` → 与当前生产逐字一致（auto-reconnect 仍开、仅 receipt 事件处理器、无 reaper goroutine、无斜坡）。
- **正确性不变式**：`Session.Close` 幂等；回收经活 `Session` 的锁句柄释放 fence（与 `guardSession` 一致）；`make gate`（含 -race）全绿。
- **包分层**（注释强制）：`store` ← `cluster` ← `node` ← `cmd/wadist`；`control` 零引擎依赖（回调注入）。只有 `conn_whatsmeow.go` 可 import whatsmeow。
- **默认值**：`ghost_ttl=20s`、`ghost_max=128`、`ghost_tick=5s`、`boot_ramp=0`（关，建议 60000）。
- **永久信号语义**：`LoggedOut`/`StreamReplaced` → 回收 + PG `ban_status='logged_out'`（复用已有 enum，无新迁移），**不** re-warm；瞬态 → 回收 + re-warm。

---

### Task 1: LivenessConn 接口 + Session.Liveness + fakeConn 支持

**Files:**
- Modify: `internal/cluster/types.go`（加 `Liveness` 结构、`LivenessConn` 接口、`Session.Liveness()`）
- Modify: `internal/cluster/session_test.go`（`fakeConn` 加 liveness 字段 + 断言测试）

**Interfaces:**
- Produces:
  - `type Liveness struct { Alive bool; Permanent bool }`
  - `type LivenessConn interface { Liveness() Liveness }`
  - `func (s *Session) Liveness() (Liveness, bool)` — 底层 `conn` 实现 `LivenessConn` 时返回其状态且 ok=true；否则 `Liveness{}, false`（capability 缺失=无法探测）。

- [ ] **Step 1: Write the failing test**

在 `internal/cluster/session_test.go` 末尾追加。`fakeConn`（该文件已有，字段 `connectErr error; connected bool; events *[]string; name string`）需新增 `alive, permanent, hasLiveness bool` 字段并实现 `Liveness()`——但**仅当** `hasLiveness` 为真时该断言路径才应命中。为保持 `fakeConn` 同时能扮演「无 liveness capability」的 conn，改为**新增一个独立 fake** 而非改旧 `fakeConn`：

```go
// liveConn is a Conn that also implements LivenessConn, for Session.Liveness tests.
type liveConn struct {
	fakeConn
	lv Liveness
}

func (c *liveConn) Liveness() Liveness { return c.lv }

func TestSessionLiveness_CapabilityPresent(t *testing.T) {
	conn := &liveConn{lv: Liveness{Alive: false, Permanent: true}}
	s := NewSession("j1", conn, &fakeLock{})
	lv, ok := s.Liveness()
	if !ok {
		t.Fatal("ok=false; want true when conn implements LivenessConn")
	}
	if lv.Alive || !lv.Permanent {
		t.Fatalf("got %+v; want {Alive:false Permanent:true}", lv)
	}
}

func TestSessionLiveness_CapabilityAbsent(t *testing.T) {
	// plain fakeConn does NOT implement LivenessConn.
	s := NewSession("j2", &fakeConn{}, &fakeLock{})
	if _, ok := s.Liveness(); ok {
		t.Fatal("ok=true; want false when conn lacks LivenessConn capability")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cluster/ -run TestSessionLiveness -v`
Expected: FAIL（`Liveness` / `LivenessConn` / `Session.Liveness` 未定义）。

- [ ] **Step 3: Write minimal implementation**

在 `internal/cluster/types.go` 的 `Conn`/`PresenceConn` 附近新增：

```go
// Liveness is one connection's socket-liveness snapshot. Alive means the
// underlying transport is connected and logged-in right now; Permanent means a
// terminal WhatsApp signal (LoggedOut / StreamReplaced) has been observed, so the
// account must not be auto-re-warmed on this node.
type Liveness struct {
	Alive     bool
	Permanent bool
}

// LivenessConn is an optional capability on top of Conn: report socket liveness.
// waConn implements it; conns that do not (test fakes without it) are treated as
// assume-healthy at the call site (never reaped on an unprobeable conn).
type LivenessConn interface {
	Liveness() Liveness
}
```

在 `Session` 方法区（`Healthy` 附近）新增：

```go
// Liveness reports the connection's socket liveness when the underlying Conn
// implements LivenessConn. ok=false means the capability is absent — the caller
// should assume-healthy (never reap what it cannot probe).
func (s *Session) Liveness() (Liveness, bool) {
	if lc, ok := s.conn.(LivenessConn); ok {
		return lc.Liveness(), true
	}
	return Liveness{}, false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cluster/ -run TestSessionLiveness -v`
Expected: PASS（两例）。

- [ ] **Step 5: Run the package to ensure nothing else broke**

Run: `go test ./internal/cluster/`
Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add internal/cluster/types.go internal/cluster/session_test.go
git commit -m "feat(cluster): LivenessConn capability + Session.Liveness (M6 t1)"
```

---

### Task 2: NewWAConn 门控生命周期接管（关重连 + 事件处理器）

**Files:**
- Modify: `internal/cluster/conn_whatsmeow.go`（`waConn` 加 `permanent atomic.Bool` + `Liveness()`；`NewWAConn` 加 `manageLifecycle bool` 参 + 关重连 + 生命周期事件处理器）
- Modify: `cmd/wadist/main.go:246`（`NewWAConn` 调用加实参 `cfg.GhostReaper == "on"`）— 仅编译对齐，真正门控接线在 Task 6；此处先传 `false` 保持行为不变

**Interfaces:**
- Consumes: `Liveness`/`LivenessConn`（Task 1）。
- Produces: `func NewWAConn(device *waproto.Device, logger waLog.Logger, proxy *store.ProxyBinding, onReceipt ReceiptFunc, manageLifecycle bool) *waConn`；`func (c *waConn) Liveness() Liveness`。

**说明**：whatsmeow `Client` 暴露 `IsConnected() bool`、`IsLoggedIn() bool`、公有字段 `EnableAutoReconnect bool`（`NewClient` 默认 true）；事件类型 `events.LoggedOut`、`events.StreamReplaced`、`events.Disconnected` 在 `go.mau.fi/whatsmeow/types/events`（`events` 已 import）。实现者请先 `grep -rn "EnableAutoReconnect\|func (cli \*Client) IsLoggedIn\|StreamReplaced" $(go env GOMODCACHE)/go.mau.fi/whatsmeow*/` 核对确切符号后再写。

- [ ] **Step 1: Add the atomic field and Liveness (compile-first)**

`conn_whatsmeow.go` 顶部 import 加 `"sync/atomic"`。`waConn` 结构改为：

```go
type waConn struct {
	client    *whatsmeow.Client
	proxy     *store.ProxyBinding
	permanent atomic.Bool // set on LoggedOut / StreamReplaced
}
```

在 `Disconnect` 方法后新增 `LivenessConn` 实现：

```go
var _ LivenessConn = (*waConn)(nil)

// Liveness reports the whatsmeow socket state: Alive iff connected AND logged-in
// right now; Permanent iff a terminal LoggedOut/StreamReplaced was observed.
func (c *waConn) Liveness() Liveness {
	return Liveness{
		Alive:     c.client.IsConnected() && c.client.IsLoggedIn(),
		Permanent: c.permanent.Load(),
	}
}
```

- [ ] **Step 2: Rework NewWAConn to take manageLifecycle and install handlers**

把 `NewWAConn` 改为先建 `c`，再挂 receipt 处理器（不变），再按 `manageLifecycle` 关重连 + 挂生命周期处理器：

```go
func NewWAConn(device *waproto.Device, logger waLog.Logger, proxy *store.ProxyBinding, onReceipt ReceiptFunc, manageLifecycle bool) *waConn {
	client := whatsmeow.NewClient(device, logger)
	c := &waConn{client: client, proxy: proxy}
	if onReceipt != nil {
		client.AddEventHandler(func(evt any) {
			r, ok := evt.(*events.Receipt)
			if !ok || len(r.MessageIDs) == 0 {
				return
			}
			var kind string
			switch r.Type {
			case "":
				kind = "delivered"
			case "read", "read-self":
				kind = "read"
			default:
				return
			}
			ids := make([]string, len(r.MessageIDs))
			for i, m := range r.MessageIDs {
				ids[i] = string(m)
			}
			onReceipt(ids, kind, r.Timestamp)
		})
	}
	if manageLifecycle {
		// Take back lifecycle control: stop whatsmeow's invisible auto-reconnect
		// so a half-dead socket surfaces as a ghost instead of self-healing while
		// leaking FDs/goroutines/proxy sockets. Terminal signals set Permanent so
		// the reaper marks the account logged-out rather than re-warming it.
		client.EnableAutoReconnect = false
		client.AddEventHandler(func(evt any) {
			switch evt.(type) {
			case *events.LoggedOut, *events.StreamReplaced:
				c.permanent.Store(true)
			}
		})
	}
	return c
}
```

（`Connect`/`Disconnect`/`SetPresence`/`SendTyping`/`Send` 均不变。）

- [ ] **Step 3: Fix the one call site to keep behavior unchanged**

`cmd/wadist/main.go:246` 改为（Task 6 再改成 `cfg.GhostReaper == "on"`）：

```go
conn := cluster.NewWAConn(device, logger, proxyBinding, onReceipt, false)
```

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: 成功（无未用 import、签名对齐）。

- [ ] **Step 5: Run the cluster package tests**

Run: `go test ./internal/cluster/`
Expected: PASS（`conn_whatsmeow.go` 本身无单测——需真设备，同文件既有说明；本 task 靠 `go build` + `var _ LivenessConn` 断言 + 下游 Task 4/6 覆盖）。

- [ ] **Step 6: Commit**

```bash
git add internal/cluster/conn_whatsmeow.go cmd/wadist/main.go
git commit -m "feat(cluster): NewWAConn gated lifecycle takeover (disable auto-reconnect + terminal-signal handler) (M6 t2)"
```

---

### Task 3: Manager.MarkAccountLoggedOut + ListActiveAccounts 排除集成测

**Files:**
- Modify: `internal/store/node.go`（新增 `MarkAccountLoggedOut`）
- Modify: `internal/store/node_test.go`（集成测：置 logged_out 后 `ListActiveAccounts` 排除）

**Interfaces:**
- Produces: `func (m *Manager) MarkAccountLoggedOut(ctx context.Context, jid string) error`。

**说明**：复用已有 `ban_status_t` enum 的 `'logged_out'`（`migrations/0001`）；`ListActiveAccounts` 已按 `ban_status='active'` 过滤，故无新迁移。`m.bizPool` 是业务池字段（见 `ClaimAccount`）。测试沿用 `node_test.go` 既有 `TestListActiveAccounts` 的 testcontainer + 播种模式（实现者读该测试拿到 seed helper 名与建池方式）。

- [ ] **Step 1: Write the failing integration test**

在 `internal/store/node_test.go` 追加（seed 复用 `TestListActiveAccounts` 用的同一 helper；若该测试是直接 `INSERT`，照抄其 INSERT 语句）：

```go
func TestMarkAccountLoggedOut_ExcludedFromActive(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	mgr := newTestManager(t) // 与 TestListActiveAccounts 相同的构造 helper
	ctx := context.Background()

	// 两个 active 账号（seed 方式对齐 TestListActiveAccounts）。
	seedActiveAccount(t, mgr, "keep@s.whatsapp.net")
	seedActiveAccount(t, mgr, "gone@s.whatsapp.net")

	before, err := mgr.ListActiveAccounts(ctx)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("active before=%d; want 2", len(before))
	}

	if err := mgr.MarkAccountLoggedOut(ctx, "gone@s.whatsapp.net"); err != nil {
		t.Fatalf("MarkAccountLoggedOut: %v", err)
	}

	after, err := mgr.ListActiveAccounts(ctx)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != 1 || after[0] != "keep@s.whatsapp.net" {
		t.Fatalf("active after=%v; want [keep@s.whatsapp.net] (logged_out excluded)", after)
	}
}
```

> 若 `TestListActiveAccounts` 未用名为 `newTestManager`/`seedActiveAccount` 的 helper，实现者用该测试实际的构造/播种代码替换这两处，保持等价语义（两 active → 标一个 logged_out → 断言只剩一个）。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestMarkAccountLoggedOut_ExcludedFromActive -v`
Expected: FAIL（`MarkAccountLoggedOut` 未定义）。

- [ ] **Step 3: Write minimal implementation**

在 `internal/store/node.go` 的 `ClaimAccount` 附近新增：

```go
// MarkAccountLoggedOut sets ban_status='logged_out' so the account is excluded
// from ListActiveAccounts (hence never re-warmed by the WorkingSet/Reconciler).
// Used by the Ghost Reaper on a terminal LoggedOut/StreamReplaced signal.
func (m *Manager) MarkAccountLoggedOut(ctx context.Context, jid string) error {
	_, err := m.bizPool.Exec(ctx,
		`UPDATE account_devices SET ban_status='logged_out', ban_checked_at=now() WHERE account_jid=$1`,
		jid)
	if err != nil {
		return fmt.Errorf("mark account logged out %s: %w", jid, err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestMarkAccountLoggedOut_ExcludedFromActive -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/store/node.go internal/store/node_test.go
git commit -m "feat(store): MarkAccountLoggedOut excludes account from active set (M6 t3)"
```

---

### Task 4: Ghost Reaper 纯逻辑 + 分类/上限/dwell 单测

**Files:**
- Create: `internal/node/reaper.go`
- Create: `internal/node/reaper_test.go`

**Interfaces:**
- Consumes: `cluster.Session.Liveness()`（Task 1，仅在 `GhostCandidatesFrom` 里用）。
- Produces:
  - `type GhostCandidate struct { JID string; Alive, Permanent bool }`
  - `func GhostCandidatesFrom(sessions []*cluster.Session) []GhostCandidate`
  - `type GhostReaperMetrics interface { IncGhostReaped(kind string); SetGhostInFlight(n int) }`
  - `type GhostReaperDeps struct { Now func() time.Time; Snapshot func() []GhostCandidate; Reap func(context.Context, string) error; RequestWarm func(context.Context, string) error; MarkLoggedOut func(context.Context, string) error; Metrics GhostReaperMetrics }`
  - `type GhostReaperConfig struct { TTL time.Duration; Max int }`
  - `func NewGhostReaper(cfg GhostReaperConfig, deps GhostReaperDeps) *GhostReaper`
  - `func (r *GhostReaper) Tick(ctx context.Context) (int, error)`
  - `func (r *GhostReaper) Run(ctx context.Context, interval time.Duration) error`

- [ ] **Step 1: Write the reaper implementation**

`internal/node/reaper.go`：

```go
package node

import (
	"context"
	"log"
	"time"

	"github.com/acme/wadist/internal/cluster"
	"golang.org/x/sync/errgroup"
)

// GhostCandidate is one session's liveness view for a reaper tick.
type GhostCandidate struct {
	JID       string
	Alive     bool
	Permanent bool
}

// GhostCandidatesFrom maps live sessions to candidates via the LivenessConn
// capability. A session whose conn lacks the capability is reported Alive=true
// (assume-healthy: never reap what we cannot probe).
func GhostCandidatesFrom(sessions []*cluster.Session) []GhostCandidate {
	out := make([]GhostCandidate, 0, len(sessions))
	for _, s := range sessions {
		lv, ok := s.Liveness()
		if !ok {
			out = append(out, GhostCandidate{JID: s.JID(), Alive: true})
			continue
		}
		out = append(out, GhostCandidate{JID: s.JID(), Alive: lv.Alive, Permanent: lv.Permanent})
	}
	return out
}

// GhostReaperMetrics is the optional metrics sink (nil-safe at call site).
type GhostReaperMetrics interface {
	IncGhostReaped(kind string) // "transient" | "permanent"
	SetGhostInFlight(n int)
}

// GhostReaperDeps injects all side effects so the reaper is pure/testable.
type GhostReaperDeps struct {
	Now           func() time.Time
	Snapshot      func() []GhostCandidate
	Reap          func(ctx context.Context, jid string) error // reg.Remove + sess.Close
	RequestWarm   func(ctx context.Context, jid string) error  // transient → re-warm
	MarkLoggedOut func(ctx context.Context, jid string) error  // permanent → PG logged_out
	Metrics       GhostReaperMetrics                           // may be nil
}

type GhostReaperConfig struct {
	TTL time.Duration // dwell before a silent not-alive session is a ghost
	Max int           // max concurrent reaps per tick
}

// GhostReaper force-reclaims ghost connections under hard caps.
type GhostReaper struct {
	cfg   GhostReaperConfig
	deps  GhostReaperDeps
	dwell map[string]time.Time // jid → first observed not-alive time
}

func NewGhostReaper(cfg GhostReaperConfig, deps GhostReaperDeps) *GhostReaper {
	if cfg.TTL <= 0 {
		cfg.TTL = 20 * time.Second
	}
	if cfg.Max <= 0 {
		cfg.Max = 128
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &GhostReaper{cfg: cfg, deps: deps, dwell: make(map[string]time.Time)}
}

type ghost struct {
	jid       string
	permanent bool
}

// Tick classifies the current snapshot, then reaps up to Max ghosts concurrently.
// Returns the number reaped this tick.
func (r *GhostReaper) Tick(ctx context.Context) (int, error) {
	snap := r.deps.Snapshot()
	now := r.deps.Now()

	present := make(map[string]struct{}, len(snap))
	var ghosts []ghost
	for _, c := range snap {
		present[c.JID] = struct{}{}
		switch {
		case c.Permanent:
			delete(r.dwell, c.JID)
			ghosts = append(ghosts, ghost{jid: c.JID, permanent: true})
		case c.Alive:
			delete(r.dwell, c.JID)
		default: // not alive, not permanent → dwell
			first, ok := r.dwell[c.JID]
			if !ok {
				r.dwell[c.JID] = now
				first = now
			}
			if now.Sub(first) >= r.cfg.TTL {
				ghosts = append(ghosts, ghost{jid: c.JID, permanent: false})
			}
		}
	}
	// Drop dwell entries for sessions no longer present.
	for jid := range r.dwell {
		if _, ok := present[jid]; !ok {
			delete(r.dwell, jid)
		}
	}

	// Cap this tick's reaps; overflow waits for the next tick.
	if len(ghosts) > r.cfg.Max {
		ghosts = ghosts[:r.cfg.Max]
	}
	if len(ghosts) == 0 {
		if r.deps.Metrics != nil {
			r.deps.Metrics.SetGhostInFlight(0)
		}
		return 0, nil
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(r.cfg.Max)
	for _, gh := range ghosts {
		gh := gh
		g.Go(func() error {
			if err := r.deps.Reap(gctx, gh.jid); err != nil {
				log.Printf("node: ghost reap %s: %v", gh.jid, err)
				return nil // one failure never aborts the batch
			}
			if gh.permanent {
				if err := r.deps.MarkLoggedOut(gctx, gh.jid); err != nil {
					log.Printf("node: ghost mark-logged-out %s: %v", gh.jid, err)
				}
				if r.deps.Metrics != nil {
					r.deps.Metrics.IncGhostReaped("permanent")
				}
			} else {
				if err := r.deps.RequestWarm(gctx, gh.jid); err != nil {
					log.Printf("node: ghost re-warm %s: %v", gh.jid, err)
				}
				if r.deps.Metrics != nil {
					r.deps.Metrics.IncGhostReaped("transient")
				}
			}
			return nil
		})
	}
	_ = g.Wait()

	// Reaped sessions leave the registry; drop their dwell entries.
	for _, gh := range ghosts {
		delete(r.dwell, gh.jid)
	}
	if r.deps.Metrics != nil {
		r.deps.Metrics.SetGhostInFlight(len(ghosts))
	}
	return len(ghosts), nil
}

// Run ticks every interval; interval<=0 disables (blocks until ctx done).
func (r *GhostReaper) Run(ctx context.Context, interval time.Duration) error {
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
			if n, err := r.Tick(ctx); err != nil {
				log.Printf("node: ghost reaper tick error: %v", err)
			} else if n > 0 {
				log.Printf("node: ghost reaper reclaimed %d ghost(s)", n)
			}
		}
	}
}
```

- [ ] **Step 2: Write the tests**

`internal/node/reaper_test.go`：

```go
package node

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recording deps for reaper tests.
type reapRec struct {
	mu        sync.Mutex
	reaped    []string
	warmed    []string
	loggedOut []string
}

func (r *reapRec) reap(_ context.Context, jid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reaped = append(r.reaped, jid)
	return nil
}
func (r *reapRec) warm(_ context.Context, jid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warmed = append(r.warmed, jid)
	return nil
}
func (r *reapRec) markOut(_ context.Context, jid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loggedOut = append(r.loggedOut, jid)
	return nil
}

func newReaper(t *testing.T, cfg GhostReaperConfig, snap func() []GhostCandidate, now func() time.Time) (*GhostReaper, *reapRec) {
	t.Helper()
	rec := &reapRec{}
	r := NewGhostReaper(cfg, GhostReaperDeps{
		Now: now, Snapshot: snap,
		Reap: rec.reap, RequestWarm: rec.warm, MarkLoggedOut: rec.markOut,
	})
	return r, rec
}

func TestReaper_TransientDwellBelowTTL_NoReap(t *testing.T) {
	base := time.Unix(1000, 0)
	nowT := base
	snap := func() []GhostCandidate { return []GhostCandidate{{JID: "a", Alive: false}} }
	r, rec := newReaper(t, GhostReaperConfig{TTL: 20 * time.Second, Max: 8},
		snap, func() time.Time { return nowT })

	n, _ := r.Tick(context.Background()) // records dwell at base
	if n != 0 {
		t.Fatalf("tick1 reaped %d; want 0 (dwell just started)", n)
	}
	nowT = base.Add(10 * time.Second) // < TTL
	n, _ = r.Tick(context.Background())
	if n != 0 || len(rec.reaped) != 0 {
		t.Fatalf("reaped %d before TTL; want 0", n)
	}
}

func TestReaper_TransientAtTTL_ReapAndRewarm(t *testing.T) {
	base := time.Unix(1000, 0)
	nowT := base
	snap := func() []GhostCandidate { return []GhostCandidate{{JID: "a", Alive: false}} }
	r, rec := newReaper(t, GhostReaperConfig{TTL: 20 * time.Second, Max: 8},
		snap, func() time.Time { return nowT })

	r.Tick(context.Background())        // dwell starts at base
	nowT = base.Add(20 * time.Second)   // == TTL
	n, _ := r.Tick(context.Background())
	if n != 1 {
		t.Fatalf("reaped %d; want 1 at TTL", n)
	}
	if len(rec.reaped) != 1 || rec.reaped[0] != "a" {
		t.Fatalf("reaped=%v; want [a]", rec.reaped)
	}
	if len(rec.warmed) != 1 || rec.warmed[0] != "a" {
		t.Fatalf("warmed=%v; want [a] (transient re-warm)", rec.warmed)
	}
	if len(rec.loggedOut) != 0 {
		t.Fatalf("loggedOut=%v; want none for transient", rec.loggedOut)
	}
}

func TestReaper_Permanent_ImmediateReapMarkNoRewarm(t *testing.T) {
	snap := func() []GhostCandidate { return []GhostCandidate{{JID: "p", Alive: false, Permanent: true}} }
	r, rec := newReaper(t, GhostReaperConfig{TTL: 20 * time.Second, Max: 8},
		snap, func() time.Time { return time.Unix(1000, 0) })

	n, _ := r.Tick(context.Background()) // no dwell wait for permanent
	if n != 1 {
		t.Fatalf("reaped %d; want 1 (permanent immediate)", n)
	}
	if len(rec.loggedOut) != 1 || rec.loggedOut[0] != "p" {
		t.Fatalf("loggedOut=%v; want [p]", rec.loggedOut)
	}
	if len(rec.warmed) != 0 {
		t.Fatalf("warmed=%v; want none for permanent (no re-warm)", rec.warmed)
	}
}

func TestReaper_AliveClearsDwell(t *testing.T) {
	base := time.Unix(1000, 0)
	nowT := base
	alive := false
	snap := func() []GhostCandidate { return []GhostCandidate{{JID: "a", Alive: alive}} }
	r, rec := newReaper(t, GhostReaperConfig{TTL: 20 * time.Second, Max: 8},
		snap, func() time.Time { return nowT })

	r.Tick(context.Background()) // dwell starts (not alive)
	alive = true                 // recovered before TTL
	nowT = base.Add(10 * time.Second)
	r.Tick(context.Background()) // should clear dwell
	alive = false                // dies again
	nowT = base.Add(15 * time.Second)
	r.Tick(context.Background()) // dwell restarts here, not from base
	nowT = base.Add(30 * time.Second) // 15s since restart < TTL
	n, _ := r.Tick(context.Background())
	if n != 0 || len(rec.reaped) != 0 {
		t.Fatalf("reaped %d; want 0 (dwell was reset by the alive tick)", n)
	}
}

func TestReaper_GhostMaxOverflowsToNextTick(t *testing.T) {
	// 5 permanent ghosts, Max=2 → 2 per tick; snapshot shrinks as reaped.
	live := map[string]bool{"a": true, "b": true, "c": true, "d": true, "e": true}
	snap := func() []GhostCandidate {
		out := []GhostCandidate{}
		for _, j := range []string{"a", "b", "c", "d", "e"} {
			if live[j] {
				out = append(out, GhostCandidate{JID: j, Alive: false, Permanent: true})
			}
		}
		return out
	}
	rec := &reapRec{}
	r := NewGhostReaper(GhostReaperConfig{TTL: time.Second, Max: 2}, GhostReaperDeps{
		Now:      func() time.Time { return time.Unix(1000, 0) },
		Snapshot: snap,
		Reap: func(ctx context.Context, jid string) error {
			live[jid] = false // reaped session leaves the registry
			return rec.reap(ctx, jid)
		},
		RequestWarm:   rec.warm,
		MarkLoggedOut: rec.markOut,
	})

	total := 0
	for i := 0; i < 3; i++ { // ceil(5/2)=3 ticks
		n, _ := r.Tick(context.Background())
		if n > 2 {
			t.Fatalf("tick %d reaped %d; want <=Max(2)", i, n)
		}
		total += n
	}
	if total != 5 {
		t.Fatalf("total reaped=%d; want 5 across ticks", total)
	}
}

func TestReaper_DropsDwellForGoneSessions(t *testing.T) {
	present := true
	snap := func() []GhostCandidate {
		if present {
			return []GhostCandidate{{JID: "a", Alive: false}}
		}
		return nil
	}
	r, _ := newReaper(t, GhostReaperConfig{TTL: 20 * time.Second, Max: 8},
		snap, func() time.Time { return time.Unix(1000, 0) })

	r.Tick(context.Background()) // dwell["a"] set
	if len(r.dwell) != 1 {
		t.Fatalf("dwell size=%d; want 1", len(r.dwell))
	}
	present = false
	r.Tick(context.Background()) // "a" gone → dwell dropped
	if len(r.dwell) != 0 {
		t.Fatalf("dwell size=%d; want 0 after session gone", len(r.dwell))
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/node/ -run TestReaper -v`
Expected: PASS（6 例）。

- [ ] **Step 4: Run with race**

Run: `go test -race ./internal/node/ -run TestReaper`
Expected: PASS（并发 reap 无竞争；dwell 仅主 goroutine 读写）。

- [ ] **Step 5: Commit**

```bash
git add internal/node/reaper.go internal/node/reaper_test.go
git commit -m "feat(node): bounded Ghost Reaper (dwell TTL + ghost_max cap, transient re-warm / permanent mark) (M6 t4)"
```

---

### Task 5: WorkingSet 启动 warm 速率斜坡

**Files:**
- Modify: `internal/control/workingset.go`（`Config` 加 `BootRamp`；`WorkingSet` 加 `bootStartMs`；`Tick` 用 `effTarget`）
- Modify: `internal/control/workingset_test.go`（斜坡单测；若文件不存在则 Create）

**Interfaces:**
- Consumes: 无（纯内部）。
- Produces: `control.Config.BootRamp time.Duration`（0=关，语义：`[bootStart, bootStart+BootRamp]` 内 active-warm 目标线性 `ceil(Target*elapsed/BootRamp)`，之后满 `Target`）。

**说明**：仅影响 ACTIVE-warm 阶段（第 2 步的两处 `residentCount >= Target` 比较改为 `>= effTarget`）；PASSIVE-warm 与 EVICT 不变。`bootStartMs` 于首个 `Tick` 的 `nowMs` 惰性初始化。

- [ ] **Step 1: Write the failing test**

`internal/control/workingset_test.go`（新增；`WorkingSet` 与 `Scheduler` 需 Redis，斜坡逻辑本身是纯算术——把 `effTarget` 抽成可单测的纯方法以避免起容器）：

先在实现里抽出纯函数（Step 3 会写），测试针对它：

```go
package control

import (
	"testing"
	"time"
)

func TestEffTarget_RampDisabled(t *testing.T) {
	w := &WorkingSet{cfg: Config{Target: 1500}} // BootRamp==0
	if got := w.effTarget(0); got != 1500 {
		t.Fatalf("ramp off: effTarget=%d; want 1500", got)
	}
}

func TestEffTarget_LinearRampAndSaturation(t *testing.T) {
	w := &WorkingSet{cfg: Config{Target: 1000, BootRamp: 60 * time.Second}}
	// first tick sets bootStartMs = nowMs
	base := int64(10_000)
	if got := w.effTarget(base); got != 0 { // elapsed=0 → ceil(0)=0
		t.Fatalf("t=0: effTarget=%d; want 0", got)
	}
	if got := w.effTarget(base + 30_000); got != 500 { // 50% → 500
		t.Fatalf("t=30s: effTarget=%d; want 500", got)
	}
	if got := w.effTarget(base + 6_000); got != 100 { // 10% → 100
		t.Fatalf("t=6s: effTarget=%d; want 100", got)
	}
	if got := w.effTarget(base + 60_000); got != 1000 { // ==ramp → full
		t.Fatalf("t=60s: effTarget=%d; want 1000", got)
	}
	if got := w.effTarget(base + 120_000); got != 1000 { // past ramp → full
		t.Fatalf("t>ramp: effTarget=%d; want 1000", got)
	}
}

func TestEffTarget_CeilGivesAtLeastOneOncePastZero(t *testing.T) {
	w := &WorkingSet{cfg: Config{Target: 10, BootRamp: 100 * time.Second}}
	base := int64(0)
	w.effTarget(base) // init bootStartMs
	// elapsed=1ms of 100_000ms with Target=10 → ceil(10*1/100000)=1
	if got := w.effTarget(base + 1); got != 1 {
		t.Fatalf("tiny elapsed: effTarget=%d; want 1 (ceil)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/control/ -run TestEffTarget -v`
Expected: FAIL（`effTarget` / `Config.BootRamp` / `bootStartMs` 未定义）。

- [ ] **Step 3: Write minimal implementation**

`workingset.go` 改动：

`Config` 加字段：

```go
type Config struct {
	Target          int
	WarmReqBatch    int
	KeepWarmHorizon time.Duration
	Linger          time.Duration
	BootRamp        time.Duration // WADIST_BOOT_RAMP_MS; 0 disables the startup ramp
}
```

`WorkingSet` 加状态字段：

```go
type WorkingSet struct {
	rdb         *goredis.Client
	sched       *Scheduler
	ccs         []string
	cfg         Config
	deps        Deps
	bootStartMs int64 // first Tick's nowMs; 0 until set
}
```

新增纯方法：

```go
// effTarget is the active-warm resident ceiling for this tick. With BootRamp>0,
// it ramps linearly from 0 to Target over [bootStart, bootStart+BootRamp] to
// spread the post-restart reconnect burst; after the window (and always when
// BootRamp==0) it is the full Target. bootStartMs is latched on first call.
func (w *WorkingSet) effTarget(nowMs int64) int {
	if w.cfg.BootRamp <= 0 {
		return w.cfg.Target
	}
	if w.bootStartMs == 0 {
		w.bootStartMs = nowMs
	}
	elapsed := nowMs - w.bootStartMs
	rampMs := w.cfg.BootRamp.Milliseconds()
	if elapsed >= rampMs {
		return w.cfg.Target
	}
	if elapsed <= 0 {
		return 0
	}
	// ceil(Target * elapsed / rampMs)
	return int((int64(w.cfg.Target)*elapsed + rampMs - 1) / rampMs)
}
```

`Tick` 第 2 步（ACTIVE warm）把两处 `w.cfg.Target` 改为 `eff`：

```go
	// 2. ACTIVE warm: for each cc, fill resident set up to the (ramped) target
	eff := w.effTarget(nowMs)
	for _, cc := range w.ccs {
		if w.residentCount(ctx) >= eff {
			break
		}
		due, err := w.sched.PopDue(ctx, cc, nowMs, w.cfg.Target)
		if err != nil {
			return err
		}
		for _, jid := range due {
			if w.residentCount(ctx) >= eff {
				break
			}
			w.warm(ctx, jid, cc)
		}
	}
```

（`PopDue` 的第 4 参保持 `w.cfg.Target`——它是单 cc 弹出上限，斜坡只约束 resident 天花板。PASSIVE warm 第 1 步与 EVICT 第 3 步不变。）

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/control/ -run TestEffTarget -v`
Expected: PASS（3 例）。

- [ ] **Step 5: Run the control package**

Run: `go test ./internal/control/`
Expected: PASS（既有 WorkingSet/Scheduler 测试不受影响——`BootRamp` 默认 0 → `effTarget==Target`）。

- [ ] **Step 6: Commit**

```bash
git add internal/control/workingset.go internal/control/workingset_test.go
git commit -m "feat(control): WorkingSet boot warm-rate ramp (spread restart reconnect burst) (M6 t5)"
```

---

### Task 6: 配置字段 + main.go 门控接线 + 指标

**Files:**
- Modify: `internal/config/config.go`（加 5 个字段 + Load 行）
- Modify: `internal/metrics/metrics.go`（`ghostReaped` CounterVec + `ghostInFlight` Gauge + `IncGhostReaped`/`SetGhostInFlight`）
- Modify: `internal/metrics/metrics_test.go`（指标断言）
- Modify: `cmd/wadist/main.go`（factory 传 `cfg.GhostReaper=="on"`；WorkingSet Config 传 `BootRamp`；门控构建 + `sup.Go(reaper.Run)`）

**Interfaces:**
- Consumes: `node.NewGhostReaper`/`GhostReaperDeps`/`GhostCandidatesFrom`（Task 4）、`Manager.MarkAccountLoggedOut`（Task 3）、`control.Config.BootRamp`（Task 5）、`cluster.NewWAConn(...manageLifecycle)`（Task 2）。
- Produces: `Metrics.IncGhostReaped(kind string)`、`Metrics.SetGhostInFlight(n int)`；config 字段 `GhostReaper string`、`GhostTTL/GhostTick time.Duration`、`GhostMax int`、`BootRamp time.Duration`。

- [ ] **Step 1: Config fields + tests-by-inspection**

`config.go` 结构体加（Segment 段后）：

```go
	// Ghost Reaper (M6): bounded reclamation of dead/ghost whatsmeow connections.
	// Master gate off → auto-reconnect stays on, no lifecycle handlers, no reaper
	// loop (today's behavior). ttl≤30s per design.
	GhostReaper string        // WADIST_GHOST_REAPER default "off"
	GhostTTL    time.Duration // WADIST_GHOST_TTL_MS default 20000
	GhostMax    int           // WADIST_GHOST_MAX default 128
	GhostTick   time.Duration // WADIST_GHOST_TICK_MS default 5000
	// BootRamp spreads the post-restart warm burst; 0 disables (default).
	BootRamp time.Duration // WADIST_BOOT_RAMP_MS default 0
```

`Load()` 加（Segment 段后）：

```go
	cfg.GhostReaper = getenv("WADIST_GHOST_REAPER", "off")
	cfg.GhostTTL = msEnv("WADIST_GHOST_TTL_MS", 20000)
	cfg.GhostMax = intEnv("WADIST_GHOST_MAX", 128)
	cfg.GhostTick = msEnv("WADIST_GHOST_TICK_MS", 5000)
	cfg.BootRamp = msEnvZero("WADIST_BOOT_RAMP_MS")
```

- [ ] **Step 2: Write the failing metrics test**

`internal/metrics/metrics_test.go` 追加（沿用该文件既有构造/收集 helper）：

```go
func TestIncGhostReaped(t *testing.T) {
	m := New(prometheus.NewRegistry()) // 用该文件既有的构造方式
	m.IncGhostReaped("transient")
	m.IncGhostReaped("transient")
	m.IncGhostReaped("permanent")
	if got := testutil.ToFloat64(m.ghostReaped.WithLabelValues("transient")); got != 2 {
		t.Fatalf("transient=%v; want 2", got)
	}
	if got := testutil.ToFloat64(m.ghostReaped.WithLabelValues("permanent")); got != 1 {
		t.Fatalf("permanent=%v; want 1", got)
	}
}

func TestSetGhostInFlight(t *testing.T) {
	m := New(prometheus.NewRegistry())
	m.SetGhostInFlight(7)
	if got := testutil.ToFloat64(m.ghostInFlight); got != 7 {
		t.Fatalf("in-flight=%v; want 7", got)
	}
	m.SetGhostInFlight(0)
	if got := testutil.ToFloat64(m.ghostInFlight); got != 0 {
		t.Fatalf("in-flight=%v; want 0", got)
	}
}
```

> 实现者：`New` 构造签名 / `testutil` import 以该文件既有测试为准（如用 `newTestMetrics(t)` 则照用）。

- [ ] **Step 3: Run metrics test to verify it fails**

Run: `go test ./internal/metrics/ -run 'TestIncGhostReaped|TestSetGhostInFlight' -v`
Expected: FAIL（字段/方法未定义）。

- [ ] **Step 4: Implement metrics**

`metrics.go` 结构体加字段（`scheduleReconciled` 附近）：

```go
	ghostReaped   *prometheus.CounterVec // label: kind (transient|permanent)
	ghostInFlight prometheus.Gauge
```

构造区加（`m.scheduleReconciled = ...` 后）：

```go
	m.ghostReaped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_ghost_reaped_total",
		Help: "Ghost connections reclaimed by the reaper, by kind.",
	}, []string{"kind"})
	m.ghostInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "wadist_ghost_in_flight",
		Help: "Ghost connections reaped in the last reaper tick.",
	})
```

在该文件注册区（其它 `reg.MustRegister(...)` 处）注册 `m.ghostReaped, m.ghostInFlight`。方法区加：

```go
func (m *Metrics) IncGhostReaped(kind string) { m.ghostReaped.WithLabelValues(kind).Inc() }
func (m *Metrics) SetGhostInFlight(n int)      { m.ghostInFlight.Set(float64(n)) }
```

- [ ] **Step 5: Run metrics test to verify it passes**

Run: `go test ./internal/metrics/`
Expected: PASS。

- [ ] **Step 6: Wire factory + WorkingSet BootRamp in main.go**

`main.go:246` 改为传门控：

```go
			conn := cluster.NewWAConn(device, logger, proxyBinding, onReceipt, cfg.GhostReaper == "on")
```

`main.go:306` 的 `control.Config{...}` 加 `BootRamp: cfg.BootRamp,`（其余字段不动）。

- [ ] **Step 7: Wire the reaper loop (gated) in main.go**

在 WorkingSet 的 `sup.Go(... ws.Run ...)`（约 `main.go:346`）之后、Janitor 之前插入门控块：

```go
	// Ghost Reaper (M6): bounded reclamation of dead whatsmeow sockets. Only when
	// WADIST_GHOST_REAPER=on — default off keeps auto-reconnect on and runs no
	// reaper (zero blast radius). Transient ghosts are re-warmed via warm:req
	// (empty cc reuses the sticky proxy binding); terminal LoggedOut/StreamReplaced
	// ghosts are marked logged_out (excluded from the active set, not re-warmed).
	if cfg.GhostReaper == "on" {
		reaper := node.NewGhostReaper(
			node.GhostReaperConfig{TTL: cfg.GhostTTL, Max: cfg.GhostMax},
			node.GhostReaperDeps{
				Now:      time.Now,
				Snapshot: func() []node.GhostCandidate { return node.GhostCandidatesFrom(reg.Snapshot()) },
				Reap: func(ctx context.Context, jid string) error {
					if s, ok := reg.Remove(jid); ok {
						s.Close(ctx)
					}
					return nil
				},
				RequestWarm:   func(ctx context.Context, jid string) error { return rdb.RPush(ctx, "warm:req", jid+"|").Err() },
				MarkLoggedOut: mgr.MarkAccountLoggedOut,
				Metrics:       m,
			},
		)
		sup.Go(func(lctx context.Context) error { return reaper.Run(lctx, cfg.GhostTick) })
		log.Printf("ghost reaper on (ttl=%s max=%d tick=%s)", cfg.GhostTTL, cfg.GhostMax, cfg.GhostTick)
	}
	if cfg.BootRamp > 0 {
		log.Printf("boot warm ramp on (%s)", cfg.BootRamp)
	}
```

> 实现者核对：`m` 是 `*metrics.Metrics`（满足 `node.GhostReaperMetrics`）；`rdb` 是 `*goredis.Client`；`node`、`context`、`log`、`time` 均已 import。若 `*metrics.Metrics` 因方法集不匹配无法直接作为 `node.GhostReaperMetrics` 传入，检查 `IncGhostReaped`/`SetGhostInFlight` 签名与接口逐字一致。

- [ ] **Step 8: Build + gate**

Run: `go build ./...`
Expected: 成功。

Run: `go test ./internal/config/ ./internal/metrics/ ./internal/node/ ./internal/control/ ./internal/cluster/`
Expected: PASS。

- [ ] **Step 9: Commit**

```bash
git add internal/config/config.go internal/metrics/metrics.go internal/metrics/metrics_test.go cmd/wadist/main.go
git commit -m "feat(wadist): wire Ghost Reaper + boot ramp (gated WADIST_GHOST_REAPER/WADIST_BOOT_RAMP_MS, default off) (M6 t6)"
```

---

## Self-Review 记录

- **Spec 覆盖**：Liveness 缝(t1)/生命周期接管(t2)/永久标记(t3)/Reaper 逻辑(t4)/boot 斜坡(t5)/配置+接线+指标(t6) 对齐 spec §4.1–4.5、§5、§8 六 task 表。
- **类型一致**：`Liveness{Alive,Permanent}`、`GhostCandidate{JID,Alive,Permanent}`、`GhostReaperDeps` 字段签名在 t1/t2/t4/t6 间逐字一致；`NewWAConn` 新参 `manageLifecycle bool` 在 t2 定义、t2/t6 两处调用同步。
- **零爆炸半径**：t2 门控 off→auto-reconnect 不动、仅 receipt 处理器；t5 `BootRamp==0→effTarget==Target`；t6 `GhostReaper!="on"` 不建 reaper。默认全 off/0。
- **无占位符**：每 code step 均含完整代码与期望输出；集成测 helper 名以现有测试为准的两处已显式标注「照现有代码替换」。
