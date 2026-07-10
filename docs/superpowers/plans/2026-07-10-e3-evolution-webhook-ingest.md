# E3 Evolution Webhook 入库（receipt + 实例状态）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 E0 的 log-only Evolution webhook 升级为功能性入库：`messages.update`→ack 翻译→喂 `receipt.Recorder`（键 message_id+assigned_jid，幂等）；`connection.update`→`SetInstanceState` + `BindInstanceJID`（首见 jid 回填）+（可选）close→health signal。全程 HMAC 验签。

**Architecture:** webhook 挂在 api server（cmd/console）上。handler 通过**包内三个协作接口**（`receiptSink`/`instanceStore`/`healthSink`）与真实实现解耦——可用 fake 单测、无需 DB。`instanceStore` 由现成的 `*store.Manager`（E0 的 `JIDForInstance`/`BindInstanceJID`/`SetInstanceState`）满足；`receiptSink` 由 `*receipt.Recorder` 满足；`healthSink`（sendgate）是 worker 侧概念，E3 做成**可选 nil-safe**，console 接线传 nil，真 sendgate 接线留 E4。ack 语义与事件名字段属真机待钉（spec §9①）。

**Tech Stack:** Go、gin、`internal/receipt`、`internal/store`（instance 解析）、crypto/hmac、httptest（gin 单测，fake 协作者）。

## Global Constraints

逐字遵守：

- **whatsmeow 路径完全不动**：E3 只碰 Evolution webhook + 其 console 接线。不改 `conn_whatsmeow.go`、dispatch 发送路径、cluster。
- **实践上仍 inert，但功能性**：E3 后 webhook 会真写 `account_instances`（state/jid）与 Evolution-attributed `campaign_recipients`（回执）。但 evoSender 尚未接 SendWorker（E2 休眠），故**没有 Evolution message_id 进过 campaign_recipients**，回执 UPDATE 命中 0 行——安全。
- **正确性不变式**（spec §3）：回执走 `receipt.Recorder.Record` 原样复用（`message_id + assigned_jid` 匹配、`COALESCE` 幂等、read 回填 delivered）——**不改 receipt 包**。message_id = Evolution `key.id`（E2 已保证发送侧对称）。
- **HMAC 验签保留**：E0 的 `verify` 不变；bad sig → 401；空 secret（dev）→ 放行。
- **幂等**：Evolution 会重投；靠 `receipt.Record` 的 `COALESCE` 兜底 + `SetInstanceState`/`BindInstanceJID` 本身幂等。
- **健康信号可选**：`healthSink` 为 nil 时跳过（console E3 传 nil；E4 接真 sendgate）。
- **字段实测待钉**：webhook 事件名 / `data` 字段 / ack 数值语义以真机为准，源码标 `// TODO(evo-verify)`；单测断言的是「我们对自定义 payload 的解析」（spec §9①）。
- **`make gate` 全程绿**（除预存在 `GO-2026-5856`）；gofmt 干净；提交带 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` trailer。

**执行前置**：从 `main`（现 `8c96dca`）新建分支 `feat/evolution-e3-webhook`，先确认基线编译。

---

## Task 1: 信封类型 + `translateAck`（纯解析）

**Files:**
- Create: `internal/api/webhook_evolution_types.go`
- Test: `internal/api/webhook_evolution_types_test.go`

**Interfaces:**
- Produces:
  - `type evoWebhook struct { Event string; Instance string; Data evoWebhookData }`（json tags: `event`/`instance`/`data`）
  - `type evoWebhookData struct { Key *evoKey; Status string; Ack *int; State string; RemoteJID string }`（json: `key`/`status`/`ack`/`state`/`remoteJid`）
  - `type evoKey struct { ID string; FromMe bool; RemoteJID string }`（json: `id`/`fromMe`/`remoteJid`）
  - `func normalizeEvent(s string) string` —— 归一化事件名：小写 + `_`→`.`，使 `"CONNECTION_UPDATE"` 与 `"connection.update"` 等价。
  - `func translateAck(status string, ack *int) (receipt.Kind, bool)` —— Baileys ack `>=4`→Read、`==3`→Delivered、其它数值→`("",false)`（ack 2=server=已发，回执侧跳过）；ack 为 nil 时按 status：`READ`/`PLAYED`→Read、`DELIVERY_ACK`/`DELIVERED`→Delivered、其它→`("",false)`。

- [ ] **Step 1: 写失败测试**

`internal/api/webhook_evolution_types_test.go`：
```go
package api

import (
	"testing"

	"github.com/acme/wadist/internal/receipt"
)

func TestTranslateAck(t *testing.T) {
	i := func(n int) *int { return &n }
	cases := []struct {
		name   string
		status string
		ack    *int
		want   receipt.Kind
		ok     bool
	}{
		{"ack4 read", "", i(4), receipt.Read, true},
		{"ack3 delivered", "", i(3), receipt.Delivered, true},
		{"ack2 server skip", "", i(2), "", false},
		{"status READ", "READ", nil, receipt.Read, true},
		{"status DELIVERY_ACK", "DELIVERY_ACK", nil, receipt.Delivered, true},
		{"status SERVER_ACK skip", "SERVER_ACK", nil, "", false},
		{"empty both", "", nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, ok := translateAck(c.status, c.ack)
			if ok != c.ok || k != c.want {
				t.Fatalf("translateAck(%q,%v)=(%q,%v) want (%q,%v)", c.status, c.ack, k, ok, c.want, c.ok)
			}
		})
	}
}

func TestNormalizeEvent(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"CONNECTION_UPDATE", "connection.update"},
		{"connection.update", "connection.update"},
		{"MESSAGES_UPDATE", "messages.update"},
	} {
		if got := normalizeEvent(c.in); got != c.want {
			t.Fatalf("normalizeEvent(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/ -run "TestTranslateAck|TestNormalizeEvent" -v`
Expected: FAIL（未定义）

- [ ] **Step 3: 实现**

`internal/api/webhook_evolution_types.go`：
```go
package api

import (
	"strings"

	"github.com/acme/wadist/internal/receipt"
)

// evoWebhook is Evolution's callback envelope (subset we consume).
// TODO(evo-verify): confirm event names + data fields against real Evolution v2.
type evoWebhook struct {
	Event    string         `json:"event"`
	Instance string         `json:"instance"`
	Data     evoWebhookData `json:"data"`
}

type evoWebhookData struct {
	Key       *evoKey `json:"key,omitempty"`
	Status    string  `json:"status,omitempty"`
	Ack       *int    `json:"ack,omitempty"`
	State     string  `json:"state,omitempty"`
	RemoteJID string  `json:"remoteJid,omitempty"`
}

type evoKey struct {
	ID        string `json:"id"`
	FromMe    bool   `json:"fromMe"`
	RemoteJID string `json:"remoteJid"`
}

// normalizeEvent folds Evolution's two event-name spellings
// ("CONNECTION_UPDATE" vs "connection.update") into one canonical dotted form.
func normalizeEvent(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "_", ".")
}

// translateAck maps Baileys/Evolution ack semantics to a receipt.Kind. Baileys
// numeric ack: 2=server(sent), 3=delivered(device), 4=read. Status strings vary
// by version, so the numeric ack wins when present.
// TODO(evo-verify): confirm ack numbers + status strings against real Evolution v2.
func translateAck(status string, ack *int) (receipt.Kind, bool) {
	if ack != nil {
		switch {
		case *ack >= 4:
			return receipt.Read, true
		case *ack == 3:
			return receipt.Delivered, true
		}
		return "", false // ack 2 (server) = "sent", recorded at send time
	}
	switch status {
	case "READ", "PLAYED":
		return receipt.Read, true
	case "DELIVERY_ACK", "DELIVERED":
		return receipt.Delivered, true
	}
	return "", false
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api/ -run "TestTranslateAck|TestNormalizeEvent" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/api/webhook_evolution_types.go internal/api/webhook_evolution_types_test.go
git commit -m "feat(api): evolution webhook envelope types + ack/event translation"
```

---

## Task 2: handler 升级 — 协作接口 + 事件分派

**Files:**
- Modify: `internal/api/webhook_evolution.go`
- Test: `internal/api/webhook_evolution_test.go`（追加；E0 的 HMAC 测试保留）

**Interfaces:**
- Consumes: Task 1 的类型/函数、`receipt.Event`/`receipt.Kind`。
- Produces（协作接口，定义在 `webhook_evolution.go`）：
  - `type receiptSink interface { Record(ctx context.Context, ev receipt.Event) error }`（`*receipt.Recorder` 满足）
  - `type instanceStore interface { JIDForInstance(ctx context.Context, instanceName string) (string, bool, error); BindInstanceJID(ctx context.Context, instanceName, jid string) error; SetInstanceState(ctx context.Context, instanceName, state string) error }`（`*store.Manager` 满足）
  - `type healthSink interface { ApplyHealthSignal(ctx context.Context, jid, signal string, cooloff time.Duration) error }`（`*sendgate.SendGate` 满足；E3 传 nil）
  - 改造 `EvolutionWebhook` 持有 `secret string; rec receiptSink; inst instanceStore; health healthSink`。
  - 改 `NewEvolutionWebhook(secret string, rec receiptSink, inst instanceStore, health healthSink) *EvolutionWebhook`。
- 行为 `handle`（验签后）：解析 `evoWebhook`（解析失败 → 200 停重投）；按 `normalizeEvent(w.Event)` 分派：
  - `connection.update`：`w.Data.RemoteJID != ""` → `inst.BindInstanceJID(instance, remoteJid)`；`inst.SetInstanceState(instance, w.Data.State)`；`w.Data.State == "close"` 且 `health != nil` → 解析 jid（优先 remoteJid，否则 `inst.JIDForInstance`）→ `health.ApplyHealthSignal(jid, "offline", 5*time.Minute)`。
  - `messages.update`：`translateAck` 得 `(kind, ok)`；`ok && w.Data.Key != nil && w.Data.Key.FromMe` → `inst.JIDForInstance(instance)` 得 jid → `rec.Record(receipt.Event{MessageIDs:[key.id], SenderJID:jid, Kind:kind, At: /* 见下 */})`。
  - 其它事件：忽略。
  - 始终 `200`（已受理即停重投）。
  - **时间戳**：E3 用 `time.Now()`（Evolution ts 可选；`Record` 用 `COALESCE` 保最早）。为可测，注入一个 `now func() time.Time`（默认 `time.Now`，测试可覆盖；或直接用 `time.Now()` 并在测试里不断言具体时刻）。**采用后者**：直接 `time.Now()`，测试只断言 Record 被调且 MessageIDs/SenderJID/Kind 正确，不断言 At。

- [ ] **Step 1: 写失败测试（fake 协作者）**

`internal/api/webhook_evolution_test.go` 追加（`sign` 复用 E0 已定义的同文件 helper）：
```go
type fakeReceipt struct{ evs []receipt.Event }

func (f *fakeReceipt) Record(_ context.Context, ev receipt.Event) error {
	f.evs = append(f.evs, ev)
	return nil
}

type fakeInst struct {
	jidByInst map[string]string
	bound     map[string]string
	states    map[string]string
}

func newFakeInst() *fakeInst {
	return &fakeInst{jidByInst: map[string]string{}, bound: map[string]string{}, states: map[string]string{}}
}
func (f *fakeInst) JIDForInstance(_ context.Context, inst string) (string, bool, error) {
	j, ok := f.jidByInst[inst]
	return j, ok, nil
}
func (f *fakeInst) BindInstanceJID(_ context.Context, inst, jid string) error {
	f.bound[inst] = jid
	f.jidByInst[inst] = jid
	return nil
}
func (f *fakeInst) SetInstanceState(_ context.Context, inst, state string) error {
	f.states[inst] = state
	return nil
}

type fakeHealth struct{ calls []string }

func (f *fakeHealth) ApplyHealthSignal(_ context.Context, jid, signal string, _ time.Duration) error {
	f.calls = append(f.calls, jid+":"+signal)
	return nil
}

func postWebhook(t *testing.T, h *EvolutionWebhook, body []byte) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.Register(r)
	req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestWebhook_ConnectionUpdate_BindsJIDAndState(t *testing.T) {
	inst := newFakeInst()
	h := NewEvolutionWebhook("", &fakeReceipt{}, inst, nil)
	body := []byte(`{"event":"connection.update","instance":"wa_1","data":{"state":"open","remoteJid":"123@s.whatsapp.net"}}`)
	if code := postWebhook(t, h, body); code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	if inst.bound["wa_1"] != "123@s.whatsapp.net" {
		t.Fatalf("jid not bound: %+v", inst.bound)
	}
	if inst.states["wa_1"] != "open" {
		t.Fatalf("state=%q", inst.states["wa_1"])
	}
}

func TestWebhook_ConnectionClose_FiresHealthSignal(t *testing.T) {
	inst := newFakeInst()
	inst.jidByInst["wa_1"] = "123@s.whatsapp.net"
	health := &fakeHealth{}
	h := NewEvolutionWebhook("", &fakeReceipt{}, inst, health)
	body := []byte(`{"event":"connection.update","instance":"wa_1","data":{"state":"close"}}`)
	postWebhook(t, h, body)
	if len(health.calls) != 1 || health.calls[0] != "123@s.whatsapp.net:offline" {
		t.Fatalf("health calls=%v", health.calls)
	}
}

func TestWebhook_MessagesUpdate_RecordsReadReceipt(t *testing.T) {
	inst := newFakeInst()
	inst.jidByInst["wa_1"] = "123@s.whatsapp.net"
	rec := &fakeReceipt{}
	h := NewEvolutionWebhook("", rec, inst, nil)
	body := []byte(`{"event":"messages.update","instance":"wa_1","data":{"ack":4,"key":{"id":"WAMID7","fromMe":true}}}`)
	postWebhook(t, h, body)
	if len(rec.evs) != 1 {
		t.Fatalf("recorded %d events", len(rec.evs))
	}
	ev := rec.evs[0]
	if ev.Kind != receipt.Read || ev.SenderJID != "123@s.whatsapp.net" ||
		len(ev.MessageIDs) != 1 || ev.MessageIDs[0] != "WAMID7" {
		t.Fatalf("event=%+v", ev)
	}
}

func TestWebhook_MessagesUpdate_UnknownInstanceNoRecord(t *testing.T) {
	rec := &fakeReceipt{}
	h := NewEvolutionWebhook("", rec, newFakeInst(), nil)
	body := []byte(`{"event":"messages.update","instance":"ghost","data":{"ack":4,"key":{"id":"X","fromMe":true}}}`)
	postWebhook(t, h, body)
	if len(rec.evs) != 0 {
		t.Fatalf("must not record for unknown instance: %+v", rec.evs)
	}
}

func TestWebhook_MessagesUpdate_NotFromMeSkipped(t *testing.T) {
	inst := newFakeInst()
	inst.jidByInst["wa_1"] = "j"
	rec := &fakeReceipt{}
	h := NewEvolutionWebhook("", rec, inst, nil)
	body := []byte(`{"event":"messages.update","instance":"wa_1","data":{"ack":4,"key":{"id":"X","fromMe":false}}}`)
	postWebhook(t, h, body)
	if len(rec.evs) != 0 {
		t.Fatalf("inbound (not fromMe) must be skipped: %+v", rec.evs)
	}
}
```
测试 import 需要：`bytes`、`context`、`net/http`、`net/http/httptest`、`time`、`testing`、`github.com/gin-gonic/gin`、`github.com/acme/wadist/internal/receipt`。E0 的 `TestEvolutionWebhook_HMAC` 与 `sign` 保留——但注意 E0 的 `NewEvolutionWebhook("s3cr3t")` 调用点签名变了，需在本任务同步改（见 Step 3 末）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/ -run TestWebhook_ -v`
Expected: FAIL（新签名/字段未定义、编译错）

- [ ] **Step 3: 实现 handler 升级**

重写 `internal/api/webhook_evolution.go` 为下面这版（协作接口的 ctx 一律用 `context.Context`）：
```go
package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/receipt"
)

// receiptSink records delivery/read milestones (satisfied by *receipt.Recorder).
type receiptSink interface {
	Record(ctx context.Context, ev receipt.Event) error
}

// instanceStore resolves + mutates account_instances routing rows
// (satisfied by *store.Manager).
type instanceStore interface {
	JIDForInstance(ctx context.Context, instanceName string) (string, bool, error)
	BindInstanceJID(ctx context.Context, instanceName, jid string) error
	SetInstanceState(ctx context.Context, instanceName, state string) error
}

// healthSink feeds account health signals (satisfied by *sendgate.SendGate).
// Optional: nil skips the health path (E3 wires nil; E4 supplies sendgate).
type healthSink interface {
	ApplyHealthSignal(ctx context.Context, jid, signal string, cooloff time.Duration) error
}

// EvolutionWebhook receives Evolution API callbacks: HMAC-verifies, then feeds
// receipts (messages.update) and instance state (connection.update). It never
// mutates the whatsmeow path.
type EvolutionWebhook struct {
	secret string
	rec    receiptSink
	inst   instanceStore
	health healthSink
}

func NewEvolutionWebhook(secret string, rec receiptSink, inst instanceStore, health healthSink) *EvolutionWebhook {
	return &EvolutionWebhook{secret: secret, rec: rec, inst: inst, health: health}
}

func (h *EvolutionWebhook) Register(r gin.IRouter) {
	r.POST("/webhook/evolution", h.handle)
}

func (h *EvolutionWebhook) handle(c *gin.Context) {
	body, _ := c.GetRawData()
	if !h.verify(c.GetHeader("X-Evolution-Signature"), body) {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	var w evoWebhook
	if err := json.Unmarshal(body, &w); err != nil {
		c.Status(http.StatusOK) // stop Evolution retrying unparseable payloads
		return
	}
	ctx := c.Request.Context()
	switch normalizeEvent(w.Event) {
	case "connection.update":
		if w.Data.RemoteJID != "" {
			_ = h.inst.BindInstanceJID(ctx, w.Instance, w.Data.RemoteJID)
		}
		if w.Data.State != "" {
			_ = h.inst.SetInstanceState(ctx, w.Instance, w.Data.State)
		}
		if w.Data.State == "close" && h.health != nil {
			if jid := h.resolveJID(ctx, w); jid != "" {
				_ = h.health.ApplyHealthSignal(ctx, jid, "offline", 5*time.Minute)
			}
		}
	case "messages.update":
		kind, ok := translateAck(w.Data.Status, w.Data.Ack)
		if !ok || w.Data.Key == nil || !w.Data.Key.FromMe {
			break
		}
		jid, ok, err := h.inst.JIDForInstance(ctx, w.Instance)
		if err != nil || !ok {
			break
		}
		_ = h.rec.Record(ctx, receipt.Event{
			MessageIDs: []string{w.Data.Key.ID},
			SenderJID:  jid,
			Kind:       kind,
			At:         time.Now(),
		})
	}
	c.Status(http.StatusOK)
}

// resolveJID prefers the payload's remoteJid, else looks it up by instance.
func (h *EvolutionWebhook) resolveJID(ctx context.Context, w evoWebhook) string {
	if w.Data.RemoteJID != "" {
		return w.Data.RemoteJID
	}
	if jid, ok, err := h.inst.JIDForInstance(ctx, w.Instance); err == nil && ok {
		return jid
	}
	return ""
}

func (h *EvolutionWebhook) verify(sig string, body []byte) bool {
	if h.secret == "" { // dev only; prod must set WADIST_EVOLUTION_WEBHOOK_SECRET
		return true
	}
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(body)
	return hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil))))
}
```
同步修 E0 的 HMAC 测试调用点：`internal/api/webhook_evolution_test.go` 里 `NewEvolutionWebhook("s3cr3t")` → `NewEvolutionWebhook("s3cr3t", &fakeReceipt{}, newFakeInst(), nil)`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/api/ -run "TestWebhook_|TestEvolutionWebhook_HMAC|TestTranslateAck" -v`
Expected: PASS（含保留的 HMAC 测试）

- [ ] **Step 5: Commit**

```bash
git add internal/api/webhook_evolution.go internal/api/webhook_evolution_test.go
git commit -m "feat(api): evolution webhook feeds receipt + instance state (collaborator ifaces)"
```

---

## Task 3: 接线（router + cmd/console）

**Files:**
- Modify: `internal/api/router.go`（`Deps` + `Router()` 注册调用）
- Modify: `cmd/console/main.go`（构造 `receipt.Recorder`，填 `Deps`）
- Test: 复用现有 api 测试；本任务以 `go build ./...` + 现有套件为验收（不新增测试——纯接线）。

**Interfaces:**
- Consumes: Task 2 的 `NewEvolutionWebhook(secret, rec, inst, health)`。
- Produces: `Deps` 新增 `Receipt *receipt.Recorder`（api 导入 receipt）。`Router()` 注册改为 `NewEvolutionWebhook(s.deps.EvolutionWebhookSecret, s.deps.Receipt, s.deps.Mgr, nil).Register(v1)`（`s.deps.Mgr` 满足 `instanceStore`；health 传 nil）。

- [ ] **Step 1: 改 `Deps` + 注册调用**

`internal/api/router.go`：
- `Deps` struct 追加字段（靠近 `Mgr`）：
```go
	Receipt *receipt.Recorder // delivery/read receipt writer (Evolution webhook)
```
（文件顶部 import 增加 `"github.com/acme/wadist/internal/receipt"`。）
- `Router()` 里 E0 的注册行：
```go
	NewEvolutionWebhook(s.deps.EvolutionWebhookSecret).Register(v1)
```
改为：
```go
	NewEvolutionWebhook(s.deps.EvolutionWebhookSecret, s.deps.Receipt, s.deps.Mgr, nil).Register(v1)
```

- [ ] **Step 2: 改 cmd/console 构造 Receipt**

`cmd/console/main.go` 的 `api.Deps{...}` literal（约 L116-129）追加一行（复用已构造的 `mgr`；import 增加 `"github.com/acme/wadist/internal/receipt"`）：
```go
		Receipt: receipt.New(mgr.SystemPool()),
```

- [ ] **Step 3: 编译 + 现有套件**

Run: `go build ./... && go test ./internal/api/ -run "TestWebhook_|TestEvolutionWebhook_HMAC" -v && gofmt -l internal/api/router.go cmd/console/main.go`
Expected: 编译通过 + PASS + gofmt 空

- [ ] **Step 4: 冒烟——console 构造不 panic**

说明：`s.deps.Receipt` 现由 console 用 `receipt.New(mgr.SystemPool())` 填充（非 nil），生产路径不会因 typed-nil 接口在 `Record` 时 panic。测试路径用 fake，从不经过真实 `*receipt.Recorder`。无需额外测试；`go build` + 现有 api 套件即覆盖接线正确性。

- [ ] **Step 5: Commit**

```bash
git add internal/api/router.go cmd/console/main.go
git commit -m "feat(api): wire evolution webhook to receipt.Recorder + manager (console)"
```

---

## E3 收尾：门禁 + 自检

- [ ] **门禁**

Run: `make gate`
Expected: `tidy vet test-race labels` 绿；`vuln` 仅 `GO-2026-5856`。

- [ ] **whatsmeow 零回归自检**

Run: `grep -rn "whatsmeow" internal/api/ && echo "FOUND (bad)" || echo "clean: api 不碰 whatsmeow"`
Expected: `clean`——E3 只动 Evolution webhook + 接线，whatsmeow 发送/回执路径（`conn_whatsmeow.go` 的 `onReceipt`→`receipt.Recorder`）完全独立、未改。

- [ ] **回执包未改自检**

Run: `git diff main -- internal/receipt/ | head`
Expected: **空**——`receipt` 包一字未改，E3 只是新增一个喂它的来源（webhook），与 whatsmeow 的事件 handler 并列。

---

## Self-Review（对 spec §7 E3）

- **connection.update → state + jid 回填 + offline health signal** → Task 2（BindInstanceJID/SetInstanceState/close→health）。✅（health 可选，console 传 nil，真 sendgate 接线 E4。）
- **messages.update → ack 翻译 → receipt.Record** → Task 1（translateAck）+ Task 2（分派）。✅
- **HMAC 校验** → 保留 E0 `verify`。✅
- **幂等** → `receipt.Record` COALESCE + SetInstanceState/BindInstanceJID 幂等 + 重投停于 200。✅
- **instance→assigned_jid 翻译** → `instanceStore.JIDForInstance`（E0 现成），`receipt.Event.SenderJID` = 该 jid，匹配 `campaign_recipients.assigned_jid`。✅
- **不改 receipt 包 / 不碰 whatsmeow** → 收尾自检两条。✅
- **占位符扫描**：无 TBD；每步含实际代码/命令/期望。
- **类型一致性**：`receiptSink.Record`/`instanceStore.*`/`healthSink.ApplyHealthSignal` 与 `*receipt.Recorder`/`*store.Manager`/`*sendgate.SendGate` 的真实签名逐一对齐；`Deps.Receipt *receipt.Recorder` 满足 `receiptSink`；`Deps.Mgr *store.Manager` 满足 `instanceStore`。
- **待钉点**：事件名/字段/ack 语义标 `TODO(evo-verify)`，真机回环单独验证（spec §9①）。

---

**下一步**：E4（背压/反封重建：per-instance 信号量、错误分类 429/503→governor 乘性减、loggedOut→permanent、per-instance 熔断；把 E2 的 `retrySender.WithPermanent` 接真分类器、把本 E3 的 `healthSink` 接真 `sendgate.SendGate`）。E3 合并后 just-in-time 写。
