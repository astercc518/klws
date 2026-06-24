# M8 优雅停机 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现账号会话注册表(Registry)、单账号会话封装(Session)、有序优雅停机的总管(Supervisor:asynq 先停进料 → 断会话 → 关池 → flush)、信号接线与并发启动上限(SetLimit),并把真实 Registry 注入 M7 的 metrics、收口 M7 遗留的 orphaned Dispatcher。

**Architecture:** 新增 `internal/cluster` 包。会话的"连接"抽象为 `Conn` 接口、设备锁抽象为 `DeviceLockHandle` 接口,使 Session/Registry/Supervisor 全部可用 fake 做 `-race` 单测;真实 whatsmeow 客户端只在一个编译期校验的薄适配器文件里出现(沿用 M6 asynq 适配器模式)。Supervisor 用 `golang.org/x/sync/errgroup`(已是依赖)的 `SetLimit` 限制并发起号,并以固定顺序优雅停机。

**Tech Stack:** Go 1.26.4、golang.org/x/sync/errgroup、go.mau.fi/whatsmeow(仅适配器)、hibiken/asynq、pgx/v5、prometheus(经 RegistrySnapshotProvider 注入)。

## Global Constraints

- **whatsmeow 不可测**:任何真实 `whatsmeow.NewClient/Connect/Disconnect` 只能出现在单个适配器文件 `internal/cluster/conn_whatsmeow.go`,用 `var _ Conn = (*waConn)(nil)` 编译期校验;Session/Registry/Supervisor 的逻辑测试一律用 fakeConn/fakeLock,绝不连真实 WA。
- **包隔离/无环**:`internal/cluster` 生产代码不得 import `internal/metrics`(`Registry.ActiveSessions() int` 结构化满足 `metrics.RegistrySnapshotProvider`,编译期断言放 `cluster` 的 `_test.go` 或 `cmd`);不得在生产代码 import `internal/store`(Session 持有 `DeviceLockHandle` 接口,`*store.DeviceLock` 结构化满足之,由 cmd/测试注入具体锁)。适配器文件可 import whatsmeow/dispatch。`dispatch`/`store`/`metrics` 不得 import `cluster`。
- **停机顺序不可变**:`Supervisor.Shutdown` 必须严格按 ① 停 asynq 进料(等在途 handler 跑完)② 取消后台循环并等其退出 ③ 断开所有会话(每个 Session 先 `conn.Disconnect()` 再 `lock.Release()`)④ 关 `store.Manager`(池+sqlDB)⑤ flush 日志。理由:in-flight 发送用 DB 池与会话,asynq.Shutdown 返回后才无 handler 在用;会话用 lockPool,必须在关池前释放锁。
- **Session.Close 顺序**:先 `conn.Disconnect()`(停收发)后 `lock.Release(ctx)`(让出 advisory 锁),防止双开。
- ctx 贯穿;错误 `%w`;`-race` 干净;并发数据结构用 `sync.RWMutex` 或等价,Registry 必须 race-clean。
- 不改已合并业务包(billing/sendgate/dispatch/store/metrics)的现有导出签名;仅 `dispatch` 新增一个 `DispatchRunning` 方法(Task 5,用于收口调度循环)与 `store` 可选只读不变。
- `errgroup.Group.SetLimit(n)`:n 来自新增 config `MaxConcurrentStarts`(默认 32)。`ShutdownTimeout` 新增(默认 30s)。
- module `github.com/acme/wadist`,Go 1.26.4,二进制 `/usr/local/bin/go`。

---

## File Structure

- `internal/config/config.go` — 增 `ShutdownTimeout time.Duration`(env `WADIST_SHUTDOWN_TIMEOUT`,默认 30s)、`MaxConcurrentStarts int`(env `WADIST_MAX_CONCURRENT_STARTS`,默认 32)。
- `internal/cluster/types.go` — `Conn`、`DeviceLockHandle` 接口;`Session` 结构 + `Close`/`Healthy`/`JID`。
- `internal/cluster/session_test.go` — Session.Close 顺序、Healthy 单测(fakeConn/fakeLock)。
- `internal/cluster/registry.go` — `Registry`(线程安全会话表)+ `ActiveSessions() int` + `Add/Get/Remove/Snapshot/Len/CloseAll`。
- `internal/cluster/registry_test.go` — 并发 race 测试 + RegistrySnapshotProvider 编译断言。
- `internal/cluster/supervisor.go` — `Supervisor`、`Shutdown`(有序)、`StartAccounts`(errgroup.SetLimit)、`RunLoop`(可取消后台循环)。
- `internal/cluster/supervisor_test.go` — 停机顺序、SetLimit 上限、双次 Shutdown 安全、RunLoop 取消。
- `internal/cluster/conn_whatsmeow.go` — 真实 `waConn`(whatsmeow 适配,编译期校验)。
- `internal/cluster/sender.go` — `RoutingSender`(按 JID 从 Registry 查会话路由,satisfies `dispatch.Sender`)。
- `internal/cluster/sender_test.go` — 未知 JID → 错误;已注册 → 路由到会话。
- `internal/dispatch/scheduler.go` — `(*Dispatcher).DispatchRunning(ctx, batch)`(扫 running 活动并分发)。
- `internal/dispatch/scheduler_test.go` — 集成测试(seeded running campaign)。
- `cmd/wadist/main.go` — 重接线:Registry→DBCollector、Supervisor 持有 asynq/metrics/dispatch-loop、信号→Supervisor.Shutdown、移除 orphaned Dispatcher。
- `cmd/wadist/main_smoke_test.go` — 启动→ActiveSessions 反映注册→有序停机冒烟。

---

### Task 1: config 字段 + cluster 类型(Conn/DeviceLockHandle/Session)

**Files:**
- Modify: `internal/config/config.go`(+ ShutdownTimeout, MaxConcurrentStarts)
- Create: `internal/cluster/types.go`
- Create: `internal/cluster/session_test.go`

**Interfaces:**
- Produces:
  - `type Conn interface { Connect(ctx context.Context) error; Disconnect() }`
  - `type DeviceLockHandle interface { Healthy(ctx context.Context) bool; Release(ctx context.Context) }`
  - `type Session struct {...}`;`func NewSession(jid string, conn Conn, lock DeviceLockHandle) *Session`;`func (s *Session) Close(ctx context.Context)`;`func (s *Session) Healthy(ctx context.Context) bool`;`func (s *Session) JID() string`。
- Consumes: 无。

- [ ] **Step 1: config 失败测试**

`internal/config/config_test.go` 追加:

```go
func TestLoad_ShutdownAndConcurrencyDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_SHUTDOWN_TIMEOUT", "")
	t.Setenv("WADIST_MAX_CONCURRENT_STARTS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Fatalf("shutdown default: got %v", cfg.ShutdownTimeout)
	}
	if cfg.MaxConcurrentStarts != 32 {
		t.Fatalf("starts default: got %d", cfg.MaxConcurrentStarts)
	}
}
```

- [ ] **Step 2: 改 config.go**

在 `Config` 结构增加:

```go
ShutdownTimeout     time.Duration
MaxConcurrentStarts int
```

在 `Load()` 中(沿用现有 `getenv` 辅助;解析 duration 用 `time.ParseDuration`,失败回退默认;解析 int 用 `strconv.Atoi`,失败或 <=0 回退默认):

```go
cfg.ShutdownTimeout = 30 * time.Second
if v := getenv("WADIST_SHUTDOWN_TIMEOUT", ""); v != "" {
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		cfg.ShutdownTimeout = d
	}
}
cfg.MaxConcurrentStarts = 32
if v := getenv("WADIST_MAX_CONCURRENT_STARTS", ""); v != "" {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		cfg.MaxConcurrentStarts = n
	}
}
```

> 读现有 config.go 的 import 与 `getenv` 签名,确保 `time`/`strconv` 已/正确导入。

- [ ] **Step 3: 跑 config 测试**

Run: `/usr/local/bin/go test ./internal/config/ -run TestLoad_ShutdownAndConcurrencyDefaults -v`
Expected: PASS

- [ ] **Step 4: 写 Session 失败测试**

`internal/cluster/session_test.go`:

```go
package cluster

import (
	"context"
	"errors"
	"testing"
)

type fakeConn struct {
	connectErr error
	connected  bool
	events     *[]string
	name       string
}

func (f *fakeConn) Connect(_ context.Context) error {
	if f.connectErr != nil {
		return f.connectErr
	}
	f.connected = true
	return nil
}
func (f *fakeConn) Disconnect() {
	f.connected = false
	if f.events != nil {
		*f.events = append(*f.events, f.name+":disconnect")
	}
}

type fakeLock struct {
	healthy bool
	events  *[]string
	name    string
}

func (l *fakeLock) Healthy(_ context.Context) bool { return l.healthy }
func (l *fakeLock) Release(_ context.Context) {
	if l.events != nil {
		*l.events = append(*l.events, l.name+":release")
	}
}

func TestSession_Close_DisconnectsThenReleases(t *testing.T) {
	var seq []string
	c := &fakeConn{events: &seq, name: "conn", connected: true}
	l := &fakeLock{healthy: true, events: &seq, name: "lock"}
	s := NewSession("jid-1", c, l)

	s.Close(context.Background())

	if len(seq) != 2 || seq[0] != "conn:disconnect" || seq[1] != "lock:release" {
		t.Fatalf("close order wrong: %v", seq)
	}
	if c.connected {
		t.Fatal("conn still connected")
	}
}

func TestSession_Healthy_DelegatesToLock(t *testing.T) {
	s := NewSession("jid-1", &fakeConn{}, &fakeLock{healthy: false})
	if s.Healthy(context.Background()) {
		t.Fatal("expected unhealthy when lock unhealthy")
	}
	s2 := NewSession("jid-2", &fakeConn{}, &fakeLock{healthy: true})
	if !s2.Healthy(context.Background()) {
		t.Fatal("expected healthy")
	}
}

func TestSession_Close_Idempotent(t *testing.T) {
	c := &fakeConn{connected: true}
	l := &fakeLock{healthy: true}
	s := NewSession("jid-1", c, l)
	s.Close(context.Background())
	s.Close(context.Background()) // must not panic / double-release
	_ = errors.New
}
```

- [ ] **Step 5: 跑确认失败**

Run: `/usr/local/bin/go test ./internal/cluster/ -run TestSession -v`
Expected: FAIL(包不存在)

- [ ] **Step 6: 写 types.go**

```go
// Package cluster manages active account sessions and orchestrates graceful
// shutdown. The live whatsmeow connection is abstracted behind Conn and the
// advisory device lock behind DeviceLockHandle so session lifecycle, the
// Registry, and the Supervisor are all unit-testable with fakes. The only file
// that touches a real whatsmeow client is conn_whatsmeow.go.
package cluster

import (
	"context"
	"sync"
)

// Conn is one account's live transport (real impl wraps a whatsmeow client).
type Conn interface {
	Connect(ctx context.Context) error
	Disconnect()
}

// DeviceLockHandle is the cross-process ownership lock for an account.
// *store.DeviceLock satisfies this structurally.
type DeviceLockHandle interface {
	Healthy(ctx context.Context) bool
	Release(ctx context.Context)
}

// Session is one active account: a live Conn guarded by an advisory lock.
type Session struct {
	jid    string
	conn   Conn
	lock   DeviceLockHandle
	mu     sync.Mutex
	closed bool
}

func NewSession(jid string, conn Conn, lock DeviceLockHandle) *Session {
	return &Session{jid: jid, conn: conn, lock: lock}
}

func (s *Session) JID() string { return s.jid }

func (s *Session) Healthy(ctx context.Context) bool {
	return s.lock != nil && s.lock.Healthy(ctx)
}

// Close detaches the session: disconnect the live transport FIRST (stop
// sending/receiving), THEN release the advisory lock (so another node may take
// over only after we have fully detached). Idempotent.
func (s *Session) Close(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.conn != nil {
		s.conn.Disconnect()
	}
	if s.lock != nil {
		s.lock.Release(ctx)
	}
}
```

- [ ] **Step 7: 跑测试**

Run: `/usr/local/bin/go test ./internal/cluster/ ./internal/config/ -race -v`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add internal/config/ internal/cluster/types.go internal/cluster/session_test.go
git commit -m "feat(cluster): Conn/DeviceLockHandle abstractions + Session lifecycle; shutdown/concurrency config"
```

---

### Task 2: Registry(线程安全会话表 + ActiveSessions)

**Files:**
- Create: `internal/cluster/registry.go`
- Create: `internal/cluster/registry_test.go`

**Interfaces:**
- Consumes: `Session`(Task 1)。
- Produces: `type Registry struct{...}`;`func NewRegistry() *Registry`;`func (r *Registry) Add(s *Session)`;`func (r *Registry) Get(jid string) (*Session, bool)`;`func (r *Registry) Remove(jid string) (*Session, bool)`;`func (r *Registry) Snapshot() []*Session`;`func (r *Registry) Len() int`;`func (r *Registry) ActiveSessions() int`;`func (r *Registry) CloseAll(ctx context.Context)`。

- [ ] **Step 1: 写失败测试**

`internal/cluster/registry_test.go`:

```go
package cluster

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/acme/wadist/internal/metrics"
)

// 编译期断言：Registry 满足 metrics.RegistrySnapshotProvider（断言放测试，生产 cluster 不 import metrics）。
var _ metrics.RegistrySnapshotProvider = (*Registry)(nil)

func TestRegistry_AddGetRemoveLen(t *testing.T) {
	r := NewRegistry()
	if r.ActiveSessions() != 0 {
		t.Fatal("empty registry should be 0")
	}
	s := NewSession("jid-1", &fakeConn{}, &fakeLock{healthy: true})
	r.Add(s)
	if r.Len() != 1 || r.ActiveSessions() != 1 {
		t.Fatalf("len=%d active=%d", r.Len(), r.ActiveSessions())
	}
	got, ok := r.Get("jid-1")
	if !ok || got != s {
		t.Fatal("get failed")
	}
	removed, ok := r.Remove("jid-1")
	if !ok || removed != s || r.Len() != 0 {
		t.Fatal("remove failed")
	}
}

func TestRegistry_AddReplacesAndClosesOld(t *testing.T) {
	r := NewRegistry()
	oldConn := &fakeConn{connected: true}
	r.Add(NewSession("jid-1", oldConn, &fakeLock{healthy: true}))
	// 重复 Add 同 JID：旧会话必须被 Close（防双开），新会话占位
	newConn := &fakeConn{connected: true}
	r.Add(NewSession("jid-1", newConn, &fakeLock{healthy: true}))
	if r.Len() != 1 {
		t.Fatalf("len=%d", r.Len())
	}
	if oldConn.connected {
		t.Fatal("old session must be disconnected when replaced")
	}
}

func TestRegistry_CloseAll(t *testing.T) {
	r := NewRegistry()
	conns := make([]*fakeConn, 5)
	for i := range conns {
		conns[i] = &fakeConn{connected: true}
		r.Add(NewSession(fmt.Sprintf("jid-%d", i), conns[i], &fakeLock{healthy: true}))
	}
	r.CloseAll(context.Background())
	if r.Len() != 0 {
		t.Fatalf("registry not drained: %d", r.Len())
	}
	for i, c := range conns {
		if c.connected {
			t.Fatalf("session %d not disconnected", i)
		}
	}
}

func TestRegistry_ConcurrentRace(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			jid := fmt.Sprintf("jid-%d", i%10)
			r.Add(NewSession(jid, &fakeConn{}, &fakeLock{healthy: true}))
			_, _ = r.Get(jid)
			_ = r.ActiveSessions()
			_ = r.Snapshot()
			r.Remove(jid)
		}(i)
	}
	wg.Wait()
}
```

- [ ] **Step 2: 跑确认失败**

Run: `/usr/local/bin/go test ./internal/cluster/ -run TestRegistry -v`
Expected: FAIL(undefined NewRegistry)

- [ ] **Step 3: 写 registry.go**

```go
package cluster

import (
	"context"
	"sync"
)

// Registry is the thread-safe set of active account sessions on this node.
// It satisfies metrics.RegistrySnapshotProvider via ActiveSessions().
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*Session)}
}

// Add registers a session. If a session for the same JID already exists it is
// closed first (defensive: never keep two live connections for one account).
func (r *Registry) Add(s *Session) {
	r.mu.Lock()
	old, ok := r.sessions[s.jid]
	r.sessions[s.jid] = s
	r.mu.Unlock()
	if ok && old != s {
		old.Close(context.Background())
	}
}

func (r *Registry) Get(jid string) (*Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[jid]
	return s, ok
}

func (r *Registry) Remove(jid string) (*Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[jid]
	if ok {
		delete(r.sessions, jid)
	}
	return s, ok
}

func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

// ActiveSessions satisfies metrics.RegistrySnapshotProvider.
func (r *Registry) ActiveSessions() int { return r.Len() }

func (r *Registry) Snapshot() []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out
}

// CloseAll detaches every session and empties the registry. Used by the
// Supervisor during graceful shutdown, after asynq intake has stopped.
func (r *Registry) CloseAll(ctx context.Context) {
	r.mu.Lock()
	sessions := r.sessions
	r.sessions = make(map[string]*Session)
	r.mu.Unlock()
	for _, s := range sessions {
		s.Close(ctx)
	}
}
```

- [ ] **Step 4: 跑测试(race)**

Run: `/usr/local/bin/go test ./internal/cluster/ -race -v`
Expected: PASS(含并发用例)

- [ ] **Step 5: 提交**

```bash
git add internal/cluster/registry.go internal/cluster/registry_test.go
git commit -m "feat(cluster): thread-safe session Registry with ActiveSessions provider"
```

---

### Task 3: Supervisor(有序停机 + SetLimit 起号 + 可取消循环)

**Files:**
- Create: `internal/cluster/supervisor.go`
- Create: `internal/cluster/supervisor_test.go`

**Interfaces:**
- Consumes: `Registry`、`Session`(Task 1-2);`golang.org/x/sync/errgroup`。
- Produces:
  - `type Supervisor struct{...}`
  - `func NewSupervisor(reg *Registry, opts SupervisorOpts) *Supervisor`,其中 `type SupervisorOpts struct { StopIntake func(); CloseStore func(); Flush func(); Limit int; ShutdownTimeout time.Duration }`
  - `func (s *Supervisor) Go(fn func(ctx context.Context) error)`(在受管 goroutine 中运行 fn,传入可取消 ctx;Shutdown 时取消并等待其退出。fn 自管节奏/ticker)
  - `func (s *Supervisor) StartAccounts(ctx context.Context, jids []string, start func(ctx context.Context, jid string) (*Session, error)) error`(errgroup.SetLimit(Limit);成功者 Add 进 Registry)
  - `func (s *Supervisor) Shutdown()`(有序、幂等)

- [ ] **Step 1: 写失败测试**

`internal/cluster/supervisor_test.go`:

```go
package cluster

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSupervisor_ShutdownOrder(t *testing.T) {
	var seq []string
	var mu sync.Mutex
	rec := func(s string) { mu.Lock(); seq = append(seq, s); mu.Unlock() }

	reg := NewRegistry()
	conn := &fakeConn{connected: true}
	reg.Add(NewSession("jid-1", conn, &fakeLock{healthy: true}))

	sup := NewSupervisor(reg, SupervisorOpts{
		StopIntake:      func() { rec("intake") },
		CloseStore:      func() { rec("store") },
		Flush:           func() { rec("flush") },
		Limit:           4,
		ShutdownTimeout: 2 * time.Second,
	})
	// 一个后台循环，Shutdown 必须先取消它（在断会话/关池之前 loop 已退出）
	loopStopped := make(chan struct{})
	sup.Go(func(ctx context.Context) error {
		<-ctx.Done()
		close(loopStopped)
		return ctx.Err()
	})

	sup.Shutdown()

	select {
	case <-loopStopped:
	default:
		t.Fatal("loop not stopped by shutdown")
	}
	mu.Lock()
	defer mu.Unlock()
	// 期望顺序：intake -> store -> flush（session 断开通过 conn.connected 验证，夹在 intake 与 store 之间）
	if len(seq) != 3 || seq[0] != "intake" || seq[1] != "store" || seq[2] != "flush" {
		t.Fatalf("shutdown order wrong: %v", seq)
	}
	if conn.connected {
		t.Fatal("sessions not disconnected during shutdown")
	}
	if reg.Len() != 0 {
		t.Fatal("registry not drained")
	}
}

func TestSupervisor_ShutdownIdempotent(t *testing.T) {
	reg := NewRegistry()
	calls := int32(0)
	sup := NewSupervisor(reg, SupervisorOpts{
		StopIntake: func() { atomic.AddInt32(&calls, 1) },
		CloseStore: func() {}, Flush: func() {}, Limit: 2, ShutdownTimeout: time.Second,
	})
	sup.Shutdown()
	sup.Shutdown() // 第二次必须 no-op
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("StopIntake called %d times, want 1", calls)
	}
}

func TestSupervisor_StartAccounts_RespectsLimit(t *testing.T) {
	reg := NewRegistry()
	sup := NewSupervisor(reg, SupervisorOpts{
		StopIntake: func() {}, CloseStore: func() {}, Flush: func() {},
		Limit: 4, ShutdownTimeout: time.Second,
	})
	var cur, max int32
	jids := make([]string, 40)
	for i := range jids {
		jids[i] = fmt.Sprintf("jid-%d", i)
	}
	err := sup.StartAccounts(context.Background(), jids, func(_ context.Context, jid string) (*Session, error) {
		n := atomic.AddInt32(&cur, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		return NewSession(jid, &fakeConn{}, &fakeLock{healthy: true}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if max > 4 {
		t.Fatalf("concurrency %d exceeded limit 4", max)
	}
	if reg.Len() != 40 {
		t.Fatalf("expected 40 registered, got %d", reg.Len())
	}
}

func TestSupervisor_StartAccounts_StartErrorDoesNotRegister(t *testing.T) {
	reg := NewRegistry()
	sup := NewSupervisor(reg, SupervisorOpts{
		StopIntake: func() {}, CloseStore: func() {}, Flush: func() {},
		Limit: 4, ShutdownTimeout: time.Second,
	})
	_ = sup.StartAccounts(context.Background(), []string{"a", "b"}, func(_ context.Context, jid string) (*Session, error) {
		if jid == "a" {
			return nil, fmt.Errorf("boom")
		}
		return NewSession(jid, &fakeConn{}, &fakeLock{healthy: true}), nil
	})
	// 失败的不入册；成功的入册
	if _, ok := reg.Get("a"); ok {
		t.Fatal("failed start must not register")
	}
	if _, ok := reg.Get("b"); !ok {
		t.Fatal("successful start should register")
	}
}
```

- [ ] **Step 2: 跑确认失败**

Run: `/usr/local/bin/go test ./internal/cluster/ -run TestSupervisor -v`
Expected: FAIL(undefined NewSupervisor)

- [ ] **Step 3: 写 supervisor.go**

```go
package cluster

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

type SupervisorOpts struct {
	StopIntake      func()        // stop asynq intake (waits for in-flight handlers)
	CloseStore      func()        // close store.Manager (pools + sqlDB)
	Flush           func()        // flush logger
	Limit           int           // max concurrent account starts
	ShutdownTimeout time.Duration // bound for the disconnect/close phase
}

type managedLoop struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type Supervisor struct {
	reg   *Registry
	opts  SupervisorOpts
	mu    sync.Mutex
	loops []*managedLoop
	done  bool
}

func NewSupervisor(reg *Registry, opts SupervisorOpts) *Supervisor {
	if opts.Limit <= 0 {
		opts.Limit = 32
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 30 * time.Second
	}
	return &Supervisor{reg: reg, opts: opts}
}

// Go runs fn in a managed goroutine. fn receives a context cancelled at
// Shutdown and is expected to return when ctx is done (fn owns its own ticker/
// pacing). Shutdown cancels all such goroutines and waits for them to exit
// before disconnecting sessions.
func (s *Supervisor) Go(fn func(ctx context.Context) error) {
	ctx, cancel := context.WithCancel(context.Background())
	ml := &managedLoop{cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.loops = append(s.loops, ml)
	s.mu.Unlock()
	go func() {
		defer close(ml.done)
		_ = fn(ctx)
	}()
}

// StartAccounts brings up accounts with bounded concurrency (SetLimit). Each
// successful Session is registered; a start error is returned in aggregate but
// does not register a half-open session.
func (s *Supervisor) StartAccounts(ctx context.Context, jids []string, start func(ctx context.Context, jid string) (*Session, error)) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.opts.Limit)
	for _, jid := range jids {
		jid := jid
		g.Go(func() error {
			sess, err := start(gctx, jid)
			if err != nil {
				return err
			}
			s.reg.Add(sess)
			return nil
		})
	}
	return g.Wait()
}

// Shutdown performs the fixed graceful order and is idempotent:
//  1. stop asynq intake (waits for in-flight handlers)
//  2. cancel + await background loops
//  3. disconnect all sessions (conn.Disconnect then lock.Release each)
//  4. close store (pools + sqlDB)
//  5. flush logger
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	loops := s.loops
	s.mu.Unlock()

	// 1. intake
	if s.opts.StopIntake != nil {
		s.opts.StopIntake()
	}
	// 2. loops
	for _, ml := range loops {
		ml.cancel()
	}
	for _, ml := range loops {
		<-ml.done
	}
	// 3. sessions (bounded by ShutdownTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.ShutdownTimeout)
	s.reg.CloseAll(ctx)
	cancel()
	// 4. store
	if s.opts.CloseStore != nil {
		s.opts.CloseStore()
	}
	// 5. flush
	if s.opts.Flush != nil {
		s.opts.Flush()
	}
}
```

> **说明:** 测试 `TestSupervisor_ShutdownOrder` 记录 intake/store/flush 三点;会话断开发生在 store 之前(顺序 ③→④),由 `conn.connected==false` 与 `reg.Len()==0` 验证。`Go` 是通用受管 goroutine 原语,fn 自管 ticker(见 Task 5 main 的 dispatch 循环)。

- [ ] **Step 4: 跑测试(race)**

Run: `/usr/local/bin/go test ./internal/cluster/ -race -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/cluster/supervisor.go internal/cluster/supervisor_test.go
git commit -m "feat(cluster): Supervisor ordered graceful shutdown + SetLimit account startup + managed loops"
```

---

### Task 4: whatsmeow Conn 适配器 + RoutingSender

**Files:**
- Create: `internal/cluster/conn_whatsmeow.go`
- Create: `internal/cluster/sender.go`
- Create: `internal/cluster/sender_test.go`

**Interfaces:**
- Consumes: `Conn`(Task 1)、`Registry`(Task 2)、`dispatch.Sender`/`dispatch.MediaHandle`(已合并)、whatsmeow、`store.ApplyProxy`/`*store.ProxyBinding`。
- Produces:
  - `type waConn struct{...}`;`func NewWAConn(device *store.Device, waLog waLog.Logger, proxy *store.ProxyBinding) *waConn`;实现 `Conn`。
  - `type SessionSender interface { Send(ctx, phone, body string, media *dispatch.MediaHandle) (string, error) }`(单账号会话发送能力)。
  - `type RoutingSender struct{ reg *Registry }`;`func NewRoutingSender(reg *Registry) *RoutingSender`;实现 `dispatch.Sender`(按 `jid` 从 Registry 查会话并路由)。

> **范围:** `conn_whatsmeow.go` 是唯一 import whatsmeow 的文件,仅编译期校验 `var _ Conn = (*waConn)(nil)`,不做真实连接测试(无 WA 环境)。真实消息发送(whatsmeow SendMessage)属后续发送里程碑;本任务的 `waConn.Connect` 调 `store.ApplyProxy` + `client.Connect()`,`Disconnect` 调 `client.Disconnect()`。`RoutingSender` 让 dispatch 的发送经由 Registry 中的活跃会话——若 JID 无活跃会话则返回明确错误(发送链路据此走 requeue/refund,不静默成功)。

- [ ] **Step 1: 写 RoutingSender 失败测试**

`internal/cluster/sender_test.go`:

```go
package cluster

import (
	"context"
	"errors"
	"testing"

	"github.com/acme/wadist/internal/dispatch"
)

// sessionSender 把一个可发送会话塞进 Session 以便 RoutingSender 路由（测试用）。
// 生产中由 waConn 提供发送能力；此处用 fake。
type fakeSendConn struct {
	fakeConn
	sendErr error
	id      string
	gotJID  string
}

func TestRoutingSender_UnknownJID(t *testing.T) {
	reg := NewRegistry()
	rs := NewRoutingSender(reg)
	_, err := rs.Send(context.Background(), "jid-x", "+100", "hi", nil)
	if err == nil {
		t.Fatal("expected error for unknown jid (must not silently succeed)")
	}
}

func TestRoutingSender_RoutesToSession(t *testing.T) {
	reg := NewRegistry()
	sc := &fakeSendConn{id: "wamid.1"}
	// Session 必须能对外暴露其发送能力；见实现说明（Session 持有可选 SessionSender）。
	s := newSessionWithSender("jid-1", sc, &fakeLock{healthy: true}, sc)
	reg.Add(s)
	rs := NewRoutingSender(reg)
	id, err := rs.Send(context.Background(), "jid-1", "+100", "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "wamid.1" {
		t.Fatalf("got id %q", id)
	}
	_ = errors.New
	_ = dispatch.MediaHandle{}
}

func (f *fakeSendConn) Send(_ context.Context, phone, body string, _ *dispatch.MediaHandle) (string, error) {
	if f.sendErr != nil {
		return "", f.sendErr
	}
	return f.id, nil
}
```

> **实现说明(给 implementer):** 为让 RoutingSender 路由到会话的发送能力,`Session` 需要一个可选的 `sender SessionSender` 字段 + 一个测试构造 `newSessionWithSender(jid, conn, lock, sender)`(放 sender.go 或 types.go;生产 `NewSession` 默认 sender=nil)。`RoutingSender.Send(ctx, jid, phone, body, media)`:`reg.Get(jid)` → 无则 `fmt.Errorf("cluster: no active session for routing")`;有但 `session.sender==nil` 则 `fmt.Errorf("cluster: session has no send capability")`;否则委托 `session.sender.Send(ctx, phone, body, media)`。`SessionSender` 接口签名见上(`Send(ctx, phone, body string, media *dispatch.MediaHandle)(string,error)`)。`var _ dispatch.Sender = (*RoutingSender)(nil)` 编译期校验。

- [ ] **Step 2: 跑确认失败**

Run: `/usr/local/bin/go test ./internal/cluster/ -run TestRoutingSender -v`
Expected: FAIL

- [ ] **Step 3: 写 sender.go**

```go
package cluster

import (
	"context"
	"fmt"

	"github.com/acme/wadist/internal/dispatch"
)

// SessionSender is one account session's outbound capability.
type SessionSender interface {
	Send(ctx context.Context, phone, body string, media *dispatch.MediaHandle) (string, error)
}

// add an optional send capability to Session (production NewSession leaves nil).
func newSessionWithSender(jid string, conn Conn, lock DeviceLockHandle, sender SessionSender) *Session {
	s := NewSession(jid, conn, lock)
	s.sender = sender
	return s
}

// RoutingSender implements dispatch.Sender by routing each send to the live
// session that owns the destination JID. A missing/incapable session returns a
// clear error so the send pipeline requeues/refunds rather than silently
// succeeding.
type RoutingSender struct{ reg *Registry }

func NewRoutingSender(reg *Registry) *RoutingSender { return &RoutingSender{reg: reg} }

var _ dispatch.Sender = (*RoutingSender)(nil)

func (rs *RoutingSender) Send(ctx context.Context, jid, phone, body string, media *dispatch.MediaHandle) (string, error) {
	sess, ok := rs.reg.Get(jid)
	if !ok {
		return "", fmt.Errorf("cluster: no active session for routing")
	}
	if sess.sender == nil {
		return "", fmt.Errorf("cluster: session has no send capability")
	}
	return sess.sender.Send(ctx, phone, body, media)
}
```

> 并在 `types.go` 的 `Session` 结构增加字段 `sender SessionSender`(零值 nil)。

- [ ] **Step 4: 写 conn_whatsmeow.go(薄适配,编译期校验)**

```go
package cluster

import (
	"context"

	"github.com/acme/wadist/internal/store"
	"go.mau.fi/whatsmeow"
	waproto "go.mau.fi/whatsmeow/store"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// waConn is the real whatsmeow-backed Conn. It is the ONLY file in this package
// that imports whatsmeow; everything else is fake-testable. No live-connection
// unit test exists (requires a real WhatsApp device + 4G proxy).
type waConn struct {
	client *whatsmeow.Client
	proxy  *store.ProxyBinding
}

// NewWAConn builds a client over a registered device store. Proxy is applied at
// Connect time (per-account dynamic 4G proxy) before dialing.
func NewWAConn(device *waproto.Device, logger waLog.Logger, proxy *store.ProxyBinding) *waConn {
	return &waConn{client: whatsmeow.NewClient(device, logger), proxy: proxy}
}

var _ Conn = (*waConn)(nil)

func (c *waConn) Connect(_ context.Context) error {
	if c.proxy != nil {
		if err := store.ApplyProxy(c.client, c.proxy); err != nil {
			return err
		}
	}
	return c.client.Connect()
}

func (c *waConn) Disconnect() { c.client.Disconnect() }
```

> **implementer 注意:** 确认 `store.GetDeviceStore` 返回的设备类型(侦察:`*go.mau.fi/whatsmeow/store.Device`)与 `whatsmeow.NewClient(device, logger)` 形参匹配;确认 `store.ApplyProxy(client *whatsmeow.Client, b *ProxyBinding) error` 与 `*store.ProxyBinding` 字段(`ProxyURL`)。whatsmeow 客户端创建/连接的真实签名以源码为准——若 `NewClient` 需要额外参数,按实际调整,但保持 `Conn` 接口不变。**只**在本文件 import whatsmeow。

- [ ] **Step 5: 跑测试 + build/vet**

Run:
```bash
/usr/local/bin/go test ./internal/cluster/ -race -v
/usr/local/bin/go build ./... && /usr/local/bin/go vet ./internal/cluster/
```
Expected: 全 PASS;build/vet 干净。

- [ ] **Step 6: 提交**

```bash
git add internal/cluster/conn_whatsmeow.go internal/cluster/sender.go internal/cluster/sender_test.go internal/cluster/types.go
git commit -m "feat(cluster): whatsmeow Conn adapter + RoutingSender over Registry"
```

---

### Task 5: 调度循环收口 + main 重接线(Supervisor 总装)

**Files:**
- Create: `internal/dispatch/scheduler.go`
- Create: `internal/dispatch/scheduler_test.go`
- Modify: `cmd/wadist/main.go`
- Modify: `cmd/wadist/main_smoke_test.go`

**Interfaces:**
- Consumes: 全部前序 + `cluster.Registry/NewSupervisor/NewRoutingSender`、`metrics.*`、`dispatch.*`。
- Produces: `func (d *Dispatcher) DispatchRunning(ctx context.Context, batch int) (int, error)`(扫 `campaigns WHERE state='running'`,对每个调 `dispatchBatch`,返回总分发数)。

- [ ] **Step 1: scheduler 失败测试**

`internal/dispatch/scheduler_test.go`(复用本包既有 pgPool/redisClient/applyMigrations/seed 辅助):

```go
package dispatch

import (
	"testing"
)

func TestDispatchRunning_DispatchesRunningCampaigns(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	// 复用既有 batch happy-case setup：seed 账号容量 + 一个 state='running' 的 campaign + 若干 pending recipients。
	d, ctx := setupRunningCampaignCase(t) // 提取既有 setup 为辅助；若不存在则内联
	n, err := d.DispatchRunning(ctx, 10)
	if err != nil {
		t.Fatalf("DispatchRunning: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected >=1 dispatched across running campaigns, got %d", n)
	}
}
```

> implementer:`setupRunningCampaignCase` 用既有 seedAccount/seedCampaign/seedRecipient 构造一个 `state='running'` 的活动(注意 seedCampaign 若默认 state 非 running,需 UPDATE 或加参数)+ pending recipients;返回构造好的 `*Dispatcher` 与 ctx。

- [ ] **Step 2: 跑确认失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./internal/dispatch/ -run TestDispatchRunning -v`
Expected: FAIL(undefined DispatchRunning)

- [ ] **Step 3: 写 scheduler.go**

```go
// internal/dispatch/scheduler.go
package dispatch

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// DispatchRunning scans all running campaigns and dispatches one batch each,
// returning the total number of recipients enqueued. Intended to be driven by a
// supervised background loop. Per-campaign errors abort the scan (caller retries
// next tick); a campaign with no capacity simply contributes 0.
func (d *Dispatcher) DispatchRunning(ctx context.Context, batch int) (int, error) {
	rows, err := d.pool.Query(ctx, `SELECT id FROM campaigns WHERE state='running' ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("scan running campaigns: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan campaign id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate campaigns: %w", err)
	}

	total := 0
	for _, id := range ids {
		n, err := d.dispatchBatch(ctx, id, batch)
		if err != nil {
			return total, fmt.Errorf("dispatch campaign %d: %w", id, err)
		}
		total += n
	}
	_ = pgx.ErrNoRows
	return total, nil
}
```

> implementer:确认 `Dispatcher` 字段名 `pool`(侦察确认);确认 `dispatchBatch(ctx, campaignID, batch)` 签名。`_ = pgx.ErrNoRows` 仅占位避免误删 import——若 pgx 未被该文件实际使用则删掉该行与 import(只 import 用到的)。

- [ ] **Step 4: 跑 scheduler 测试**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./internal/dispatch/ -run TestDispatchRunning -race -v`
Expected: PASS

- [ ] **Step 5: 重写 main.go run() 用 Supervisor 总装**

按下述结构改 `cmd/wadist/main.go`(保留 `run(ctx, cfg) (*metrics.Server, func(), error)` 签名供冒烟测试,但 stop() 改为调用 `sup.Shutdown`):

要点(implementer 按真实签名落地):
1. `reg := cluster.NewRegistry()`。
2. metrics:`reg.MustRegister(metrics.NewDBCollector(pool, 10*time.Second, registry))` —— 把 **nil 改为 `reg`**(真实 RegistrySnapshotProvider)。
3. dispatch worker 的 Sender 用 `cluster.NewRoutingSender(reg)` 替换 `placeholderSender{}`(Uploader 暂仍占位,真实媒体上传属后续里程碑;RoutingSender 让无活跃会话时发送显式失败而非静默成功)。
4. 构造 `dispatcher := dispatch.NewDispatcher(pool, billingRepo, enqueuer, priceFor, 3*time.Second).WithMetrics(m)`(不再 `_ =` 丢弃)。
5. 构造 Supervisor:
```go
sup := cluster.NewSupervisor(reg, cluster.SupervisorOpts{
	StopIntake:      func() { asynqSrv.Shutdown() },
	CloseStore:      mgr.Close,
	Flush:           flush,
	Limit:           cfg.MaxConcurrentStarts,
	ShutdownTimeout: cfg.ShutdownTimeout,
})
```
6. 启动后台调度循环(受管,Shutdown 时取消):
```go
sup.Go(func(lctx context.Context) error {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-lctx.Done():
			return lctx.Err()
		case <-t.C:
			if _, err := dispatcher.DispatchRunning(lctx, 100); err != nil {
				// 记录但不退出循环（瞬时 DB 错误下次 tick 重试）
			}
		}
	}
})
```
7. 启动 asynq(`go asynqSrv.Run(mux)`)与 metrics server(`srv.Start()`)。
8. **停机收口**:`stop()` 改为:
```go
stop := func() {
	sup.Shutdown()                  // intake→loops→sessions→store→flush
	sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	_ = srv.Shutdown(sctx)          // metrics http（最后停，便于停机期间仍可抓取）
	c()
	_ = asynqClient.Close()
	_ = rdb.Close()
}
```
> 注意:`sup.Shutdown` 已负责 `asynqSrv.Shutdown()`(StopIntake)、`mgr.Close()`(CloseStore)、`flush()`。`stop()` 不要再重复调用这三者,只补 metrics http 与 redis/asynq client 句柄关闭,避免双关。`mgr.Close` 幂等(nil-guard),但 flush 重复调用可能报错——**只在 Supervisor 内 flush 一次**。
9. 删除 M7 的 orphaned Dispatcher TODO 行与占位 `placeholderSender`(若 RoutingSender 取代之;`placeholderUploader` 保留并注释为后续里程碑)。
10. metrics 的 `RegistrySnapshotProvider` 现为真实 `reg`——确认 `cmd` import `internal/cluster`,无环。

- [ ] **Step 6: 更新冒烟测试**

`cmd/wadist/main_smoke_test.go`:在 run() 起来后,构造并 `reg.Add` 一个 fake 会话不可行(reg 在 run 内部)——改为断言 `/metrics` 含 `wadist_active_sessions`,且 stop() 后端口关闭。核心仍是:无 DSN 时 skip;有 DSN 时 run()→GET /metrics 200 且含 `wadist_`→stop() 干净返回(无 panic、无 goroutine 泄漏)。若需要验证 active_sessions 随注册变化,可在 cluster 包的单测层面覆盖(已由 registry_test 覆盖),此处只验证装配与停机。

- [ ] **Step 7: 全门禁**

Run:
```bash
/usr/local/bin/go build ./... && /usr/local/bin/go vet ./...
./scripts/check_metric_labels.sh
TESTCONTAINERS_RYUK_DISABLED=true /usr/local/bin/go test ./... -race
```
Expected: 全绿;高基数门禁 OK。

- [ ] **Step 8: 提交**

```bash
git add internal/dispatch/scheduler.go internal/dispatch/scheduler_test.go cmd/wadist/
git commit -m "feat(cmd): Supervisor-driven graceful shutdown + real Registry/RoutingSender wiring + dispatch loop"
```

---

## Self-Review(写作后自查)

- **Spec 覆盖:** Registry ✅(Task 2)、Session ✅(Task 1)、Supervisor.Shutdown 有序(asynq→断会话→关池)✅(Task 3)、信号接线 ✅(Task 5 main 沿用 signal.NotifyContext→sup.Shutdown)、SetLimit(32) ✅(Task 3 StartAccounts)。收口:真实 RegistrySnapshotProvider 注入 ✅、orphaned Dispatcher 接调度循环 ✅、RoutingSender 取代 placeholderSender ✅。
- **可测性:** whatsmeow 仅 conn_whatsmeow.go(编译期校验);Session/Registry/Supervisor 全 fake 单测 race-clean。
- **无环:** 生产 cluster 不 import metrics/store;断言放 _test/cmd;适配器单独文件 import whatsmeow/store。dispatch/store/metrics 不 import cluster。
- **类型一致:** `Conn`/`DeviceLockHandle`/`Session`/`Registry`/`Supervisor`/`SupervisorOpts`/`RoutingSender`/`SessionSender`/`DispatchRunning` 跨任务一致。`ActiveSessions() int` Task2 定义、Task5 注入。
- **停机不双关:** main 的 stop() 明确不重复 asynqSrv.Shutdown/mgr.Close/flush(由 Supervisor 负责),只补 metrics http + redis/asynq client。
- **占位扫描:** 无 TODO/占位逻辑;Supervisor.Go 为通用受管 goroutine 原语,无死参数。
- **前置承接:** M9 的 `DeviceLock.Healthy` 所有权再校验仍是 M9 事项;Session.Healthy 现委托 lock.Healthy(M9 增强后自动受益)。
