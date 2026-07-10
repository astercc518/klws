# E4 Evolution 背压 / 反封原语 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 Evolution 发送路径的背压/反封原语——HTTP 错误分类（`EvoHTTPError` + `IsPermanent`/`IsThrottle`）、`perInstanceLimiter`（按 jid 有界并发）、`circuitBreakerSender`（按 jid 熔断）——**全部休眠**（不接 live SendWorker，默认仍 whatsmeow），零行为变更。

**Architecture:** 限流器与熔断器是**协议无关的 `dispatch.Sender` 装饰器**（包装任意 Sender），因此不依赖任何未验证的 Evolution JSON 字段——只有分类器基于标准 HTTP 状态码（429/503/400）。`doJSON` 改为返回类型化 `*EvoHTTPError`（向后兼容：调用方只查 `err != nil`）。live 组合（`evoSender`→retry(WithPermanent=cluster.IsPermanent)→limiter→breaker、throttle→governor 乘性减、healthSink→真 sendgate）与门控 `WADIST_SENDER` 生效属 **E6 cutover** 接线；E4 只造+测原语。

**Tech Stack:** Go、net/http + httptest、`sync`（per-key 信号量/熔断状态）、注入时钟测熔断、`internal/dispatch` 的 `Sender` 接口。

## Global Constraints

逐字遵守：

- **零行为变更**：E4 新符号无 live 调用者（whatsmeow `RoutingSender` 仍唯一活跃 `Sender`）。不改 `SendWorker`/`pump`/`governor`/`cmd`。
- **不制造 import 环**：limiter/breaker 全在 `internal/dispatch`，只依赖包内 `Sender`；分类器在 `internal/cluster`（cmd 在 cutover 时桥接，dispatch 不 import cluster）。
- **doJSON 向后兼容**：返回 `*EvoHTTPError`（实现 `error`），既有「status>=300 → err != nil」测试仍绿。
- **不引入 whatsmeow 依赖**到新文件。
- **HTTP 状态语义待钉**：`IsPermanent`/`IsThrottle` 的状态码集合以真机 Evolution 错误为准，源码标 `// TODO(evo-verify): confirm which HTTP statuses Evolution returns for logged-out/throttle`。
- **`make gate` 全程绿**（除预存在 `GO-2026-5856`）；gofmt 干净；提交带 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` trailer。
- **并发测试防抖**：熔断用**注入时钟**（不 sleep）；限流器用**channel barrier**（`time.After` 仅作「证明被阻塞」的宽松上界，不作正向时序依赖）。

**执行前置**：从 `main`（现 `5383306`）新建分支 `feat/evolution-e4-backpressure`，先确认基线编译。

---

## Task 1: `EvoHTTPError` + 分类器（`IsPermanent`/`IsThrottle`）

**Files:**
- Modify: `internal/cluster/evolution_client.go`
- Test: `internal/cluster/evolution_client_test.go`（追加）

**Interfaces:**
- Produces:
  - `type EvoHTTPError struct { StatusCode int; Method, Path, Body string }` + `func (e *EvoHTTPError) Error() string`。
  - `doJSON` 在 `StatusCode >= 300` 时返回 `*EvoHTTPError`（替代原 `fmt.Errorf`）。
  - `func IsThrottle(err error) bool` —— `errors.As` 到 `*EvoHTTPError` 且状态 `429`/`503`。
  - `func IsPermanent(err error) bool` —— `errors.As` 到 `*EvoHTTPError` 且状态 `400`/`401`/`403`/`404`/`422`（客户端错误，重试无益；含 logged-out/not-connected 的典型返回）。非 `*EvoHTTPError`（网络/超时）→ 两者皆 false（= 可重试的瞬态）。

- [ ] **Step 1: 写失败测试**

`internal/cluster/evolution_client_test.go` 追加：
```go
func TestEvoHTTPError_Classification(t *testing.T) {
	throttle := &EvoHTTPError{StatusCode: 429}
	perm := &EvoHTTPError{StatusCode: 400}
	other := errors.New("dial tcp: timeout")

	if !IsThrottle(throttle) || IsThrottle(perm) || IsThrottle(other) {
		t.Fatal("IsThrottle wrong")
	}
	if !IsPermanent(perm) || IsPermanent(throttle) || IsPermanent(other) {
		t.Fatal("IsPermanent wrong")
	}
	if !IsPermanent(&EvoHTTPError{StatusCode: 404}) {
		t.Fatal("404 should be permanent")
	}
	if !IsThrottle(&EvoHTTPError{StatusCode: 503}) {
		t.Fatal("503 should be throttle")
	}
}

func TestEvoClient_doJSON_ReturnsTypedHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate"}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	_, err := c.SendText(context.Background(), "wa_1", "1555", "hi")
	if err == nil {
		t.Fatal("expected error on 429")
	}
	var he *EvoHTTPError
	if !errors.As(err, &he) || he.StatusCode != 429 {
		t.Fatalf("want *EvoHTTPError 429, got %T %v", err, err)
	}
	if !IsThrottle(err) {
		t.Fatal("429 send error should classify as throttle")
	}
}
```
（测试 import 需 `errors`；若文件已 import 则复用。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster/ -run "TestEvoHTTPError|TestEvoClient_doJSON_ReturnsTyped" -v`
Expected: FAIL（`EvoHTTPError`/`IsThrottle`/`IsPermanent` 未定义）

- [ ] **Step 3: 实现**

`internal/cluster/evolution_client.go`：
① 顶部（import 增加 `"errors"`，若无）新增类型与分类器：
```go
// EvoHTTPError is a non-2xx response from Evolution, carrying the status code
// so the send path can classify it (throttle vs permanent vs transient).
type EvoHTTPError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *EvoHTTPError) Error() string {
	return fmt.Sprintf("evolution %s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// IsThrottle reports whether err is a rate/overload signal (retry after backoff,
// and shed the global rate). TODO(evo-verify): confirm Evolution's throttle codes.
func IsThrottle(err error) bool {
	var he *EvoHTTPError
	return errors.As(err, &he) && (he.StatusCode == 429 || he.StatusCode == 503)
}

// IsPermanent reports whether err will not be fixed by retrying (client errors:
// bad request, unauthorized, logged-out / not-connected instance, gone).
// TODO(evo-verify): confirm which statuses Evolution returns for logged-out.
func IsPermanent(err error) bool {
	var he *EvoHTTPError
	if !errors.As(err, &he) {
		return false // network/transport errors are transient → retryable
	}
	switch he.StatusCode {
	case 400, 401, 403, 404, 422:
		return true
	}
	return false
}
```
② `doJSON` 里把
```go
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("evolution %s %s: %d %s", method, path, resp.StatusCode, msg)
	}
```
改为
```go
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &EvoHTTPError{StatusCode: resp.StatusCode, Method: method, Path: path, Body: string(msg)}
	}
```

- [ ] **Step 4: 跑测试确认通过（含既有 client 测试不回归）**

Run: `go test ./internal/cluster/ -run "TestEvoHTTPError|TestEvoClient" -v`
Expected: PASS（既有 `TestEvoClient_*` 仍绿——它们只查 `err != nil`）

- [ ] **Step 5: Commit**

```bash
git add internal/cluster/evolution_client.go internal/cluster/evolution_client_test.go
git commit -m "feat(cluster): typed EvoHTTPError + IsThrottle/IsPermanent classifiers"
```

---

## Task 2: `perInstanceLimiter`（按 jid 有界并发装饰器）

**Files:**
- Create: `internal/dispatch/sender_limiter.go`
- Test: `internal/dispatch/sender_limiter_test.go`

**Interfaces:**
- Consumes: `dispatch.Sender`。
- Produces:
  - `type perInstanceLimiter struct { inner Sender; max int; mu sync.Mutex; sems map[string]chan struct{} }`
  - `func NewPerInstanceLimiter(inner Sender, max int) *perInstanceLimiter` —— `max<1`→`1`。
  - `var _ Sender = (*perInstanceLimiter)(nil)`
- 行为 `Send(ctx, jid, ...)`：取（或惰性建）该 jid 的容量为 `max` 的信号量 channel；`select` 抢占一格（或 `ctx.Done()` 返回 `ctx.Err()`）；`defer` 释放；委托 `inner.Send`。**同 jid 并发被钳在 `max`；不同 jid 互不影响。**

- [ ] **Step 1: 写失败测试（channel barrier，确定性）**

`internal/dispatch/sender_limiter_test.go`：
```go
package dispatch

import (
	"context"
	"testing"
	"time"
)

// gateSender blocks each Send until the test pushes to release, and announces
// entry on entered. Lets the test observe concurrency deterministically.
type gateSender struct {
	entered chan string
	release chan struct{}
}

func (g *gateSender) Send(_ context.Context, jid, _, _ string, _ *MediaHandle) (string, error) {
	g.entered <- jid
	<-g.release
	return "id", nil
}

func TestPerInstanceLimiter_SameJIDSerialized(t *testing.T) {
	g := &gateSender{entered: make(chan string, 4), release: make(chan struct{}, 4)}
	l := NewPerInstanceLimiter(g, 1)

	go func() { _, _ = l.Send(context.Background(), "j", "1", "a", nil) }() // A
	<-g.entered                                                             // A is inside inner (holds the only slot)

	done := make(chan struct{})
	go func() { _, _ = l.Send(context.Background(), "j", "1", "b", nil); close(done) }() // B

	// B must be blocked on acquire — it neither enters inner nor completes.
	select {
	case <-g.entered:
		t.Fatal("B entered inner before A released (limit not enforced)")
	case <-done:
		t.Fatal("B completed before A released")
	case <-time.After(80 * time.Millisecond):
		// expected: B blocked
	}

	g.release <- struct{}{} // release A
	<-g.entered             // now B enters
	g.release <- struct{}{} // release B
	<-done
}

func TestPerInstanceLimiter_DifferentJIDsConcurrent(t *testing.T) {
	g := &gateSender{entered: make(chan string, 4), release: make(chan struct{}, 4)}
	l := NewPerInstanceLimiter(g, 1)

	go func() { _, _ = l.Send(context.Background(), "a", "1", "x", nil) }()
	go func() { _, _ = l.Send(context.Background(), "b", "1", "y", nil) }()

	// Both distinct jids should be able to enter inner concurrently.
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case j := <-g.entered:
			got[j] = true
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("only %d/2 distinct jids entered concurrently: %v", len(got), got)
		}
	}
	if !got["a"] || !got["b"] {
		t.Fatalf("expected both a and b, got %v", got)
	}
	g.release <- struct{}{}
	g.release <- struct{}{}
}

func TestPerInstanceLimiter_CtxCancelReleases(t *testing.T) {
	g := &gateSender{entered: make(chan string, 4), release: make(chan struct{}, 4)}
	l := NewPerInstanceLimiter(g, 1)
	// Fill the slot with a blocked A.
	go func() { _, _ = l.Send(context.Background(), "j", "1", "a", nil) }()
	<-g.entered
	// B with a cancelled ctx must not block forever — returns ctx error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Send(ctx, "j", "1", "b", nil); err == nil {
		t.Fatal("expected ctx error when slot full and ctx cancelled")
	}
	g.release <- struct{}{}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dispatch/ -run TestPerInstanceLimiter -v`
Expected: FAIL（未定义）

- [ ] **Step 3: 实现**

`internal/dispatch/sender_limiter.go`：
```go
package dispatch

import (
	"context"
	"sync"
)

// perInstanceLimiter bounds concurrent sends per account (jid → one Evolution
// instance → one Baileys socket). Blasting a single socket with concurrent
// sends reorders/trips WhatsApp; this caps in-flight sends per jid at max.
// DORMANT: composed into the live send path only at E6 cutover.
type perInstanceLimiter struct {
	inner Sender
	max   int
	mu    sync.Mutex
	sems  map[string]chan struct{}
}

func NewPerInstanceLimiter(inner Sender, max int) *perInstanceLimiter {
	if max < 1 {
		max = 1
	}
	return &perInstanceLimiter{inner: inner, max: max, sems: map[string]chan struct{}{}}
}

var _ Sender = (*perInstanceLimiter)(nil)

func (l *perInstanceLimiter) sem(jid string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.sems[jid]
	if s == nil {
		s = make(chan struct{}, l.max)
		l.sems[jid] = s
	}
	return s
}

func (l *perInstanceLimiter) Send(ctx context.Context, jid, phone, body string, media *MediaHandle) (string, error) {
	s := l.sem(jid)
	select {
	case s <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s }()
	return l.inner.Send(ctx, jid, phone, body, media)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dispatch/ -run TestPerInstanceLimiter -v -race`
Expected: PASS（`-race` 无竞态）

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/sender_limiter.go internal/dispatch/sender_limiter_test.go
git commit -m "feat(dispatch): perInstanceLimiter bounds concurrent sends per jid (dormant)"
```

---

## Task 3: `circuitBreakerSender`（按 jid 熔断装饰器）

**Files:**
- Create: `internal/dispatch/sender_breaker.go`
- Test: `internal/dispatch/sender_breaker_test.go`

**Interfaces:**
- Consumes: `dispatch.Sender`。
- Produces:
  - `var ErrCircuitOpen = errors.New("dispatch: circuit open for account")`
  - `type circuitBreakerSender struct { inner Sender; threshold int; cooloff time.Duration; now func() time.Time; mu sync.Mutex; state map[string]*breakerState }`；`type breakerState struct { fails int; openUntil time.Time }`
  - `func NewCircuitBreakerSender(inner Sender, threshold int, cooloff time.Duration) *circuitBreakerSender` —— `threshold<1`→`1`；`now` 默认 `time.Now`。
  - `func (b *circuitBreakerSender) withNow(fn func() time.Time) *circuitBreakerSender` —— 仅测试注入时钟。
  - `var _ Sender = (*circuitBreakerSender)(nil)`
- 行为 `Send(ctx, jid, ...)`：
  - 若该 jid 处于 open（`now() < openUntil`）→ 直接返回 `("", ErrCircuitOpen)`，**不**调 inner。
  - 否则调 inner：成功 → 清零 `fails`、清 `openUntil`（关闭/半开转闭）；失败 → `fails++`，若 `fails >= threshold` → `openUntil = now() + cooloff`；返回 inner 的结果。
  - （半开=冷却过后自然允许一次 inner 试探，由上面逻辑天然覆盖。）

- [ ] **Step 1: 写失败测试（注入时钟，确定性）**

`internal/dispatch/sender_breaker_test.go`：
```go
package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

type flakySender struct {
	calls int
	err   error // when non-nil, every call fails with this
}

func (f *flakySender) Send(_ context.Context, _, _, _ string, _ *MediaHandle) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return "OK", nil
}

func TestCircuitBreaker_OpensAfterThresholdAndShortCircuits(t *testing.T) {
	inner := &flakySender{err: errors.New("boom")}
	now := time.Unix(1000, 0)
	b := NewCircuitBreakerSender(inner, 2, 30*time.Second).withNow(func() time.Time { return now })

	// 2 failures reach threshold → circuit opens.
	_, _ = b.Send(context.Background(), "j", "1", "a", nil)
	_, _ = b.Send(context.Background(), "j", "1", "a", nil)
	if inner.calls != 2 {
		t.Fatalf("inner calls=%d want 2", inner.calls)
	}
	// 3rd call while open → ErrCircuitOpen, inner NOT called.
	_, err := b.Send(context.Background(), "j", "1", "a", nil)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("want ErrCircuitOpen, got %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("inner must not be called while open: calls=%d", inner.calls)
	}
}

func TestCircuitBreaker_HalfOpenAfterCooloff(t *testing.T) {
	inner := &flakySender{err: errors.New("boom")}
	now := time.Unix(1000, 0)
	b := NewCircuitBreakerSender(inner, 1, 30*time.Second).withNow(func() time.Time { return now })

	_, _ = b.Send(context.Background(), "j", "1", "a", nil) // opens (threshold 1)
	if _, err := b.Send(context.Background(), "j", "1", "a", nil); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected open, got %v", err)
	}
	// Advance past cooloff → half-open: inner is tried again.
	now = now.Add(31 * time.Second)
	inner.err = nil // now it recovers
	id, err := b.Send(context.Background(), "j", "1", "a", nil)
	if err != nil || id != "OK" {
		t.Fatalf("half-open trial should reach inner: id=%q err=%v", id, err)
	}
	// After success, circuit closed: subsequent calls flow.
	if _, err := b.Send(context.Background(), "j", "1", "a", nil); err != nil {
		t.Fatalf("closed circuit should pass: %v", err)
	}
}

func TestCircuitBreaker_SuccessResetsFailCount(t *testing.T) {
	inner := &flakySender{err: errors.New("boom")}
	now := time.Unix(1000, 0)
	b := NewCircuitBreakerSender(inner, 2, 30*time.Second).withNow(func() time.Time { return now })
	_, _ = b.Send(context.Background(), "j", "1", "a", nil) // fail 1
	inner.err = nil
	_, _ = b.Send(context.Background(), "j", "1", "a", nil) // success → reset
	inner.err = errors.New("boom again")
	_, _ = b.Send(context.Background(), "j", "1", "a", nil) // fail 1 again (not 2)
	// Circuit should still be closed (only 1 consecutive fail since reset).
	if _, err := b.Send(context.Background(), "j", "1", "a", nil); errors.Is(err, ErrCircuitOpen) {
		t.Fatal("circuit opened too early — success did not reset fail count")
	}
}

func TestCircuitBreaker_PerJIDIsolation(t *testing.T) {
	inner := &flakySender{err: errors.New("boom")}
	now := time.Unix(1000, 0)
	b := NewCircuitBreakerSender(inner, 1, 30*time.Second).withNow(func() time.Time { return now })
	_, _ = b.Send(context.Background(), "j1", "1", "a", nil) // opens j1
	// j2 is independent — should still reach inner.
	before := inner.calls
	_, _ = b.Send(context.Background(), "j2", "1", "a", nil)
	if inner.calls != before+1 {
		t.Fatal("j2 breaker should be independent of j1")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dispatch/ -run TestCircuitBreaker -v`
Expected: FAIL（未定义）

- [ ] **Step 3: 实现**

`internal/dispatch/sender_breaker.go`：
```go
package dispatch

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned by circuitBreakerSender when an account's breaker
// is open (too many recent consecutive failures) — the send is shed without
// touching the Evolution instance.
var ErrCircuitOpen = errors.New("dispatch: circuit open for account")

type breakerState struct {
	fails     int
	openUntil time.Time
}

// circuitBreakerSender trips a per-jid breaker after `threshold` consecutive
// failures, shedding sends for `cooloff` before a half-open trial. Protects a
// sick Evolution instance (and our own throughput) from a hot failure loop.
// DORMANT: composed into the live send path only at E6 cutover.
type circuitBreakerSender struct {
	inner     Sender
	threshold int
	cooloff   time.Duration
	now       func() time.Time
	mu        sync.Mutex
	state     map[string]*breakerState
}

func NewCircuitBreakerSender(inner Sender, threshold int, cooloff time.Duration) *circuitBreakerSender {
	if threshold < 1 {
		threshold = 1
	}
	return &circuitBreakerSender{
		inner:     inner,
		threshold: threshold,
		cooloff:   cooloff,
		now:       time.Now,
		state:     map[string]*breakerState{},
	}
}

// withNow injects a clock for tests.
func (b *circuitBreakerSender) withNow(fn func() time.Time) *circuitBreakerSender {
	b.now = fn
	return b
}

var _ Sender = (*circuitBreakerSender)(nil)

func (b *circuitBreakerSender) st(jid string) *breakerState {
	s := b.state[jid]
	if s == nil {
		s = &breakerState{}
		b.state[jid] = s
	}
	return s
}

func (b *circuitBreakerSender) Send(ctx context.Context, jid, phone, body string, media *MediaHandle) (string, error) {
	b.mu.Lock()
	s := b.st(jid)
	if !s.openUntil.IsZero() && b.now().Before(s.openUntil) {
		b.mu.Unlock()
		return "", ErrCircuitOpen
	}
	b.mu.Unlock()

	id, err := b.inner.Send(ctx, jid, phone, body, media)

	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		s.fails = 0
		s.openUntil = time.Time{}
		return id, nil
	}
	s.fails++
	if s.fails >= b.threshold {
		s.openUntil = b.now().Add(b.cooloff)
	}
	return id, err
}
```

- [ ] **Step 4: 跑测试通过 + 全包编译**

Run: `go test ./internal/dispatch/ -run "TestCircuitBreaker|TestPerInstanceLimiter" -v -race && go build ./... && gofmt -l internal/dispatch/sender_breaker.go internal/dispatch/sender_breaker_test.go`
Expected: PASS + 编译 + gofmt 空

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/sender_breaker.go internal/dispatch/sender_breaker_test.go
git commit -m "feat(dispatch): circuitBreakerSender per-jid breaker (dormant)"
```

---

## E4 收尾：门禁 + 零回归自检

- [ ] **门禁**

Run: `make gate`
Expected: `tidy vet test-race labels` 绿；`vuln` 仅 `GO-2026-5856`。

- [ ] **零回归自检**

Run: `grep -rn "NewPerInstanceLimiter\|NewCircuitBreakerSender\|IsThrottle\|IsPermanent\|EvoHTTPError" internal/ cmd/ --include=*.go | grep -v _test.go | grep -vE "internal/dispatch/sender_(limiter|breaker).go|internal/cluster/evolution_client.go"`
Expected: **空**——E4 新符号在定义文件外无调用者（SendWorker/cmd/governor 未接线），零行为变更。

---

## Self-Review（对 spec §7 E4）

- **per-instance semaphore** → Task 2（`perInstanceLimiter`）。✅
- **错误分类（429/503→throttle；400 not-connected/loggedOut→permanent）** → Task 1（`IsThrottle`/`IsPermanent`）。✅
- **per-instance 熔断** → Task 3（`circuitBreakerSender`）。✅
- **429/503→governor 乘性减** → **E6 cutover 接线**：组合 `IsThrottle`（Task 1）+ 既有 `governor`（AIMD `setRate`）。E4 只提供分类原语；governor 未改。
- **loggedOut→permanent 不重连** → `IsPermanent`（Task 1）供 E2 `retrySender.WithPermanent` 在 cutover 时注入；实例不重连由 E1 `evoInstance` 的 `permanent`/state 与 reaper 决策（已在）。
- **接上 E2 retrySender.WithPermanent / E3 healthSink** → **E6 cutover 接线**（cmd 桥接 `cluster.IsPermanent` + 真 `sendgate.SendGate`）。
- **门控 WADIST_SENDER 生效 / live 组合** → E6（本模块休眠，收尾自检证明零调用者）。
- **占位符扫描**：无 TBD；每步含实际代码/命令/期望。
- **类型一致性**：`perInstanceLimiter`/`circuitBreakerSender` 均 `var _ Sender` 断言，签名对 `dispatch.Sender`；`IsPermanent` 供 `retrySender.WithPermanent(func(error)bool)` 直接用（签名匹配）；`EvoHTTPError` 实现 `error`，`doJSON` 既有调用方无感。
- **待钉点**：`IsPermanent`/`IsThrottle` 的状态码集合标 `TODO(evo-verify)`（真机 Evolution 的 logged-out/throttle 状态码需确认）。

---

**下一步**：E5（多 Evolution 节点分片：一致性哈希 `instance→evo_node`、每节点容量上限、`instanceResolver` 据此路由——填 10 万账号密度落差）。E4 合并后 just-in-time 写。之后 E6 cutover：live 组合 + `WADIST_SENDER` + 拆 whatsmeow + E3 carry-forwards（500-on-DB-error、first-see-bind）+ 全量重扫码。
