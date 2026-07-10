# E0 数据面换栈地基 — Evolution 骨架 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 Evolution 换栈的地基——`account_instances` 表、`instance_name↔jid` 解析器、Evolution HTTP 客户端骨架、log-only webhook 路由、Evolution 配置——**零发送行为变更**，whatsmeow 仍是唯一活跃数据面，`make gate` 全程绿。

**Architecture:** 纯新增、无删除。新增一张路由表 + store 层解析器 + 一个带连接池上限的 HTTP 客户端 + 一个只验签+log 的 webhook 端点。所有新代码在 E1+ 被接线前都不参与发送/连接，安全网 = 各自单测 + 现有 `make gate` 回归证明旧路径零回归。

**Tech Stack:** Go、pgx/pgxpool、gin、net/http（`http.Transport` 连接池）、crypto/hmac、testcontainers（迁移/store 集成测）、httptest（HTTP 客户端与 webhook 单测）。

## Global Constraints

以下逐字来自 spec，每个任务隐含遵守：

- **正确性不变式必须存活**：`balance = Σledger`、`frozen = Σ未结charge`、`message_id` 幂等、`sent_today` 加减对称、单账号单会话（fence token）。E0 不碰这些路径，靠 `make gate` 回归守护。
- **`make gate` 全程绿**：`gate: tidy vet test-race labels vuln`。`test-race` = `TESTCONTAINERS_RYUK_DISABLED=true go test -race ./...`（需 Docker）。已知 `vuln` 子项因 `GO-2026-5856`（Go1.26.4 crypto/tls）预先红，与本次无关，其余全绿即通过。
- **迁移是 replay-all 幂等模型**（无版本表，每次部署重放全部 migration）：新迁移必须 `IF NOT EXISTS` / `information_schema` guard，且过 `scripts/migrate_twice.sh` / `TestMigrations_AllIdempotent`。
- **tenant 为轴**：`account_instances` 带 `tenant_id`，RLS policy 与既有表一致；webhook/receipt 走 `SystemPool()` BYPASSRLS。
- **零行为变更**：E0 结束后 whatsmeow 仍是唯一活跃数据面，新代码无调用方（除单测）。

**执行前置**：从 `main` 新建分支 `feat/evolution-migration`，先确认基线 `make gate` 绿（除已知 vuln 红）再开工。

---

## Task 1: `account_instances` 迁移（幂等 + RLS）

**Files:**
- Create: `migrations/0020_account_instances.sql`
- Test: `internal/store/instances_db_test.go`（新建，testcontainers）

**Interfaces:**
- Produces: 表 `account_instances(instance_name PK, jid, tenant_id, evo_node, proxy_id, state, created_at, updated_at)`，`jid` 部分唯一索引，`tenant_id` RLS policy。

- [ ] **Step 1: 写迁移 SQL**

`migrations/0020_account_instances.sql`：
```sql
-- 0020: Evolution 换栈路由表。Go 侧只存 instance↔jid↔node 路由与状态，
-- 不存 Signal 会话（会话在 Evolution 侧 Redis）。replay-all 幂等。
CREATE TABLE IF NOT EXISTS account_instances (
    instance_name text PRIMARY KEY,
    jid           text,
    tenant_id     bigint NOT NULL,
    evo_node      text   NOT NULL DEFAULT 'default',
    proxy_id      bigint,
    state         text   NOT NULL DEFAULT 'created',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- 一个 jid 最多绑一个 instance（防双绑；NULL jid 不受约束，允许多未配对实例）。
CREATE UNIQUE INDEX IF NOT EXISTS uq_account_instances_jid
    ON account_instances (jid) WHERE jid IS NOT NULL;

CREATE INDEX IF NOT EXISTS ix_account_instances_tenant
    ON account_instances (tenant_id);

ALTER TABLE account_instances ENABLE ROW LEVEL SECURITY;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE tablename = 'account_instances' AND policyname = 'account_instances_tenant_isolation'
    ) THEN
        CREATE POLICY account_instances_tenant_isolation ON account_instances
            USING (tenant_id = current_setting('app.tenant_id', true)::bigint);
    END IF;
END $$;
```

- [ ] **Step 2: 写幂等 + 结构断言测试**

`internal/store/instances_db_test.go`（复用既有 testcontainers helper，参照 `proxy_test.go` 起 PG 的方式；helper 名以仓库现有为准）：
```go
func TestMigration0020_AccountInstances_Idempotent(t *testing.T) {
    pool := startPGWithMigrations(t)          // 复用现有 helper（applies all migrations）
    // 重放全部迁移第二次必须不报错（replay-all 幂等模型）。
    applyAllMigrations(t, pool)               // 复用现有 helper
    var reg bool
    err := pool.QueryRow(context.Background(),
        `SELECT to_regclass('public.account_instances') IS NOT NULL`).Scan(&reg)
    if err != nil || !reg {
        t.Fatalf("account_instances not present after migrate-twice: reg=%v err=%v", reg, err)
    }
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestMigration0020 -v`
Expected: FAIL（迁移文件不存在或 helper 未拾取 0020）→ 加文件后转 PASS。若 helper 名不符，先 `grep -rn "func start.*PG\|applyAllMigrations\|RunMigrations" internal/store/*_test.go` 对齐真实 helper 名再改测试。

- [ ] **Step 4: 跑迁移双跑脚本确认幂等**

Run: `bash scripts/migrate_twice.sh` （或 `go test ./... -run TestMigrations_AllIdempotent`）
Expected: PASS（0020 二次重放无错）

- [ ] **Step 5: Commit**

```bash
git add migrations/0020_account_instances.sql internal/store/instances_db_test.go
git commit -m "feat(store): 0020 account_instances routing table for evolution migration"
```

---

## Task 2: Evolution 配置项

**Files:**
- Modify: `internal/config/config.go`（`Config` struct + `Load` 里 `getenv`）
- Test: `internal/config/config_test.go`（追加）

**Interfaces:**
- Produces: `config.Config` 新增字段 `EvolutionBaseURL string`、`EvolutionAPIKey string`、`EvolutionWebhookSecret string`、`EvolutionNode string`。默认值：BaseURL=`http://localhost:8080`、Node=`default`、其余空串。

- [ ] **Step 1: 写失败测试**

`internal/config/config_test.go` 追加：
```go
func TestLoad_EvolutionDefaults(t *testing.T) {
    t.Setenv("WADIST_EVOLUTION_BASE_URL", "")
    t.Setenv("WADIST_EVOLUTION_NODE", "")
    cfg := Load() // 与现有 Load 签名一致；若 Load 返回 (Config,error) 则相应接收
    if cfg.EvolutionBaseURL != "http://localhost:8080" {
        t.Fatalf("base url default = %q", cfg.EvolutionBaseURL)
    }
    if cfg.EvolutionNode != "default" {
        t.Fatalf("node default = %q", cfg.EvolutionNode)
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/config/ -run TestLoad_EvolutionDefaults -v`
Expected: FAIL（字段不存在，编译错）

- [ ] **Step 3: 加字段与加载**

`internal/config/config.go` 的 `Config` struct 内追加：
```go
	// Evolution API 数据面（换栈）。E0 仅装载，未接线。
	EvolutionBaseURL       string
	EvolutionAPIKey        string
	EvolutionWebhookSecret string
	EvolutionNode          string
```
`Load()` 组装处追加（与既有 `getenv` 用法一致）：
```go
		EvolutionBaseURL:       getenv("WADIST_EVOLUTION_BASE_URL", "http://localhost:8080"),
		EvolutionAPIKey:        getenv("WADIST_EVOLUTION_APIKEY", ""),
		EvolutionWebhookSecret: getenv("WADIST_EVOLUTION_WEBHOOK_SECRET", ""),
		EvolutionNode:          getenv("WADIST_EVOLUTION_NODE", "default"),
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/config/ -run TestLoad_EvolutionDefaults -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): evolution base url/apikey/webhook-secret/node settings"
```

---

## Task 3: `instanceResolver`（jid↔instance↔node 解析器）

**Files:**
- Create: `internal/store/instances.go`
- Test: `internal/store/instances_db_test.go`（追加到 Task 1 文件）

**Interfaces:**
- Consumes: `*pgxpool.Pool`（`SystemPool()`，webhook/路由无 tenant 上下文时用）。
- Produces:
  - `func (m *Manager) UpsertInstance(ctx, in InstanceRow) error`
  - `func (m *Manager) BindInstanceJID(ctx, instanceName, jid string) error`（`connection.update` 首次拿到 jid 时回填，幂等）
  - `func (m *Manager) SetInstanceState(ctx, instanceName, state string) error`
  - `func (m *Manager) JIDForInstance(ctx, instanceName string) (jid string, ok bool, err error)`
  - `func (m *Manager) InstanceForJID(ctx, jid string) (instanceName string, ok bool, err error)`
  - `type InstanceRow struct { InstanceName, JID, EvoNode, State string; TenantID, ProxyID int64 }`

- [ ] **Step 1: 写失败测试（round-trip + jid 回填 + 反查）**

`internal/store/instances_db_test.go` 追加：
```go
func TestInstanceResolver_RoundTrip(t *testing.T) {
    ctx := context.Background()
    m := newTestManager(t) // 复用现有 Manager 测试构造（对齐 proxy_test.go）
    if err := m.UpsertInstance(ctx, store.InstanceRow{
        InstanceName: "wa_1_default_1", TenantID: 1, EvoNode: "default", State: "created",
    }); err != nil {
        t.Fatal(err)
    }
    if err := m.BindInstanceJID(ctx, "wa_1_default_1", "123@s.whatsapp.net"); err != nil {
        t.Fatal(err)
    }
    jid, ok, err := m.JIDForInstance(ctx, "wa_1_default_1")
    if err != nil || !ok || jid != "123@s.whatsapp.net" {
        t.Fatalf("JIDForInstance = %q ok=%v err=%v", jid, ok, err)
    }
    inst, ok, err := m.InstanceForJID(ctx, "123@s.whatsapp.net")
    if err != nil || !ok || inst != "wa_1_default_1" {
        t.Fatalf("InstanceForJID = %q ok=%v err=%v", inst, ok, err)
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestInstanceResolver -v`
Expected: FAIL（方法/类型未定义）

- [ ] **Step 3: 实现解析器**

`internal/store/instances.go`：
```go
package store

import "context"

// InstanceRow is one row of account_instances (Evolution routing table).
type InstanceRow struct {
	InstanceName string
	JID          string
	EvoNode      string
	State        string
	TenantID     int64
	ProxyID      int64
}

// UpsertInstance creates or updates an instance routing row. Idempotent on
// instance_name. Uses the system (BYPASSRLS) pool: routing has no tenant
// request-context, tenant_id is a stored column.
func (m *Manager) UpsertInstance(ctx context.Context, in InstanceRow) error {
	_, err := m.SystemPool().Exec(ctx, `
INSERT INTO account_instances (instance_name, jid, tenant_id, evo_node, state, updated_at)
VALUES ($1, NULLIF($2,''), $3, $4, $5, now())
ON CONFLICT (instance_name) DO UPDATE
   SET evo_node = EXCLUDED.evo_node, state = EXCLUDED.state, updated_at = now()`,
		in.InstanceName, in.JID, in.TenantID, in.EvoNode, in.State)
	return err
}

// BindInstanceJID back-fills the jid once pairing completes. Idempotent.
func (m *Manager) BindInstanceJID(ctx context.Context, instanceName, jid string) error {
	_, err := m.SystemPool().Exec(ctx,
		`UPDATE account_instances SET jid=$2, updated_at=now() WHERE instance_name=$1`,
		instanceName, jid)
	return err
}

// SetInstanceState updates lifecycle state (created/qr/connected/disconnected/loggedOut).
func (m *Manager) SetInstanceState(ctx context.Context, instanceName, state string) error {
	_, err := m.SystemPool().Exec(ctx,
		`UPDATE account_instances SET state=$2, updated_at=now() WHERE instance_name=$1`,
		instanceName, state)
	return err
}

func (m *Manager) JIDForInstance(ctx context.Context, instanceName string) (string, bool, error) {
	var jid *string
	err := m.SystemPool().QueryRow(ctx,
		`SELECT jid FROM account_instances WHERE instance_name=$1`, instanceName).Scan(&jid)
	if err != nil || jid == nil {
		return "", false, ignoreNoRows(err)
	}
	return *jid, true, nil
}

func (m *Manager) InstanceForJID(ctx context.Context, jid string) (string, bool, error) {
	var name string
	err := m.SystemPool().QueryRow(ctx,
		`SELECT instance_name FROM account_instances WHERE jid=$1`, jid).Scan(&name)
	if err != nil {
		return "", false, ignoreNoRows(err)
	}
	return name, true, nil
}
```
若仓库无 `ignoreNoRows` helper，用内联：`if errors.Is(err, pgx.ErrNoRows) { return "", false, nil }`（import `github.com/jackc/pgx/v5`）。确认 `Manager.SystemPool()` 存在（坍缩后 admin 后台已用它读 redis owner；grep 确认签名）。

- [ ] **Step 4: 跑测试确认通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run "TestInstanceResolver|TestMigration0020" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/store/instances.go internal/store/instances_db_test.go
git commit -m "feat(store): instance resolver (jid<->instance<->node routing crud)"
```

---

## Task 4: Evolution HTTP 客户端骨架（连接池上限 + apikey 注入）

**Files:**
- Create: `internal/cluster/evolution_client.go`
- Test: `internal/cluster/evolution_client_test.go`（httptest，无需 whatsmeow）

**Interfaces:**
- Consumes: `config` 的 BaseURL/APIKey。
- Produces:
  - `type EvoClient struct { ... }`
  - `func NewEvoClient(baseURL, apiKey string) *EvoClient`（内部装 `http.Transport{MaxConnsPerHost, MaxIdleConnsPerHost}`）
  - `func (c *EvoClient) doJSON(ctx, method, path string, body any, out any) error`（注入 `apikey` 头 + `Content-Type`）
  - `func (c *EvoClient) FetchState(ctx, instanceName string) (string, error)`（E0 唯一实体方法，证明回环；调 `GET /instance/connectionState/{name}`）

- [ ] **Step 1: 写失败测试（httptest 假 Evolution）**

`internal/cluster/evolution_client_test.go`：
```go
func TestEvoClient_FetchState_SendsApiKeyAndParses(t *testing.T) {
    var gotKey, gotPath string
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotKey = r.Header.Get("apikey")
        gotPath = r.URL.Path
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{"instance":{"state":"open"}}`))
    }))
    defer srv.Close()

    c := NewEvoClient(srv.URL, "secret-key")
    state, err := c.FetchState(context.Background(), "wa_1_default_1")
    if err != nil {
        t.Fatal(err)
    }
    if gotKey != "secret-key" {
        t.Fatalf("apikey header = %q", gotKey)
    }
    if gotPath != "/instance/connectionState/wa_1_default_1" {
        t.Fatalf("path = %q", gotPath)
    }
    if state != "open" {
        t.Fatalf("state = %q", state)
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cluster/ -run TestEvoClient_FetchState -v`
Expected: FAIL（`NewEvoClient`/`FetchState` 未定义）

- [ ] **Step 3: 实现客户端骨架**

`internal/cluster/evolution_client.go`：
```go
package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// EvoClient is the low-level HTTP client to one Evolution API node. It caps
// connections per host so a burst of sends cannot exhaust the (single-process,
// V8) Evolution node — the density guardrail called out in the migration spec.
type EvoClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewEvoClient(baseURL, apiKey string) *EvoClient {
	tr := &http.Transport{
		MaxConnsPerHost:     64,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
	}
	return &EvoClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}
}

// doJSON issues a request with the apikey header and decodes a JSON response.
func (c *EvoClient) doJSON(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("evolution %s %s: %d %s", method, path, resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// FetchState returns the connection state of an instance (E0 round-trip proof).
func (c *EvoClient) FetchState(ctx context.Context, instanceName string) (string, error) {
	var out struct {
		Instance struct {
			State string `json:"state"`
		} `json:"instance"`
	}
	if err := c.doJSON(ctx, http.MethodGet,
		"/instance/connectionState/"+instanceName, nil, &out); err != nil {
		return "", err
	}
	return out.Instance.State, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cluster/ -run TestEvoClient_FetchState -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cluster/evolution_client.go internal/cluster/evolution_client_test.go
git commit -m "feat(cluster): evolution http client skeleton (conn-pool cap, apikey, FetchState)"
```

---

## Task 5: Webhook 路由（log-only + HMAC 校验）

**Files:**
- Create: `internal/api/webhook_evolution.go`
- Modify: `internal/api/router.go`（`Router()` 内 `v1` 组下挂无鉴权路由）
- Test: `internal/api/webhook_evolution_test.go`（gin + httptest）

**Interfaces:**
- Consumes: `config.EvolutionWebhookSecret`。
- Produces:
  - `type EvolutionWebhook struct { secret string; log *zap.SugaredLogger }`
  - `func NewEvolutionWebhook(secret string, log *zap.SugaredLogger) *EvolutionWebhook`
  - `func (h *EvolutionWebhook) Register(r gin.IRouter)`（挂 `POST /webhook/evolution`）
  - `func (h *EvolutionWebhook) verify(sig string, body []byte) bool`
  - **E0 只 log，不落库**；E3 再升级为喂 `receipt.Recorder`/`sendgate`。

- [ ] **Step 1: 写失败测试（验签放行 + 拒伪造）**

`internal/api/webhook_evolution_test.go`：
```go
func sign(secret string, body []byte) string {
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(body)
    return hex.EncodeToString(mac.Sum(nil))
}

func TestEvolutionWebhook_HMAC(t *testing.T) {
    gin.SetMode(gin.TestMode)
    r := gin.New()
    NewEvolutionWebhook("s3cr3t", zap.NewNop().Sugar()).Register(r)

    body := []byte(`{"event":"connection.update","instance":"wa_1","data":{"state":"open"}}`)

    // 正确签名 → 200
    req := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewReader(body))
    req.Header.Set("X-Evolution-Signature", sign("s3cr3t", body))
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("valid sig → %d", w.Code)
    }

    // 错误签名 → 401
    req2 := httptest.NewRequest(http.MethodPost, "/webhook/evolution", bytes.NewReader(body))
    req2.Header.Set("X-Evolution-Signature", "deadbeef")
    w2 := httptest.NewRecorder()
    r.ServeHTTP(w2, req2)
    if w2.Code != http.StatusUnauthorized {
        t.Fatalf("bad sig → %d", w2.Code)
    }
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/api/ -run TestEvolutionWebhook_HMAC -v`
Expected: FAIL（`NewEvolutionWebhook` 未定义）

- [ ] **Step 3: 实现 log-only handler**

`internal/api/webhook_evolution.go`：
```go
package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// EvolutionWebhook receives Evolution API callbacks. E0 verifies HMAC and logs;
// E3 upgrades this to feed receipt.Recorder + sendgate health signals.
type EvolutionWebhook struct {
	secret string
	log    *zap.SugaredLogger
}

func NewEvolutionWebhook(secret string, log *zap.SugaredLogger) *EvolutionWebhook {
	return &EvolutionWebhook{secret: secret, log: log}
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
	// E0: observe only. Never mutate campaign_recipients here yet.
	h.log.Infow("evolution webhook (log-only)", "bytes", len(body))
	c.Status(http.StatusOK)
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

- [ ] **Step 4: 挂进 router**

`internal/api/router.go` 的 `Router()`，在 `v1 := r.Group("/api/v1")` 之后、鉴权组之前追加（webhook 不走用户鉴权，靠 HMAC）：
```go
	// Evolution 数据面回调（HMAC 鉴权，非用户鉴权）。E0 log-only。
	NewEvolutionWebhook(s.cfg.EvolutionWebhookSecret, s.log).Register(v1)
```
确认 `Server` 结构体持有 `cfg config.Config` 与 `log *zap.SugaredLogger`（grep `type Server struct`；若字段名不同，按真实名调整）。挂在 `v1` 上则完整路径为 `/api/v1/webhook/evolution`（与 spec 一致）。

- [ ] **Step 5: 跑测试 + 全量门禁**

Run: `go test ./internal/api/ -run TestEvolutionWebhook_HMAC -v && go build ./...`
Expected: PASS + 编译通过

- [ ] **Step 6: Commit**

```bash
git add internal/api/webhook_evolution.go internal/api/router.go internal/api/webhook_evolution_test.go
git commit -m "feat(api): evolution webhook route (log-only, hmac-verified)"
```

---

## E0 收尾：门禁 + 自检

- [ ] **跑全量门禁**

Run: `make gate`
Expected: `tidy vet test-race labels` 全绿；`vuln` 仅 `GO-2026-5856`（预存在，与 E0 无关）。

- [ ] **零回归自检**

Run: `grep -rn "EvoClient\|EvolutionWebhook\|UpsertInstance" internal/ --include=*.go | grep -v _test.go`
Expected: 新符号仅出现在其定义文件与 `router.go` 的一处挂载——**没有任何发送/连接路径调用它们**，证明 E0 零行为变更（whatsmeow 仍唯一活跃数据面）。

---

## Self-Review（对 spec）

- **Spec §5 数据模型** → Task 1（表）+ Task 3（解析器 CRUD）。✅
- **Spec §7 E0 清单**：`account_instances` 迁移（T1）、`instanceResolver`（T3）、`evolution_client.go` + Transport 池上限（T4）、webhook 路由 log-only（T5）、Evolution 配置（T2）。✅ 全覆盖。
- **Spec §3 不变式**：E0 不碰计费/幂等/记账/防双开路径；靠 `make gate` 回归 + 收尾自检证明零调用。✅
- **占位符扫描**：无 TBD；每步含实际代码/命令/期望。helper 名（`startPGWithMigrations`/`newTestManager`/`SystemPool`/`Server` 字段）标注了"以仓库现有为准，grep 对齐"——因这些是既有符号、非本计划新造，实现者须先对齐真实签名。
- **类型一致性**：`InstanceRow`（T3）字段与 T1 表列一致；`EvoClient`（T4）、`EvolutionWebhook`（T5）符号在 Interfaces 与代码块中同名。✅

---

**下一步**：E0 合并后，E1（实例生命周期 `conn_evolution.go`，`Conn`/`PresenceConn`/`LivenessConn` + 代理粘性 create）、E2（`evoSender`）、E3（webhook 升级喂 receipt）、E4（背压/反封）、E5（多节点分片）、E6（cutover 拆 whatsmeow）各自一份计划，按 spec §7 顺序 just-in-time 写。
