# E2 Evolution 发送适配器（evoSender）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 Evolution 文本发送路径——`EvoClient.SendText`（返回 `key.id`）、`evoSender`（实现 `dispatch.Sender`，jid→instance 路由 + 文本发送）、`retrySender`（指数退避装饰器）——**全部休眠**（不接 live SendWorker，默认仍 whatsmeow），零行为变更。

**Architecture:** `dispatch` 包**不能** import `cluster`（cluster 已 import dispatch，反向会成环）。因此 `evoSender` 在 `internal/dispatch` 内依赖一个**本地** `evoSendAPI` 接口（生产由 `*cluster.EvoClient` 经一层薄适配满足，接线在后续集成模块）与一个 `route` 回调（jid→instanceName，生产由 `store.Manager.InstanceForJID` 提供）。`key.id`（Evolution 返回的真实 WA 消息 id）作为 `Send` 的 msgID 返回，落 `campaign_recipients.message_id`，保回执对称（spec §8b）。媒体因 whatsmeow 的 `MediaHandle`（加密 CDN 句柄）对 Evolution 无意义，本模块 `media!=nil` 显式返回 `ErrEvoMediaUnsupported`，媒体路径待 Uploader 重设计模块。

**Tech Stack:** Go、net/http + httptest（EvoClient）、`internal/dispatch` 的 `Sender` 接口、纯 fake 单测。

## Global Constraints

逐字遵守：

- **零行为变更**：E2 新符号无 live 调用者（whatsmeow `RoutingSender` 仍是唯一活跃 `Sender`）。不改 `SendWorker`/`pump`/`cmd`；不加 `WADIST_SENDER` 的**接线**（门控与 live 选择留待集成模块；如需可加 config 字段但本模块不消费）。
- **不制造 import 环**：`internal/dispatch` 不得 import `internal/cluster`。`evoSender` 只依赖 dispatch 内定义的接口/回调。
- **回执对称**（spec §8b/§3 不变式）：`SendText` 必须回传 Evolution 响应里的 `key.id`（真实 WA 消息 id），`evoSender.Send` 以它作 msgID 返回。**不**用 Evolution 内部 uuid。
- **代理粘性**：本模块不碰实例创建/代理（E1 已定），不引入 per-send 代理动作。
- **不引入 whatsmeow 依赖**到新文件。
- **REST 字段实测待钉**：`SendText` 的 path/JSON 以真机为准，源码注释标 `// TODO(evo-verify): confirm path/fields against real Evolution v2`；httptest 断言的是「我们发出的请求形状」（spec §9①）。
- **`make gate` 全程绿**（除预存在 `GO-2026-5856`）；gofmt 干净；提交带 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` trailer。

**执行前置**：从 `main`（现 `c7096de`）新建分支 `feat/evolution-e2-sender`，先确认基线编译。

---

## Task 1: `SendResult` + `EvoClient.SendText`

**Files:**
- Modify: `internal/cluster/evolution_client.go`
- Test: `internal/cluster/evolution_client_test.go`（追加）

**Interfaces:**
- Produces:
  - `type SendResult struct { RemoteID string; Status string }` —— `RemoteID` = `key.id`（真实 WA 消息 id，回执对称锚点）；`Status` = 响应里的发送状态（如 `PENDING`/`SERVER_ACK`），可空。
  - `func (c *EvoClient) SendText(ctx context.Context, instanceName, toPhone, body string) (SendResult, error)` —— `POST /message/sendText/{instanceName}`，body `{"number":toPhone,"text":body}`；解析响应 `key.id` → `RemoteID`、`status` → `Status`。

- [ ] **Step 1: 写失败测试**

`internal/cluster/evolution_client_test.go` 追加：
```go
func TestEvoClient_SendText_ReturnsKeyID(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":{"id":"WAMID123","fromMe":true},"status":"PENDING"}`))
	}))
	defer srv.Close()

	c := NewEvoClient(srv.URL, "k")
	res, err := c.SendText(context.Background(), "wa_1", "15551234", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/message/sendText/wa_1" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotBody["number"] != "15551234" || gotBody["text"] != "hello" {
		t.Fatalf("body=%+v", gotBody)
	}
	if res.RemoteID != "WAMID123" || res.Status != "PENDING" {
		t.Fatalf("res=%+v", res)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster/ -run TestEvoClient_SendText -v`
Expected: FAIL（`SendText`/`SendResult` 未定义）

- [ ] **Step 3: 实现**

在 `internal/cluster/evolution_client.go` 追加：
```go
// SendResult carries Evolution's returned message key. RemoteID (key.id) IS the
// WhatsApp message id and MUST be persisted as campaign_recipients.message_id so
// the webhook receipt path (E3) matches on the same key the receipt carries.
type SendResult struct {
	RemoteID string
	Status   string
}

// SendText sends a plain-text message and returns the WA message id (key.id).
// TODO(evo-verify): confirm path/fields against real Evolution v2.
func (c *EvoClient) SendText(ctx context.Context, instanceName, toPhone, body string) (SendResult, error) {
	var out struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
		Status string `json:"status"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/message/sendText/"+instanceName,
		map[string]any{"number": toPhone, "text": body}, &out)
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{RemoteID: out.Key.ID, Status: out.Status}, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cluster/ -run TestEvoClient_SendText -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cluster/evolution_client.go internal/cluster/evolution_client_test.go
git commit -m "feat(cluster): EvoClient.SendText returns WA key.id (receipt symmetry)"
```

---

## Task 2: `evoSender` 实现 `dispatch.Sender`（文本；媒体 fail-fast）

**Files:**
- Create: `internal/dispatch/sender_evolution.go`
- Test: `internal/dispatch/sender_evolution_test.go`

**Interfaces:**
- Consumes: `dispatch.Sender`（types.go）、`dispatch.MediaHandle`（types.go）。
- Produces:
  - `var ErrEvoMediaUnsupported = errors.New("dispatch: evolution media send not yet supported")`
  - `type evoSendAPI interface { SendText(ctx context.Context, instanceName, toPhone, body string) (remoteID string, err error) }` —— dispatch 内的最小接口（生产由 `*cluster.EvoClient` 经薄适配满足：其 `SendText` 返回 `(SendResult, error)`，适配层取 `.RemoteID`）。
  - `type InstanceRoute func(ctx context.Context, jid string) (instanceName string, ok bool, err error)` —— jid→instanceName（生产由 `store.Manager.InstanceForJID` 提供）。
  - `type evoSender struct { api evoSendAPI; route InstanceRoute }`
  - `func NewEvoSender(api evoSendAPI, route InstanceRoute) *evoSender`
  - `var _ Sender = (*evoSender)(nil)`
- 行为 `Send(ctx, jid, phone, body, media)`：
  - `media != nil` → 返回 `("", ErrEvoMediaUnsupported)`（媒体路径待 Uploader 重设计）。
  - `route` 返回 `ok=false` → 返回错误 `fmt.Errorf("evoSender: no instance for jid %s", jid)`；`err!=nil` → 传播。
  - 否则 `api.SendText(ctx, instanceName, phone, body)` → 返回 `remoteID`（作 message_id）。

- [ ] **Step 1: 写失败测试（fake api + route）**

`internal/dispatch/sender_evolution_test.go`：
```go
package dispatch

import (
	"context"
	"errors"
	"testing"
)

type fakeEvoSendAPI struct {
	lastInstance, lastPhone, lastBody string
	ret                               string
	err                               error
}

func (f *fakeEvoSendAPI) SendText(_ context.Context, inst, phone, body string) (string, error) {
	f.lastInstance, f.lastPhone, f.lastBody = inst, phone, body
	return f.ret, f.err
}

func TestEvoSender_TextRoutesAndReturnsRemoteID(t *testing.T) {
	api := &fakeEvoSendAPI{ret: "WAMID9"}
	route := func(_ context.Context, jid string) (string, bool, error) {
		if jid != "j1" {
			t.Fatalf("jid=%s", jid)
		}
		return "wa_inst_1", true, nil
	}
	s := NewEvoSender(api, route)
	id, err := s.Send(context.Background(), "j1", "15551234", "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "WAMID9" {
		t.Fatalf("id=%q", id)
	}
	if api.lastInstance != "wa_inst_1" || api.lastPhone != "15551234" || api.lastBody != "hi" {
		t.Fatalf("api got %+v", api)
	}
}

func TestEvoSender_MediaUnsupported(t *testing.T) {
	s := NewEvoSender(&fakeEvoSendAPI{}, func(_ context.Context, _ string) (string, bool, error) {
		return "wa", true, nil
	})
	_, err := s.Send(context.Background(), "j1", "1555", "hi", &MediaHandle{URL: "x"})
	if !errors.Is(err, ErrEvoMediaUnsupported) {
		t.Fatalf("want ErrEvoMediaUnsupported, got %v", err)
	}
}

func TestEvoSender_NoInstance(t *testing.T) {
	s := NewEvoSender(&fakeEvoSendAPI{}, func(_ context.Context, _ string) (string, bool, error) {
		return "", false, nil
	})
	if _, err := s.Send(context.Background(), "jX", "1555", "hi", nil); err == nil {
		t.Fatal("expected error when no instance for jid")
	}
}

func TestEvoSender_RouteError(t *testing.T) {
	routeErr := errors.New("boom")
	s := NewEvoSender(&fakeEvoSendAPI{}, func(_ context.Context, _ string) (string, bool, error) {
		return "", false, routeErr
	})
	if _, err := s.Send(context.Background(), "jX", "1555", "hi", nil); !errors.Is(err, routeErr) {
		t.Fatalf("want routeErr, got %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dispatch/ -run TestEvoSender -v`
Expected: FAIL（未定义）

- [ ] **Step 3: 实现**

`internal/dispatch/sender_evolution.go`：
```go
package dispatch

import (
	"context"
	"errors"
	"fmt"
)

// ErrEvoMediaUnsupported is returned by evoSender for media sends: whatsmeow's
// MediaHandle (an encrypted WA-CDN handle) is meaningless to Evolution/Baileys,
// so media awaits the Uploader redesign in a later module.
var ErrEvoMediaUnsupported = errors.New("dispatch: evolution media send not yet supported")

// evoSendAPI is the slice of the Evolution client evoSender needs. Defined here
// (not imported from cluster) because cluster already imports dispatch — the
// production wiring supplies an adapter over *cluster.EvoClient.SendText that
// unwraps its SendResult to the returned remoteID.
type evoSendAPI interface {
	SendText(ctx context.Context, instanceName, toPhone, body string) (remoteID string, err error)
}

// InstanceRoute maps an account JID to its Evolution instance name (production:
// store.Manager.InstanceForJID). ok=false means no instance is bound yet.
type InstanceRoute func(ctx context.Context, jid string) (instanceName string, ok bool, err error)

// evoSender implements dispatch.Sender over Evolution's REST API. It routes a
// jid to its instance and returns the WA key.id as the message id (receipt
// symmetry). DORMANT: not wired into the live SendWorker in E2.
type evoSender struct {
	api   evoSendAPI
	route InstanceRoute
}

func NewEvoSender(api evoSendAPI, route InstanceRoute) *evoSender {
	return &evoSender{api: api, route: route}
}

var _ Sender = (*evoSender)(nil)

func (s *evoSender) Send(ctx context.Context, jid, phone, body string, media *MediaHandle) (string, error) {
	if media != nil {
		return "", ErrEvoMediaUnsupported
	}
	inst, ok, err := s.route(ctx, jid)
	if err != nil {
		return "", fmt.Errorf("evoSender: route jid %s: %w", jid, err)
	}
	if !ok {
		return "", fmt.Errorf("evoSender: no instance for jid %s", jid)
	}
	return s.api.SendText(ctx, inst, phone, body)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dispatch/ -run TestEvoSender -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/sender_evolution.go internal/dispatch/sender_evolution_test.go
git commit -m "feat(dispatch): evoSender implements Sender (text; media fail-fast)"
```

---

## Task 3: `retrySender` 指数退避装饰器

**Files:**
- Create: `internal/dispatch/sender_retry.go`
- Test: `internal/dispatch/sender_retry_test.go`

**Interfaces:**
- Consumes: `dispatch.Sender`。
- Produces:
  - `type retrySender struct { inner Sender; maxAttempts int; baseDelay, maxDelay time.Duration; permanent func(error) bool; sleep func(time.Duration) }`
  - `func NewRetrySender(inner Sender, maxAttempts int, baseDelay, maxDelay time.Duration) *retrySender` —— `permanent` 默认 nil（一切错误都重试）；`sleep` 默认 `time.Sleep`。
  - `func (r *retrySender) WithPermanent(fn func(error) bool) *retrySender` —— 注入「永久错误」判定（返回 true=不重试）。E4 会传真分类器（loggedOut/400 not-connected 等）。
  - `func (r *retrySender) withSleep(fn func(time.Duration)) *retrySender` —— 仅测试注入（不实际 sleep）。
  - `var _ Sender = (*retrySender)(nil)`
- 行为 `Send`：成功即返回；`permanent(err)==true` 立即返回该 err；否则退避（`baseDelay` 起指数×2 钳 `maxDelay`）后重试，达 `maxAttempts` 或 `ctx` 取消则返回最后一次 err。**E2 无 jitter/分类/governor**（那些属 E4，注释标明）。

- [ ] **Step 1: 写失败测试**

`internal/dispatch/sender_retry_test.go`：
```go
package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

type scriptedSender struct {
	calls   int
	failN   int   // fail the first failN calls, then succeed
	err     error
	retID   string
}

func (s *scriptedSender) Send(_ context.Context, _, _, _ string, _ *MediaHandle) (string, error) {
	s.calls++
	if s.calls <= s.failN {
		return "", s.err
	}
	return s.retID, nil
}

func TestRetrySender_RetriesThenSucceeds(t *testing.T) {
	inner := &scriptedSender{failN: 2, err: errors.New("transient"), retID: "OK"}
	var slept int
	r := NewRetrySender(inner, 5, time.Millisecond, 10*time.Millisecond).
		withSleep(func(time.Duration) { slept++ })
	id, err := r.Send(context.Background(), "j", "1", "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "OK" {
		t.Fatalf("id=%q", id)
	}
	if inner.calls != 3 {
		t.Fatalf("inner calls=%d want 3", inner.calls)
	}
	if slept != 2 {
		t.Fatalf("slept=%d want 2", slept)
	}
}

func TestRetrySender_PermanentNoRetry(t *testing.T) {
	perm := errors.New("loggedOut")
	inner := &scriptedSender{failN: 99, err: perm}
	r := NewRetrySender(inner, 5, time.Millisecond, time.Millisecond).
		WithPermanent(func(e error) bool { return errors.Is(e, perm) }).
		withSleep(func(time.Duration) {})
	_, err := r.Send(context.Background(), "j", "1", "b", nil)
	if !errors.Is(err, perm) {
		t.Fatalf("want perm err, got %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("permanent must not retry: calls=%d", inner.calls)
	}
}

func TestRetrySender_ExhaustsAttempts(t *testing.T) {
	inner := &scriptedSender{failN: 99, err: errors.New("transient")}
	r := NewRetrySender(inner, 3, time.Millisecond, time.Millisecond).
		withSleep(func(time.Duration) {})
	if _, err := r.Send(context.Background(), "j", "1", "b", nil); err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if inner.calls != 3 {
		t.Fatalf("calls=%d want 3", inner.calls)
	}
}

func TestRetrySender_CtxCancelStops(t *testing.T) {
	inner := &scriptedSender{failN: 99, err: errors.New("transient")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewRetrySender(inner, 5, time.Millisecond, time.Millisecond).
		withSleep(func(time.Duration) {})
	if _, err := r.Send(ctx, "j", "1", "b", nil); err == nil {
		t.Fatal("expected error under cancelled ctx")
	}
	if inner.calls != 1 {
		t.Fatalf("cancelled ctx should stop after first attempt: calls=%d", inner.calls)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dispatch/ -run TestRetrySender -v`
Expected: FAIL（未定义）

- [ ] **Step 3: 实现**

`internal/dispatch/sender_retry.go`：
```go
package dispatch

import (
	"context"
	"time"
)

// retrySender wraps a Sender with exponential backoff. E2 keeps it minimal:
// no jitter, no status classification, no governor coupling — those belong to
// E4 (backpressure/anti-ban). A permanent-error predicate (injected) short-
// circuits retries; the default retries every error up to maxAttempts.
type retrySender struct {
	inner       Sender
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
	permanent   func(error) bool
	sleep       func(time.Duration)
}

func NewRetrySender(inner Sender, maxAttempts int, baseDelay, maxDelay time.Duration) *retrySender {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	return &retrySender{
		inner:       inner,
		maxAttempts: maxAttempts,
		baseDelay:   baseDelay,
		maxDelay:    maxDelay,
		sleep:       time.Sleep,
	}
}

// WithPermanent injects the predicate that classifies an error as permanent
// (true = do not retry). E4 supplies the real classifier (loggedOut, 400
// not-connected, …).
func (r *retrySender) WithPermanent(fn func(error) bool) *retrySender {
	r.permanent = fn
	return r
}

// withSleep overrides the sleep function (tests inject a no-op recorder).
func (r *retrySender) withSleep(fn func(time.Duration)) *retrySender {
	r.sleep = fn
	return r
}

var _ Sender = (*retrySender)(nil)

func (r *retrySender) Send(ctx context.Context, jid, phone, body string, media *MediaHandle) (string, error) {
	delay := r.baseDelay
	var lastErr error
	for attempt := 0; attempt < r.maxAttempts; attempt++ {
		id, err := r.inner.Send(ctx, jid, phone, body, media)
		if err == nil {
			return id, nil
		}
		lastErr = err
		if r.permanent != nil && r.permanent(err) {
			return "", err
		}
		if attempt == r.maxAttempts-1 || ctx.Err() != nil {
			break
		}
		r.sleep(delay)
		if delay = delay * 2; delay > r.maxDelay {
			delay = r.maxDelay
		}
	}
	return "", lastErr
}
```

- [ ] **Step 4: 跑测试确认通过 + 全包编译**

Run: `go test ./internal/dispatch/ -run "TestRetrySender|TestEvoSender" -v && go build ./... && gofmt -l internal/dispatch/sender_retry.go internal/dispatch/sender_retry_test.go internal/dispatch/sender_evolution.go internal/dispatch/sender_evolution_test.go`
Expected: PASS + 编译通过 + gofmt 空

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/sender_retry.go internal/dispatch/sender_retry_test.go
git commit -m "feat(dispatch): retrySender exponential-backoff decorator (dormant)"
```

---

## E2 收尾：门禁 + 零回归自检

- [ ] **门禁**

Run: `make gate`
Expected: `tidy vet test-race labels` 绿；`vuln` 仅 `GO-2026-5856`。

- [ ] **零回归自检**

Run: `grep -rn "NewEvoSender\|evoSender\|retrySender\|SendText" internal/ cmd/ --include=*.go | grep -v _test.go | grep -vE "internal/dispatch/sender_|internal/cluster/evolution_client.go"`
Expected: **空**——E2 新符号在其定义文件外无调用者（SendWorker/cmd 未接线），证明零行为变更，whatsmeow `RoutingSender` 仍唯一活跃 `Sender`。

---

## Self-Review（对 spec §7 E2）

- **evoSender 实现 dispatch.Sender（sendText，返回 key.id）** → Task 1（SendText 回 key.id）+ Task 2（evoSender）。✅
- **retrySender 退避装饰器** → Task 3（E2 最小版：指数退避；jitter/分类/governor 明确留 E4）。✅
- **门控 WADIST_SENDER** → **本模块不接线**（保持休眠零行为变更，与 E0/E1 一致）；live 选择 + `WADIST_SENDER` 生效留待集成模块（spec §7 E6 cutover 或独立集成 task），收尾自检证明零调用者。
- **回执对称（§8b）** → `SendText` 回传 `key.id`，`evoSender.Send` 以它作 msgID。✅
- **媒体** → whatsmeow `MediaHandle` 对 Evolution 无意义，`media!=nil` 显式 `ErrEvoMediaUnsupported`，媒体路径待 Uploader 重设计模块（spec §1 appendix「MediaHandle 退化为 URL/base64」）。✅（明确记为延后，非遗漏。）
- **无 import 环** → `evoSender`/`retrySender` 全在 `internal/dispatch`，只依赖包内接口/回调，不 import cluster。✅
- **占位符扫描**：无 TBD；每步含实际代码/命令/期望。
- **类型一致性**：`evoSendAPI.SendText(...)(string,error)` 与生产适配层从 `cluster.EvoClient.SendText(...)(SendResult,error)` 取 `.RemoteID` 对齐；`Sender` 签名对 types.go；`InstanceRoute` 与 `store.Manager.InstanceForJID(...)(string,bool,error)` 对齐。
- **待钉点**：`SendText` path/字段标 `TODO(evo-verify)`，真机回环在换栈落地前单独验证（spec §9①②）。

---

**下一步**：E3（webhook 升级：`connection.update`→state+jid 回填+offline health signal、`messages.update`→ack 翻译喂 `receipt.Recorder`、幂等+HMAC）。E2 合并后 just-in-time 写。
