# E1 Evolution 实例生命周期 + 拟人化 presence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `internal/cluster` 落地 Evolution 实例生命周期（create 带代理粘性 / connect+QR / logout / delete）与拟人化 presence（setPresence / typing）的 REST 方法，并用它们组装一个实现 `cluster.Conn`/`PresenceConn`/`LivenessConn` 的 `evoInstance`——**全部休眠**（不接入 live orchestrator，默认仍 whatsmeow），零行为变更。

**Architecture:** 在 E0 的 `EvoClient` 上加实例/presence REST 方法（httptest 测：断言我们的客户端发出的 method+path+body，真机字段名对齐属 spec §9「实测待钉」，不阻塞本地测试）。新增纯函数把 `store.ProxyBinding.ProxyURL` 拆成 Evolution 代理对象。新增 `evoInstance` 用一个最小 `evoAPI` 接口（EvoClient 满足之）+ instance_name + 本地 atomic 状态实现三个 cluster 能力接口；Connect=create(幂等,带代理)→connect，Disconnect=停本地不 logout，Liveness 读本地缓存状态（E3 webhook 回填）。无任何 live caller。

**Tech Stack:** Go、net/http + net/url、httptest、`internal/cluster` 能力接口、`internal/store.ProxyBinding`。

## Global Constraints

逐字遵守：

- **零行为变更**：E1 新符号无 live 调用者（whatsmeow 仍唯一活跃数据面）。不改 `conn_whatsmeow.go`、不改 orchestrator 工厂、不删 whatsmeow。
- **代理终身粘性**（spec §8c）：`CreateInstance` 传一次代理；重建实例只在代理死亡时发生（本模块只提供能力，粘性策略由后续控制面调用方保证）。绝不 per-send 换 IP。
- **能力接口签名不变**（`internal/cluster/types.go`）：`Conn{Connect(ctx) error; Disconnect()}`；`PresenceConn{SetPresence(ctx, available bool) error; SendTyping(ctx, chatPhone string, composing bool) error}`；`LivenessConn{Liveness() Liveness}`，`Liveness{Alive, Permanent bool}`。
- **不引入 whatsmeow 依赖**到任何新文件（`evoInstance` 不 import whatsmeow）。
- **真机字段实测待钉**：Evolution v2 的具体 path/JSON 字段以真实实例为准；本地 httptest 断言的是「我们客户端发出的请求形状」，字段名对齐留待 E1 之后真机回环（spec §9 未决点①②）。每个 REST 方法源码注释标 `// TODO(evo-verify): confirm path/fields against real Evolution v2`。
- **`make gate` 全程绿**（除预存在 `GO-2026-5856`）。`test-race` 需 Docker（本模块新测均纯 httptest/fake，不需 Docker，但全量 gate 的 store 集成测需要）。
- gofmt 干净；提交带 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` trailer。

**执行前置**：从 `main`（现 `b560db7`）新建分支 `feat/evolution-e1-lifecycle`，先确认基线编译。

---

## Task 1: 代理 URL → Evolution 代理对象（纯函数）

**Files:**
- Create: `internal/cluster/evolution_proxy.go`
- Test: `internal/cluster/evolution_proxy_test.go`

**Interfaces:**
- Produces:
  - `type evoProxy struct { Host string; Port int; Protocol, Username, Password string }`
  - `func proxyFromBinding(b *store.ProxyBinding) (*evoProxy, bool)` — nil/空 binding → `(nil, false)`；解析失败 → `(nil, false)`；成功 → `(*evoProxy, true)`。Protocol 取 URL scheme（`socks5`/`http`/`https`），Host/Port/Username/Password 从 URL 取。

- [ ] **Step 1: 写失败测试**

`internal/cluster/evolution_proxy_test.go`：
```go
package cluster

import (
	"testing"

	"github.com/acme/wadist/internal/store"
)

func TestProxyFromBinding(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantOK  bool
		host    string
		port    int
		proto   string
		user    string
		pass    string
	}{
		{"socks5 with auth", "socks5://u1:p1@1.2.3.4:1080", true, "1.2.3.4", 1080, "socks5", "u1", "p1"},
		{"http no auth", "http://10.0.0.1:8080", true, "10.0.0.1", 8080, "http", "", ""},
		{"empty url", "", false, "", 0, "", "", ""},
		{"garbage", "://nope", false, "", 0, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b *store.ProxyBinding
			if c.url != "" || c.name == "empty url" {
				b = &store.ProxyBinding{ProxyURL: c.url}
			}
			got, ok := proxyFromBinding(b)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if got.Host != c.host || got.Port != c.port || got.Protocol != c.proto ||
				got.Username != c.user || got.Password != c.pass {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestProxyFromBinding_Nil(t *testing.T) {
	if _, ok := proxyFromBinding(nil); ok {
		t.Fatal("nil binding must be ok=false")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster/ -run TestProxyFromBinding -v`
Expected: FAIL（`proxyFromBinding`/`evoProxy` 未定义）

- [ ] **Step 3: 实现**

`internal/cluster/evolution_proxy.go`：
```go
package cluster

import (
	"net/url"
	"strconv"

	"github.com/acme/wadist/internal/store"
)

// evoProxy is Evolution's per-instance proxy shape, derived from a
// store.ProxyBinding's URL. Evolution wants the parts split out, whereas
// whatsmeow took the whole URL string.
type evoProxy struct {
	Host     string
	Port     int
	Protocol string
	Username string
	Password string
}

// proxyFromBinding splits a ProxyBinding.ProxyURL into Evolution's proxy parts.
// Returns ok=false for a nil/empty binding or an unparseable URL (host/port
// missing) — the caller then creates the instance without a proxy rather than
// dialing direct-by-accident being masked as success.
func proxyFromBinding(b *store.ProxyBinding) (*evoProxy, bool) {
	if b == nil || b.ProxyURL == "" {
		return nil, false
	}
	u, err := url.Parse(b.ProxyURL)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return nil, false
	}
	portStr := u.Port()
	if portStr == "" {
		return nil, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, false
	}
	p := &evoProxy{
		Host:     u.Hostname(),
		Port:     port,
		Protocol: u.Scheme,
	}
	if u.User != nil {
		p.Username = u.User.Username()
		p.Password, _ = u.User.Password()
	}
	return p, true
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cluster/ -run TestProxyFromBinding -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cluster/evolution_proxy.go internal/cluster/evolution_proxy_test.go
git commit -m "feat(cluster): parse ProxyBinding url into evolution proxy parts"
```

---

## Task 2: `EvoClient.CreateInstance`（带代理 + webhook 配置）

**Files:**
- Modify: `internal/cluster/evolution_client.go`
- Test: `internal/cluster/evolution_client_test.go`（追加）

**Interfaces:**
- Consumes: `proxyFromBinding`（Task 1）、`EvoClient.doJSON`（E0）。
- Produces:
  - `func (c *EvoClient) CreateInstance(ctx context.Context, instanceName string, proxy *store.ProxyBinding, webhookURL string) error`
  - 语义：`POST /instance/create`，body 含 `instanceName`、`integration:"WHATSAPP-BAILEYS"`、（proxy 存在时）proxy 部分、（webhookURL 非空时）webhook 配置。创建幂等由 Evolution 侧按 instanceName 保证；本方法对 2xx 返回 nil，非 2xx 由 `doJSON` 返回 error。

- [ ] **Step 1: 写失败测试（断言我们发出的 body）**

`internal/cluster/evolution_client_test.go` 追加：
```go
func TestEvoClient_CreateInstance_SendsProxyAndWebhook(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instance":{"instanceName":"wa_1"}}`))
	}))
	defer srv.Close()

	c := NewEvoClient(srv.URL, "k")
	b := &store.ProxyBinding{ProxyURL: "socks5://u:p@1.2.3.4:1080", ProxyType: "socks5", Country: "US"}
	if err := c.CreateInstance(context.Background(), "wa_1", b, "https://cp/api/v1/webhook/evolution"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/instance/create" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotBody["instanceName"] != "wa_1" {
		t.Fatalf("instanceName=%v", gotBody["instanceName"])
	}
	if gotBody["proxyHost"] != "1.2.3.4" || gotBody["proxyProtocol"] != "socks5" {
		t.Fatalf("proxy fields wrong: %+v", gotBody)
	}
}

func TestEvoClient_CreateInstance_NoProxyOmitsProxyFields(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	if err := c.CreateInstance(context.Background(), "wa_2", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, present := gotBody["proxyHost"]; present {
		t.Fatalf("proxyHost must be omitted when no proxy: %+v", gotBody)
	}
}
```
（测试需 import `encoding/json` — 若文件已 import 则复用。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster/ -run TestEvoClient_CreateInstance -v`
Expected: FAIL（`CreateInstance` 未定义）

- [ ] **Step 3: 实现**

在 `internal/cluster/evolution_client.go` 追加（import 增加 `github.com/acme/wadist/internal/store`）：
```go
// CreateInstance provisions an Evolution instance bound to a proxy in one call.
// Proxy stickiness: callers invoke this only on first bind or after proxy death
// — never per-send. When webhookURL is non-empty the instance is configured to
// POST callbacks (connection/messages) back to the control plane.
// TODO(evo-verify): confirm path/fields against real Evolution v2.
func (c *EvoClient) CreateInstance(ctx context.Context, instanceName string, proxy *store.ProxyBinding, webhookURL string) error {
	body := map[string]any{
		"instanceName": instanceName,
		"integration":  "WHATSAPP-BAILEYS",
	}
	if p, ok := proxyFromBinding(proxy); ok {
		body["proxyHost"] = p.Host
		body["proxyPort"] = p.Port
		body["proxyProtocol"] = p.Protocol
		if p.Username != "" {
			body["proxyUsername"] = p.Username
			body["proxyPassword"] = p.Password
		}
	}
	if webhookURL != "" {
		body["webhook"] = map[string]any{
			"url":    webhookURL,
			"events": []string{"CONNECTION_UPDATE", "MESSAGES_UPDATE", "QRCODE_UPDATED"},
		}
	}
	return c.doJSON(ctx, http.MethodPost, "/instance/create", body, nil)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cluster/ -run TestEvoClient_CreateInstance -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cluster/evolution_client.go internal/cluster/evolution_client_test.go
git commit -m "feat(cluster): EvoClient.CreateInstance (sticky proxy + webhook config)"
```

---

## Task 3: `EvoClient` connect(QR) / logout / delete

**Files:**
- Modify: `internal/cluster/evolution_client.go`
- Test: `internal/cluster/evolution_client_test.go`（追加）

**Interfaces:**
- Produces:
  - `func (c *EvoClient) ConnectInstance(ctx context.Context, instanceName string) (qrBase64 string, err error)` — `GET /instance/connect/{name}`，解析响应 `base64` 字段（未配对时非空；已连接时空串）。
  - `func (c *EvoClient) LogoutInstance(ctx context.Context, instanceName string) error` — `DELETE /instance/logout/{name}`。
  - `func (c *EvoClient) DeleteInstance(ctx context.Context, instanceName string) error` — `DELETE /instance/delete/{name}`（幂等）。

- [ ] **Step 1: 写失败测试**

追加：
```go
func TestEvoClient_ConnectInstance_ReturnsQR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/instance/connect/wa_1" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"base64":"data:image/png;base64,QRDATA"}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	qr, err := c.ConnectInstance(context.Background(), "wa_1")
	if err != nil {
		t.Fatal(err)
	}
	if qr != "data:image/png;base64,QRDATA" {
		t.Fatalf("qr=%q", qr)
	}
}

func TestEvoClient_LogoutAndDelete(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method=%s", r.Method)
		}
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	if err := c.LogoutInstance(context.Background(), "wa_1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteInstance(context.Background(), "wa_1"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/instance/logout/wa_1" || paths[1] != "/instance/delete/wa_1" {
		t.Fatalf("paths=%v", paths)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster/ -run "TestEvoClient_ConnectInstance|TestEvoClient_LogoutAndDelete" -v`
Expected: FAIL（方法未定义）

- [ ] **Step 3: 实现**

追加：
```go
// ConnectInstance triggers pairing and returns a QR (base64 data URI) when the
// instance is unpaired; returns "" when already connected. QR also arrives via
// the QRCODE_UPDATED webhook.
// TODO(evo-verify): confirm path/fields against real Evolution v2.
func (c *EvoClient) ConnectInstance(ctx context.Context, instanceName string) (string, error) {
	var out struct {
		Base64 string `json:"base64"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/instance/connect/"+instanceName, nil, &out); err != nil {
		return "", err
	}
	return out.Base64, nil
}

// LogoutInstance ends the WA session (device unlinked). Terminal.
// TODO(evo-verify): confirm path against real Evolution v2.
func (c *EvoClient) LogoutInstance(ctx context.Context, instanceName string) error {
	return c.doJSON(ctx, http.MethodDelete, "/instance/logout/"+instanceName, nil, nil)
}

// DeleteInstance removes the instance and its Redis session. Idempotent.
// TODO(evo-verify): confirm path against real Evolution v2.
func (c *EvoClient) DeleteInstance(ctx context.Context, instanceName string) error {
	return c.doJSON(ctx, http.MethodDelete, "/instance/delete/"+instanceName, nil, nil)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cluster/ -run "TestEvoClient_ConnectInstance|TestEvoClient_LogoutAndDelete" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cluster/evolution_client.go internal/cluster/evolution_client_test.go
git commit -m "feat(cluster): EvoClient connect(QR)/logout/delete"
```

---

## Task 4: `EvoClient` presence（setPresence + typing）

**Files:**
- Modify: `internal/cluster/evolution_client.go`
- Test: `internal/cluster/evolution_client_test.go`（追加）

**Interfaces:**
- Produces:
  - `func (c *EvoClient) SetPresence(ctx context.Context, instanceName string, available bool) error` — `POST /instance/setPresence/{name}`，body `{"presence":"available"|"unavailable"}`。
  - `func (c *EvoClient) SendTyping(ctx context.Context, instanceName, toPhone string, composing bool) error` — `POST /chat/sendPresence/{name}`，body `{"number":toPhone,"presence":"composing"|"paused"}`。

- [ ] **Step 1: 写失败测试**

追加：
```go
func TestEvoClient_SetPresence(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	if err := c.SetPresence(context.Background(), "wa_1", true); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/instance/setPresence/wa_1" || gotBody["presence"] != "available" {
		t.Fatalf("path=%q body=%+v", gotPath, gotBody)
	}
}

func TestEvoClient_SendTyping(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewEvoClient(srv.URL, "k")
	if err := c.SendTyping(context.Background(), "wa_1", "15551234", true); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat/sendPresence/wa_1" || gotBody["number"] != "15551234" || gotBody["presence"] != "composing" {
		t.Fatalf("path=%q body=%+v", gotPath, gotBody)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster/ -run "TestEvoClient_SetPresence|TestEvoClient_SendTyping" -v`
Expected: FAIL

- [ ] **Step 3: 实现**

追加：
```go
// SetPresence sets the instance's global online/offline presence.
// TODO(evo-verify): confirm path/fields against real Evolution v2.
func (c *EvoClient) SetPresence(ctx context.Context, instanceName string, available bool) error {
	presence := "unavailable"
	if available {
		presence = "available"
	}
	return c.doJSON(ctx, http.MethodPost, "/instance/setPresence/"+instanceName,
		map[string]any{"presence": presence}, nil)
}

// SendTyping toggles a per-chat typing indicator (anthropomorphic dwell).
// TODO(evo-verify): confirm path/fields against real Evolution v2.
func (c *EvoClient) SendTyping(ctx context.Context, instanceName, toPhone string, composing bool) error {
	presence := "paused"
	if composing {
		presence = "composing"
	}
	return c.doJSON(ctx, http.MethodPost, "/chat/sendPresence/"+instanceName,
		map[string]any{"number": toPhone, "presence": presence}, nil)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cluster/ -run "TestEvoClient_SetPresence|TestEvoClient_SendTyping" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cluster/evolution_client.go internal/cluster/evolution_client_test.go
git commit -m "feat(cluster): EvoClient setPresence + typing (anthropomorphic dwell)"
```

---

## Task 5: `evoInstance` 实现 Conn/PresenceConn/LivenessConn（休眠，fake 测）

**Files:**
- Create: `internal/cluster/conn_evolution.go`
- Test: `internal/cluster/conn_evolution_test.go`

**Interfaces:**
- Consumes: 一个最小 `evoAPI` 接口（`*EvoClient` 结构上已满足其方法集）：
  ```go
  type evoAPI interface {
      CreateInstance(ctx context.Context, instanceName string, proxy *store.ProxyBinding, webhookURL string) error
      ConnectInstance(ctx context.Context, instanceName string) (string, error)
      SetPresence(ctx context.Context, instanceName string, available bool) error
      SendTyping(ctx context.Context, instanceName, toPhone string, composing bool) error
  }
  ```
- Produces:
  - `type evoInstance struct { ... }` implementing `Conn`, `PresenceConn`, `LivenessConn`。
  - `func NewEvoInstance(api evoAPI, instanceName, webhookURL string, proxy *store.ProxyBinding) *evoInstance`。
  - `func (e *evoInstance) UpdateState(s InstanceState)` — E3 webhook 回填状态（`InstanceState` 复用 spec §6 已定义于 `internal/cluster/evolution.go`；本模块若该文件尚未落地，则在本文件内定义 `InstanceState` 常量 `StateCreated/StateQR/StateConnected/StateDisconnected/StateLoggedOut`）。
  - 静态断言：`var _ Conn = (*evoInstance)(nil)`；`var _ PresenceConn = (*evoInstance)(nil)`；`var _ LivenessConn = (*evoInstance)(nil)`。
- 行为：
  - `Connect(ctx)`：`CreateInstance`（幂等,带代理+webhook）→ `ConnectInstance`（触发配对;QR 走 webhook,本方法忽略返回的 QR 串,仅传播 error）。
  - `Disconnect()`：仅置本地状态 `StateDisconnected`,**不** logout/delete（温驻留;生命周期回收由控制面/reaper 决策,与 whatsmeow 路径对称——Disconnect 不 Logout）。
  - `SetPresence`/`SendTyping`：委托 `api`。
  - `Liveness()`：读本地状态 → `Liveness{Alive: state==StateConnected, Permanent: state==StateLoggedOut}`。

- [ ] **Step 1: 写失败测试（fake evoAPI）**

`internal/cluster/conn_evolution_test.go`：
```go
package cluster

import (
	"context"
	"testing"

	"github.com/acme/wadist/internal/store"
)

type fakeEvoAPI struct {
	created, connected     bool
	presence               *bool
	typingCalls            int
	proxySeen              *store.ProxyBinding
	webhookSeen            string
}

func (f *fakeEvoAPI) CreateInstance(_ context.Context, _ string, p *store.ProxyBinding, w string) error {
	f.created = true
	f.proxySeen = p
	f.webhookSeen = w
	return nil
}
func (f *fakeEvoAPI) ConnectInstance(_ context.Context, _ string) (string, error) {
	f.connected = true
	return "QR", nil
}
func (f *fakeEvoAPI) SetPresence(_ context.Context, _ string, a bool) error { f.presence = &a; return nil }
func (f *fakeEvoAPI) SendTyping(_ context.Context, _, _ string, _ bool) error { f.typingCalls++; return nil }

func TestEvoInstance_ConnectCreatesThenConnects(t *testing.T) {
	f := &fakeEvoAPI{}
	b := &store.ProxyBinding{ProxyURL: "socks5://u:p@1.2.3.4:1080"}
	e := NewEvoInstance(f, "wa_1", "https://cp/wh", b)
	if err := e.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.created || !f.connected {
		t.Fatalf("created=%v connected=%v", f.created, f.connected)
	}
	if f.proxySeen != b || f.webhookSeen != "https://cp/wh" {
		t.Fatal("proxy/webhook not threaded to CreateInstance")
	}
}

func TestEvoInstance_LivenessReflectsState(t *testing.T) {
	e := NewEvoInstance(&fakeEvoAPI{}, "wa_1", "", nil)
	if lv := e.Liveness(); lv.Alive || lv.Permanent {
		t.Fatalf("fresh instance should be not-alive not-permanent: %+v", lv)
	}
	e.UpdateState(StateConnected)
	if lv := e.Liveness(); !lv.Alive || lv.Permanent {
		t.Fatalf("connected: %+v", lv)
	}
	e.UpdateState(StateLoggedOut)
	if lv := e.Liveness(); lv.Alive || !lv.Permanent {
		t.Fatalf("loggedOut: %+v", lv)
	}
}

func TestEvoInstance_DisconnectDoesNotLogout(t *testing.T) {
	f := &fakeEvoAPI{}
	e := NewEvoInstance(f, "wa_1", "", nil)
	e.UpdateState(StateConnected)
	e.Disconnect()
	if lv := e.Liveness(); lv.Alive {
		t.Fatal("after Disconnect, not alive")
	}
	if lv := e.Liveness(); lv.Permanent {
		t.Fatal("Disconnect must NOT mark permanent (no logout)")
	}
}

func TestEvoInstance_PresenceDelegates(t *testing.T) {
	f := &fakeEvoAPI{}
	e := NewEvoInstance(f, "wa_1", "", nil)
	_ = e.SetPresence(context.Background(), false)
	_ = e.SendTyping(context.Background(), "15551234", true)
	if f.presence == nil || *f.presence != false || f.typingCalls != 1 {
		t.Fatalf("presence=%v typing=%d", f.presence, f.typingCalls)
	}
}

func TestEvoInstance_ImplementsCapabilities(t *testing.T) {
	var _ Conn = (*evoInstance)(nil)
	var _ PresenceConn = (*evoInstance)(nil)
	var _ LivenessConn = (*evoInstance)(nil)
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster/ -run TestEvoInstance -v`
Expected: FAIL（`NewEvoInstance`/`evoInstance`/`StateConnected` 未定义）

- [ ] **Step 3: 实现**

`internal/cluster/conn_evolution.go`：
```go
package cluster

import (
	"context"
	"sync"

	"github.com/acme/wadist/internal/store"
)

// InstanceState mirrors Evolution's connection lifecycle, kept fresh by the
// CONNECTION_UPDATE webhook (E3). It is the Go-side liveness truth.
type InstanceState string

const (
	StateCreated      InstanceState = "created"
	StateQR           InstanceState = "qr"
	StateConnected    InstanceState = "connected"
	StateDisconnected InstanceState = "disconnected"
	StateLoggedOut    InstanceState = "loggedOut"
)

// evoAPI is the slice of EvoClient that evoInstance needs (fake-testable).
type evoAPI interface {
	CreateInstance(ctx context.Context, instanceName string, proxy *store.ProxyBinding, webhookURL string) error
	ConnectInstance(ctx context.Context, instanceName string) (string, error)
	SetPresence(ctx context.Context, instanceName string, available bool) error
	SendTyping(ctx context.Context, instanceName, toPhone string, composing bool) error
}

// evoInstance is the Evolution-backed Conn: one account's live transport
// expressed as REST calls to an Evolution instance. Proxy is applied at create
// time (sticky). It implements PresenceConn (anthropomorphic dwell) and
// LivenessConn (from webhook-fed state). NOT wired into the live orchestrator
// in E1 — dormant behind WADIST_CONN until a later integration module.
type evoInstance struct {
	api          evoAPI
	instanceName string
	webhookURL   string
	proxy        *store.ProxyBinding

	mu    sync.Mutex
	state InstanceState
}

func NewEvoInstance(api evoAPI, instanceName, webhookURL string, proxy *store.ProxyBinding) *evoInstance {
	return &evoInstance{api: api, instanceName: instanceName, webhookURL: webhookURL, proxy: proxy, state: StateCreated}
}

var (
	_ Conn         = (*evoInstance)(nil)
	_ PresenceConn = (*evoInstance)(nil)
	_ LivenessConn = (*evoInstance)(nil)
)

// Connect provisions the instance (idempotent, sticky proxy + webhook) then
// triggers pairing. The QR (if unpaired) arrives via the QRCODE_UPDATED
// webhook; Connect propagates only errors.
func (e *evoInstance) Connect(ctx context.Context) error {
	if err := e.api.CreateInstance(ctx, e.instanceName, e.proxy, e.webhookURL); err != nil {
		return err
	}
	_, err := e.api.ConnectInstance(ctx, e.instanceName)
	return err
}

// Disconnect stops routing to this instance locally. It does NOT logout/delete
// (warm-residency; terminal recovery is the reaper/control-plane's call) — the
// same "Disconnect never Logout" contract as the whatsmeow Conn.
func (e *evoInstance) Disconnect() {
	e.UpdateState(StateDisconnected)
}

func (e *evoInstance) SetPresence(ctx context.Context, available bool) error {
	return e.api.SetPresence(ctx, e.instanceName, available)
}

func (e *evoInstance) SendTyping(ctx context.Context, chatPhone string, composing bool) error {
	return e.api.SendTyping(ctx, e.instanceName, chatPhone, composing)
}

// Liveness reports socket liveness from the webhook-fed state.
func (e *evoInstance) Liveness() Liveness {
	e.mu.Lock()
	s := e.state
	e.mu.Unlock()
	return Liveness{Alive: s == StateConnected, Permanent: s == StateLoggedOut}
}

// UpdateState is called by the webhook layer (E3) on CONNECTION_UPDATE.
func (e *evoInstance) UpdateState(s InstanceState) {
	e.mu.Lock()
	e.state = s
	e.mu.Unlock()
}
```

- [ ] **Step 4: 跑测试确认通过 + 全包编译**

Run: `go test ./internal/cluster/ -run TestEvoInstance -v && go build ./... && gofmt -l internal/cluster/conn_evolution.go internal/cluster/conn_evolution_test.go`
Expected: PASS + 编译通过 + gofmt 空输出

- [ ] **Step 5: Commit**

```bash
git add internal/cluster/conn_evolution.go internal/cluster/conn_evolution_test.go
git commit -m "feat(cluster): evoInstance implements Conn/PresenceConn/LivenessConn (dormant)"
```

---

## E1 收尾：门禁 + 零回归自检

- [ ] **门禁**

Run: `make gate`
Expected: `tidy vet test-race labels` 绿；`vuln` 仅 `GO-2026-5856`。

- [ ] **零回归自检**

Run: `grep -rn "NewEvoInstance\|evoInstance\|CreateInstance\|ConnectInstance" internal/ cmd/ --include=*.go | grep -v _test.go | grep -v "internal/cluster/"`
Expected: **空**——E1 新符号在 `internal/cluster` 之外无任何调用者（orchestrator/cmd 未接线），证明零行为变更，whatsmeow 仍唯一活跃数据面。

---

## Self-Review（对 spec §7 E1）

- **conn_evolution.go 实现 Conn/PresenceConn/LivenessConn** → Task 5。✅
- **CreateInstance 传代理（拆 ProxyBinding）** → Task 1（解析）+ Task 2（create）。✅
- **ConnectInstance/QR** → Task 3。✅
- **LogoutInstance/DeleteInstance** → Task 3。✅
- **presence（setPresence/typing）** → Task 4 + Task 5 委托。✅
- **门控默认 whatsmeow / 不接 live orchestrator** → 全模块休眠，收尾自检证明零调用者。✅（真正的 orchestrator 切换 + `WADIST_CONN` 生效留待后续集成模块，spec §7 E6 cutover 或独立集成 task。）
- **占位符扫描**：无 TBD；每步含实际代码/命令/期望。
- **类型一致性**：`evoAPI` 方法集与 Task 2/3/4 的 `EvoClient` 方法签名逐一匹配；`InstanceState` 常量在 Task 5 定义、测试引用一致；能力接口签名对齐 `types.go`。
- **待钉点**：所有 REST path/JSON 字段标 `TODO(evo-verify)`，真机回环在 E1 之后单独验证（spec §9①②）。

---

**下一步**：E2（`evoSender` 实现 `dispatch.Sender`：sendText/sendMedia 返回 key.id + retrySender 退避，门控 `WADIST_SENDER`）。E1 合并后 just-in-time 写。
