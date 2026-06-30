# cluster 反指纹增强（presence/typing/优雅 logout + fence-on-send）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让每条会话"连接→停留→打字→发送→优雅下线"像真人，并在发送前校验仍持有所有权（fence-on-send），降低 WhatsApp 风控评分。

**Architecture:** 在 `internal/cluster` 新增**可选接口** `PresenceConn`（`SetPresence`/`SendTyping`），由 `waConn` 实现；`Session.GracefulClose` 做"presence-unavailable → linger → Disconnect"；`RoutingSender.Send` 在真正发送前做 fence 校验 + composing→抖动→send→paused。全部加性，不碰锁/registry/supervisor/防双开/计费逻辑。

**Tech Stack:** Go 1.26、whatsmeow v0.0.0-20260622（`SendPresence`/`SendChatPresence`，均带 ctx）。

## Global Constraints

- 红线：用户**已授权编辑 `internal/cluster`**，但仅限**加性**改动；**不得**改 `AcquireDeviceLock`/`DeviceLock`/`Registry`/`Supervisor`/`Session.Close` 的锁与生命周期/防双开次序，**不得**碰 `billing`/`sendgate`/`dispatch`/`store` 逻辑。
- **⚠️ 绝对禁止调用 `client.Logout()`**（会注销 companion 设备凭证）。"优雅下线" = `SendPresence(Unavailable)` + `Disconnect()`。
- 依赖 B2 计划提供的 `DeviceLockHandle.Healthy()`（Redis 实现）；fence-on-send 调它。`backend=pg` 下 `Healthy()` 同样可用（语义等价），故本计划可独立于 B2 cutover 先行。
- whatsmeow API：`SendPresence(ctx, types.Presence)`（`PresenceAvailable`/`PresenceUnavailable`）、`SendChatPresence(ctx, jid types.JID, types.ChatPresence, types.ChatPresenceMedia)`（`ChatPresenceComposing`/`ChatPresencePaused`）。
- presence/typing 失败一律 best-effort（不阻断发送）。fence 失败则**阻断**发送、返回 `ErrLostOwnership`（上层 requeue，不计费）。
- 总开关 `WADIST_ANTIFP`（默认 on，off 等价改造前，作回滚）。

## File Structure

- Modify `internal/cluster/types.go` — 新增 `PresenceConn` 可选接口；`Session.GracefulClose`。
- Modify `internal/cluster/conn_whatsmeow.go` — `waConn` 实现 `SetPresence`/`SendTyping`。
- Modify `internal/cluster/sender.go` — `RoutingSender` 加 fence-on-send + typing 序列 + 配置。
- Create `internal/cluster/antifingerprint_test.go` — fake-based 序列断言。
- Modify `internal/config/config.go` — env：`WADIST_ANTIFP`、`WADIST_OWNERSHIP_FENCE_ON_SEND`、typing/dwell/linger 区间。
- Modify `cmd/wadist/main.go` — 用配置构造 `RoutingSender`；warm 流程注入 presence-available + dwell（dwell 计时属控制面，本计划仅在 factory 后调一次 `SetPresence(true)`）。

---

## Task 1: `PresenceConn` 可选接口 + `waConn` 实现

**Files:**
- Modify: `internal/cluster/types.go`
- Modify: `internal/cluster/conn_whatsmeow.go`
- Test: `internal/cluster/antifingerprint_test.go`

**Interfaces:**
- Produces:
  - `type PresenceConn interface { SetPresence(ctx context.Context, available bool) error; SendTyping(ctx context.Context, chatPhone string, composing bool) error }`
  - `*waConn` 额外满足 `PresenceConn`

- [ ] **Step 1: 写失败测试 — 一个记录调用的 fake 满足 PresenceConn**

```go
// internal/cluster/antifingerprint_test.go
package cluster

import (
	"context"
	"testing"
)

type recordingConn struct {
	connected bool
	calls     []string
}
func (c *recordingConn) Connect(context.Context) error { c.connected = true; return nil }
func (c *recordingConn) Disconnect()                    { c.calls = append(c.calls, "disconnect") }
func (c *recordingConn) SetPresence(_ context.Context, available bool) error {
	if available { c.calls = append(c.calls, "present:on") } else { c.calls = append(c.calls, "present:off") }
	return nil
}
func (c *recordingConn) SendTyping(_ context.Context, _ string, composing bool) error {
	if composing { c.calls = append(c.calls, "typing:on") } else { c.calls = append(c.calls, "typing:off") }
	return nil
}

func TestRecordingConnSatisfiesPresenceConn(t *testing.T) {
	var _ Conn = (*recordingConn)(nil)
	var _ PresenceConn = (*recordingConn)(nil)
}
```

Run: `go test ./internal/cluster/ -run TestRecordingConnSatisfiesPresenceConn -count=1`
Expected: FAIL（`PresenceConn` 未定义）

- [ ] **Step 2: 定义 `PresenceConn`（types.go）**

```go
// PresenceConn 是 Conn 的可选拟人能力：在线状态与打字提示。waConn 实现它；
// 不实现的 Conn（测试 fake、无能力连接）在发送路径被安全跳过。
type PresenceConn interface {
	SetPresence(ctx context.Context, available bool) error
	SendTyping(ctx context.Context, chatPhone string, composing bool) error
}
```

- [ ] **Step 3: `waConn` 实现两法（conn_whatsmeow.go）**

```go
func (c *waConn) SetPresence(ctx context.Context, available bool) error {
	st := types.PresenceUnavailable
	if available {
		st = types.PresenceAvailable
	}
	return c.client.SendPresence(ctx, st)
}

func (c *waConn) SendTyping(ctx context.Context, chatPhone string, composing bool) error {
	jid := types.NewJID(chatPhone, types.DefaultUserServer)
	st := types.ChatPresencePaused
	if composing {
		st = types.ChatPresenceComposing
	}
	return c.client.SendChatPresence(ctx, jid, st, "")
}

var _ PresenceConn = (*waConn)(nil)
```

- [ ] **Step 4: 跑测试转绿 + 构建**

Run: `go build ./... && go test ./internal/cluster/ -run TestRecordingConnSatisfiesPresenceConn -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/cluster/types.go internal/cluster/conn_whatsmeow.go internal/cluster/antifingerprint_test.go
git commit -m "feat(cluster): optional PresenceConn (SetPresence/SendTyping) on waConn"
```

---

## Task 2: `Session.GracefulClose`（presence-off → linger → Close）

**Files:**
- Modify: `internal/cluster/types.go`
- Test: `internal/cluster/antifingerprint_test.go`

**Interfaces:**
- Consumes: `PresenceConn`（Task 1）、现有 `Session.Close`
- Produces: `func (s *Session) GracefulClose(ctx context.Context, linger time.Duration)`

- [ ] **Step 1: 写失败测试 — presence-off 先于 disconnect，且从不 Logout**

```go
func TestGracefulCloseSequence(t *testing.T) {
	rc := &recordingConn{}
	sess := NewSession("jid-1", rc, fakeLock{healthy: true})
	sess.GracefulClose(context.Background(), 0) // linger=0 跳过停留

	want := []string{"present:off", "disconnect"}
	if len(rc.calls) != 2 || rc.calls[0] != want[0] || rc.calls[1] != want[1] {
		t.Fatalf("calls=%v want %v", rc.calls, want)
	}
}

type fakeLock struct{ healthy bool }
func (l fakeLock) Healthy(context.Context) bool { return l.healthy }
func (l fakeLock) Release(context.Context)        {}
```

Run: `go test ./internal/cluster/ -run TestGracefulCloseSequence -count=1`
Expected: FAIL（`GracefulClose` 未定义）

- [ ] **Step 2: 实现 GracefulClose（types.go）**

```go
// GracefulClose 优雅下线：presence-unavailable → linger 排空回执 → Close（断连+释锁）。
// 绝不调用 whatsmeow Logout()。linger<=0 跳过停留。复用 Close 的 disconnect→release 次序。
func (s *Session) GracefulClose(ctx context.Context, linger time.Duration) {
	if pc, ok := s.conn.(PresenceConn); ok {
		_ = pc.SetPresence(ctx, false)
	}
	if linger > 0 {
		select {
		case <-time.After(linger):
		case <-ctx.Done():
		}
	}
	s.Close(ctx)
}
```

（types.go 顶部确保 import `"time"`。）

- [ ] **Step 3: 跑测试转绿**

Run: `go test ./internal/cluster/ -run TestGracefulCloseSequence -count=1`
Expected: PASS

- [ ] **Step 4: 提交**

```bash
git add internal/cluster/types.go internal/cluster/antifingerprint_test.go
git commit -m "feat(cluster): Session.GracefulClose (presence-off → linger → Close; never Logout)"
```

---

## Task 3: `RoutingSender` fence-on-send + typing 序列

**Files:**
- Modify: `internal/cluster/sender.go`
- Test: `internal/cluster/antifingerprint_test.go`

**Interfaces:**
- Consumes: `PresenceConn`（Task 1）、`Registry`、`DeviceLockHandle.Healthy()`、`Session.sender`/`Session.conn`（同包私有可达）
- Produces:
  - `var ErrLostOwnership = errors.New("cluster: lost ownership before send")`
  - `type TypingPolicy struct{ Min, Max time.Duration }`
  - `func NewRoutingSenderWithPolicy(reg *Registry, fenceOnSend bool, typing TypingPolicy) *RoutingSender`
  - `RoutingSender` 新增字段 `fenceOnSend bool`、`typing TypingPolicy`

- [ ] **Step 1: 写失败测试 — 失主时阻断发送，不触达底层 sender**

```go
type recordingSender struct{ called bool }
func (s *recordingSender) Send(context.Context, string, string, *dispatch.MediaHandle) (string, error) {
	s.called = true; return "mid-1", nil
}

func TestFenceOnSendBlocksWhenUnhealthy(t *testing.T) {
	reg := NewRegistry()
	rc := &recordingConn{}
	rs := &recordingSender{}
	sess := newSessionWithSender("jid-1", rc, fakeLock{healthy: false}, rs) // lock 不健康
	reg.Add(sess)

	sender := NewRoutingSenderWithPolicy(reg, true, TypingPolicy{})
	_, err := sender.Send(context.Background(), "jid-1", "1555000", "hi", nil)
	if !errors.Is(err, ErrLostOwnership) { t.Fatalf("want ErrLostOwnership, got %v", err) }
	if rs.called { t.Fatal("底层 sender 不应被调用") }
}

func TestTypingSequenceAroundSend(t *testing.T) {
	reg := NewRegistry()
	rc := &recordingConn{}
	rs := &recordingSender{}
	sess := newSessionWithSender("jid-2", rc, fakeLock{healthy: true}, rs)
	reg.Add(sess)

	sender := NewRoutingSenderWithPolicy(reg, true, TypingPolicy{Min: 0, Max: 0}) // 抖动 0 便于断言
	if _, err := sender.Send(context.Background(), "jid-2", "1555111", "hi", nil); err != nil {
		t.Fatal(err)
	}
	// 期望顺序：typing:on 在 send 前、typing:off 在 send 后
	want := []string{"typing:on", "typing:off"}
	if len(rc.calls) != 2 || rc.calls[0] != want[0] || rc.calls[1] != want[1] {
		t.Fatalf("calls=%v want %v (typing 包裹 send)", rc.calls, want)
	}
	if !rs.called { t.Fatal("send 应发生") }
}
```

Run: `go test ./internal/cluster/ -run 'FenceOnSend|TypingSequence' -count=1`
Expected: FAIL（`NewRoutingSenderWithPolicy`/`ErrLostOwnership`/`TypingPolicy` 未定义）

- [ ] **Step 2: 改 RoutingSender（sender.go）**

```go
var ErrLostOwnership = errors.New("cluster: lost ownership before send")

type TypingPolicy struct{ Min, Max time.Duration }

type RoutingSender struct {
	reg         *Registry
	fenceOnSend bool
	typing      TypingPolicy
}

func NewRoutingSender(reg *Registry) *RoutingSender { // 保留旧构造（默认不 fence、无 typing）
	return &RoutingSender{reg: reg}
}
func NewRoutingSenderWithPolicy(reg *Registry, fenceOnSend bool, typing TypingPolicy) *RoutingSender {
	return &RoutingSender{reg: reg, fenceOnSend: fenceOnSend, typing: typing}
}

func (rs *RoutingSender) Send(ctx context.Context, jid, phone, body string, media *dispatch.MediaHandle) (string, error) {
	sess, ok := rs.reg.Get(jid)
	if !ok {
		return "", fmt.Errorf("cluster: no active session for routing")
	}
	if sess.sender == nil {
		return "", fmt.Errorf("cluster: session has no send capability")
	}
	// (A) fence-on-send：失去所有权则不发
	if rs.fenceOnSend && !sess.Healthy(ctx) {
		return "", ErrLostOwnership
	}
	// (B) 打字拟人：composing → 抖动 → send → paused（best-effort）
	pc, hasPresence := sess.conn.(PresenceConn)
	if hasPresence {
		_ = pc.SendTyping(ctx, phone, true)
		sleepJitter(ctx, rs.typing.Min, rs.typing.Max)
	}
	id, err := sess.sender.Send(ctx, phone, body, media)
	if hasPresence {
		_ = pc.SendTyping(ctx, phone, false)
	}
	return id, err
}

func sleepJitter(ctx context.Context, min, max time.Duration) {
	d := min
	if max > min {
		// 用 jid 无关的进程级 rand；测试用 min==max==0 跳过
		d = min + time.Duration(randInt63n(int64(max-min)))
	}
	if d <= 0 {
		return
	}
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
```

> `randInt63n` 用包级 `math/rand` 实例（非全局，避免锁竞争）：`var rng = rand.New(rand.NewSource(time.Now().UnixNano()))` + `func randInt63n(n int64) int64 { if n<=0 {return 0}; return rng.Int63n(n) }`，加互斥或用 `rand/v2`。sender.go 顶部补 import `errors`、`time`、`math/rand`。

- [ ] **Step 3: 跑测试转绿**

Run: `go test ./internal/cluster/ -run 'FenceOnSend|TypingSequence' -count=1`
Expected: PASS

- [ ] **Step 4: 确认现有 cluster 测试不破**

Run: `go test ./internal/cluster/ -count=1`
Expected: PASS（旧 `NewRoutingSender` 仍在，`dispatch.Sender` 接口实现不变）

- [ ] **Step 5: 提交**

```bash
git add internal/cluster/sender.go internal/cluster/antifingerprint_test.go
git commit -m "feat(cluster): fence-on-send + typing sequence in RoutingSender"
```

---

## Task 4: 配置 + 接线（config + cmd/wadist）

**Files:**
- Modify: `internal/config/config.go`
- Modify: `cmd/wadist/main.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `NewRoutingSenderWithPolicy`、`TypingPolicy`、`Session.GracefulClose`、`PresenceConn`
- Produces: `Config` 新字段 `AntifpOn bool`、`FenceOnSend bool`、`TypingMin/TypingMax/DwellMin/DwellMax/Linger time.Duration`

- [ ] **Step 1: 写失败测试 — 默认值**

```go
func TestLoad_AntifpDefaults(t *testing.T) {
	t.Setenv("WADIST_REDIS_ADDR", "x:1")
	cfg, err := Load()
	if err != nil { t.Fatal(err) }
	if !cfg.AntifpOn { t.Fatal("AntifpOn 默认应为 true") }
	if !cfg.FenceOnSend { t.Fatal("FenceOnSend 默认应为 true") }
	if cfg.TypingMin != 1200*time.Millisecond || cfg.TypingMax != 3500*time.Millisecond {
		t.Fatalf("typing 默认 = %v/%v", cfg.TypingMin, cfg.TypingMax)
	}
	if cfg.Linger != 15*time.Second {
		t.Fatalf("linger 默认 = %v", cfg.Linger)
	}
}
```

Run: `go test ./internal/config/ -run TestLoad_AntifpDefaults -count=1`
Expected: FAIL（字段未定义）

- [ ] **Step 2: 加字段 + env 解析（config.go）**

`Config` struct 加：

```go
	AntifpOn    bool
	FenceOnSend bool
	TypingMin, TypingMax time.Duration
	DwellMin, DwellMax   time.Duration
	Linger      time.Duration
```

`Load()` 内加（沿用既有 `getenv`/布尔/时长解析风格）：

```go
		AntifpOn:    getenv("WADIST_ANTIFP", "on") != "off",
		FenceOnSend: getenv("WADIST_OWNERSHIP_FENCE_ON_SEND", "true") != "false",
		TypingMin:   msEnv("WADIST_TYPING_MIN_MS", 1200),
		TypingMax:   msEnv("WADIST_TYPING_MAX_MS", 3500),
		DwellMin:    msEnv("WADIST_DWELL_MIN_MS", 3000),
		DwellMax:    msEnv("WADIST_DWELL_MAX_MS", 10000),
		Linger:      msEnv("WADIST_LINGER_MS", 15000),
```

新增 helper：

```go
func msEnv(key string, def int) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil { return time.Duration(n) * time.Millisecond }
	}
	return time.Duration(def) * time.Millisecond
}
```

（config.go 确保 import `os`、`strconv`、`time`。）

- [ ] **Step 3: cmd/wadist 用配置构造 RoutingSender + warm 后置 presence**

把 worker 构造里的 `cluster.NewRoutingSender(reg)` 改为：

```go
	var routing *cluster.RoutingSender
	if cfg.AntifpOn {
		routing = cluster.NewRoutingSenderWithPolicy(reg, cfg.FenceOnSend,
			cluster.TypingPolicy{Min: cfg.TypingMin, Max: cfg.TypingMax})
	} else {
		routing = cluster.NewRoutingSender(reg)
	}
```
并把 `cluster.NewRoutingSender(reg)` 的使用处替换为 `routing`。

在 factory 闭包内、`conn.Connect(fctx)` 成功之后、`return NewSessionWithSender(...)` 之前，加 presence-available（best-effort，仅当 AntifpOn）：

```go
		if cfg.AntifpOn {
			if pc, ok := any(conn).(cluster.PresenceConn); ok {
				_ = pc.SetPresence(fctx, true)
			}
		}
```

> dwell（连接后停留再发）的**计时**属控制面（Plan 3 的 Working-Set Manager）；本计划只保证连接即上线 presence。优雅下线由控制面在驱逐时调 `Session.GracefulClose(ctx, cfg.Linger)`（Plan 3 接线）。

- [ ] **Step 4: 跑测试 + 构建**

Run: `go test ./internal/config/ -run TestLoad_AntifpDefaults -count=1 && go build ./...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/wadist/main.go
git commit -m "feat: wire antifingerprint config (typing/dwell/linger/fence) into RoutingSender + connect presence"
```

---

## Task 5: 回滚等价测试（WADIST_ANTIFP=off）

**Files:**
- Test: `internal/cluster/antifingerprint_test.go`

- [ ] **Step 1: 写测试 — 无 policy 时不产生 presence/typing 调用**

```go
func TestNoTypingWhenPlainRoutingSender(t *testing.T) {
	reg := NewRegistry()
	rc := &recordingConn{}
	rs := &recordingSender{}
	sess := newSessionWithSender("jid-3", rc, fakeLock{healthy: true}, rs)
	reg.Add(sess)

	sender := NewRoutingSender(reg) // 旧构造 = AntifpOff 等价
	if _, err := sender.Send(context.Background(), "jid-3", "1555222", "hi", nil); err != nil {
		t.Fatal(err)
	}
	if len(rc.calls) != 0 { t.Fatalf("plain sender 不应有 presence/typing 调用, got %v", rc.calls) }
	if !rs.called { t.Fatal("send 仍应发生") }
}
```

Run: `go test ./internal/cluster/ -run TestNoTypingWhenPlainRoutingSender -count=1`
Expected: PASS（验证回滚路径等价改造前）

- [ ] **Step 2: 提交**

```bash
git add internal/cluster/antifingerprint_test.go
git commit -m "test(cluster): WADIST_ANTIFP=off equivalence (no presence/typing on plain RoutingSender)"
```

---

## 验收（合并前）

- [ ] `make gate` 绿；`go test ./internal/cluster/ ./internal/config/ -count=1` 绿。
- [ ] 全仓 grep 确认无 `client.Logout()` 新增（`grep -rn "\.Logout(" internal/cluster` 仅历史/无）。
- [ ] fence-on-send 在 `Healthy()==false` 时阻断发送且不触达底层 sender。
- [ ] typing 包裹 send（composing 前 / paused 后）；GracefulClose 为 presence-off→disconnect。
- [ ] `WADIST_ANTIFP=off` 行为等价改造前。
- [ ] 未改 `AcquireDeviceLock`/`Registry`/`Supervisor`/`Session.Close` 次序与 `billing`/`sendgate`/`dispatch`/`store` 逻辑。

## Self-Review 备注

- 用可选接口 `PresenceConn` 而非加宽 `Conn`：避免逼所有 Conn 实现/fake 改动，降低破坏面。
- dwell 计时与 GracefulClose 调用点在控制面（Plan 3）；本计划提供原语并在 connect 处上线 presence。
- fence-on-send 依赖 `DeviceLockHandle.Healthy()`：pg/redis 后端均可用，故本计划可独立于 B2 cutover。
