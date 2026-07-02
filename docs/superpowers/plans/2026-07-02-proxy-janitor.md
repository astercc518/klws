# ProxyJanitor（死代理自动重绑）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 `control.Janitor`：周期性发现绑在死代理上的活跃账号，解绑 → 断会话 → 经 `warm:req` 触发既有粘性重绑链路换到新 alive 代理，无需人工介入。

**Architecture:** Janitor 纯编排、零引擎依赖，靠注入回调（`ListDeadProxyAccounts`/`Release`/`Evict`/`RequestWarm`）工作。唯一 store 改动是新增一个只读跨租户查询 `ListDeadProxyAccountsAll`（镜像现有 `ListAccountsByDeadProxies`，加性，不碰任何绑定/计费事务）。重绑动作全部复用既有 `StickyBindProxy`→`BindProxy`→`ApplyProxy` 链路。

**Tech Stack:** Go 1.26、go-redis/v9、prometheus/client_golang、testcontainers PG16。设计见 [`../specs/2026-07-02-control-plane-selfheal-design.md`](../specs/2026-07-02-control-plane-selfheal-design.md)。

## Global Constraints

- 红线（[[klws-redlines]]）：**不改** `billing`/`dispatch`/`store`/`sendgate`/`cluster` 的核心业务逻辑与计费/防封/绑定事务。本计划对 `store` 的**唯一**改动是新增只读方法 `ListDeadProxyAccountsAll`（加性、逐字镜像现有查询，已在 spec §3.1 标为红线擦边、需用户签字）。
- `internal/control` **禁止 import** `internal/node`、`internal/cluster`、`internal/store`——只收回调与 `*goredis.Client`。
- Redis 库别名 `goredis "github.com/redis/go-redis/v9"`。
- `warm:req` 项格式沿用既有约定 `jid + "|" + cc`（见 [workingset.go:40](../../../internal/control/workingset.go#L40) `splitReq`）。
- 指标 label 有界：新增计数器**无 label**（过 `scripts/check_metric_labels.sh`）。

## File Structure

- Modify `internal/store/proxy.go` — 新增 `DeadProxyAccount` 类型 + `ListDeadProxyAccountsAll` 只读方法。
- Create `internal/control/janitor.go` — `Janitor`（Tick/Run）+ `JanitorConfig`/`JanitorDeps`/`DeadProxyAccount`。
- Create `internal/control/janitor_test.go` — fake 回调单测（无容器）。
- Modify `internal/metrics/metrics.go` — 新增 `wadist_proxy_rebind_total` 计数器 + `IncProxyRebind()`。
- Modify `internal/config/config.go` + `internal/config/config_test.go` — janitor interval/batch env。
- Modify `cmd/wadist/main.go` — 装配 Janitor 回调并 `sup.Go(janitor.Run)`。

---

## Task 1: store 新增只读跨租户查询 ListDeadProxyAccountsAll

**Files:**
- Modify: `internal/store/proxy.go`（在 `ListAccountsByDeadProxies` 之后，[proxy.go:169](../../../internal/store/proxy.go#L169) 附近）
- Test: `internal/store/proxy_deadall_test.go`

**Interfaces:**
- Produces:
  - `type DeadProxyAccount struct{ JID, CountryCode string }`
  - `func (m *Manager) ListDeadProxyAccountsAll(ctx context.Context, limit int) ([]DeadProxyAccount, error)`

- [ ] **Step 1: 写失败测试**

```go
// internal/store/proxy_deadall_test.go
package store

import (
	"context"
	"testing"
)

func TestListDeadProxyAccountsAll_CrossTenant(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)

	// 两个租户各一活跃账号，绑同一代理。
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://u:p@h:1080", "US", 5)
	seedAccount(t, ctx, m.BizPool(), 1, "t1@s.whatsapp.net", "15550000001")
	seedAccount(t, ctx, m.BizPool(), 2, "t2@s.whatsapp.net", "15550000002")
	if _, err := m.BindProxy(ctx, "t1@s.whatsapp.net", "US"); err != nil {
		t.Fatalf("bind t1: %v", err)
	}
	if _, err := m.BindProxy(ctx, "t2@s.whatsapp.net", "US"); err != nil {
		t.Fatalf("bind t2: %v", err)
	}

	// 代理仍活着 → 不应返回任何账号。
	got, err := m.ListDeadProxyAccountsAll(ctx, 100)
	if err != nil {
		t.Fatalf("list (alive): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("alive proxy: got %v want none", got)
	}

	// 打死代理。
	if _, err := m.BizPool().Exec(ctx, `UPDATE proxy_pool SET is_alive=FALSE WHERE id=$1`, pid); err != nil {
		t.Fatalf("kill proxy: %v", err)
	}

	got, err = m.ListDeadProxyAccountsAll(ctx, 100)
	if err != nil {
		t.Fatalf("list (dead): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("dead proxy: got %d accounts want 2 (cross-tenant): %+v", len(got), got)
	}
	for _, a := range got {
		if a.CountryCode != "US" {
			t.Fatalf("cc=%q want US for %+v", a.CountryCode, a)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/store/ -run TestListDeadProxyAccountsAll_CrossTenant -count=1`
Expected: FAIL（`ListDeadProxyAccountsAll` / `DeadProxyAccount` 未定义）

- [ ] **Step 3: 实现（proxy.go 追加）**

```go
// DeadProxyAccount is an active account bound to a dead proxy, with the dead
// proxy's country_code (so the rebind can pick a same-country replacement).
type DeadProxyAccount struct {
	JID         string
	CountryCode string
}

// ListDeadProxyAccountsAll returns, across ALL tenants, active accounts bound to
// dead proxies, for automatic rebind. Read-only; mirrors ListAccountsByDeadProxies
// without the tenant filter and additionally returns the proxy country_code.
func (m *Manager) ListDeadProxyAccountsAll(ctx context.Context, limit int) ([]DeadProxyAccount, error) {
	const sql = `
SELECT a.account_jid, p.country_code
  FROM account_devices a
  JOIN proxy_pool p ON p.id = a.proxy_id
 WHERE p.is_alive = FALSE AND a.ban_status = 'active'
 LIMIT $1;`
	rows, err := m.bizPool.Query(ctx, sql, limit)
	if err != nil {
		return nil, fmt.Errorf("query dead-proxy accounts (all): %w", err)
	}
	defer rows.Close()

	var out []DeadProxyAccount
	for rows.Next() {
		var a DeadProxyAccount
		if err := rows.Scan(&a.JID, &a.CountryCode); err != nil {
			return nil, fmt.Errorf("scan dead-proxy account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/store/ -run TestListDeadProxyAccountsAll_CrossTenant -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/store/proxy.go internal/store/proxy_deadall_test.go
git commit -m "feat(store): add read-only ListDeadProxyAccountsAll (cross-tenant, for rebind)"
```

---

## Task 2: metrics 新增 wadist_proxy_rebind_total

**Files:**
- Modify: `internal/metrics/metrics.go`（字段 [:23](../../../internal/metrics/metrics.go#L23)、构造 [:53](../../../internal/metrics/metrics.go#L53)、注册 [:74](../../../internal/metrics/metrics.go#L74)、Inc 方法 [:125](../../../internal/metrics/metrics.go#L125) 各处比照 `noCapacity`）
- Test: `internal/metrics/metrics_test.go`（若已有则追加）

**Interfaces:**
- Produces: `func (m *Metrics) IncProxyRebind()`

- [ ] **Step 1: 写失败测试**

```go
// internal/metrics/metrics_test.go（追加；若文件不存在则创建，package metrics）
func TestIncProxyRebind(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.IncProxyRebind()
	m.IncProxyRebind()
	if got := testutil.ToFloat64(m.proxyRebind); got != 2 {
		t.Fatalf("proxy_rebind=%v want 2", got)
	}
}
```

> import `"github.com/prometheus/client_golang/prometheus"` 与 `"github.com/prometheus/client_golang/prometheus/testutil"`（testutil 已在 go.sum，随 client_golang）。`New(reg)` 的签名以本包实际为准（现有测试已在用）。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/metrics/ -run TestIncProxyRebind -count=1`
Expected: FAIL（`proxyRebind` / `IncProxyRebind` 未定义）

- [ ] **Step 3: 实现（metrics.go 四处，比照 noCapacity）**

字段（struct 内，`noCapacity` 旁）：

```go
	proxyRebind   prometheus.Counter
```

构造（`New` 内，`m.noCapacity = ...` 之后）：

```go
	m.proxyRebind = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "wadist_proxy_rebind_total",
		Help: "Accounts auto-rebound off a dead proxy by the janitor.",
	})
```

注册（`reg.MustRegister(` 列表内，`m.noCapacity` 同行）：把 `m.proxyRebind` 加入参数列表。

Inc 方法（`IncNoCapacity` 旁）：

```go
func (m *Metrics) IncProxyRebind() {
	if m == nil {
		return
	}
	m.proxyRebind.Inc()
}
```

- [ ] **Step 4: 运行确认通过 + label 门禁**

Run: `go test ./internal/metrics/ -run TestIncProxyRebind -count=1 && ./scripts/check_metric_labels.sh`
Expected: PASS（新计数器无 label）

- [ ] **Step 5: 提交**

```bash
git add internal/metrics/metrics.go internal/metrics/metrics_test.go
git commit -m "feat(metrics): add wadist_proxy_rebind_total counter"
```

---

## Task 3: control.Janitor（Tick + Run）

**Files:**
- Create: `internal/control/janitor.go`
- Test: `internal/control/janitor_test.go`

**Interfaces:**
- Produces:
  - `type DeadProxyAccount struct{ JID, CC string }`
  - `type JanitorConfig struct{ Batch int }`
  - `type JanitorDeps struct{ ListDeadProxyAccounts func(ctx, limit int) ([]DeadProxyAccount, error); Release func(ctx, jid string) error; Evict func(ctx, jid string, linger time.Duration); RequestWarm func(ctx, jid, cc string) error; OnRebind func() }`
  - `func NewJanitor(cfg JanitorConfig, deps JanitorDeps) *Janitor`
  - `func (j *Janitor) Tick(ctx context.Context) (int, error)`
  - `func (j *Janitor) Run(ctx context.Context, interval time.Duration) error`

- [ ] **Step 1: 写失败测试 — Tick 对每个死代理账号执行 释放→驱逐→补热**

```go
// internal/control/janitor_test.go
package control

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestJanitorTick_ReleaseEvictWarm(t *testing.T) {
	ctx := context.Background()
	dead := []DeadProxyAccount{{JID: "a", CC: "US"}, {JID: "b", CC: "IN"}}
	var released, evicted []string
	warmed := map[string]string{}
	rebinds := 0

	j := NewJanitor(JanitorConfig{Batch: 100}, JanitorDeps{
		ListDeadProxyAccounts: func(_ context.Context, _ int) ([]DeadProxyAccount, error) {
			return dead, nil
		},
		Release: func(_ context.Context, jid string) error { released = append(released, jid); return nil },
		Evict: func(_ context.Context, jid string, linger time.Duration) {
			if linger != 0 {
				t.Fatalf("evict linger=%v want 0 (drop session immediately)", linger)
			}
			evicted = append(evicted, jid)
		},
		RequestWarm: func(_ context.Context, jid, cc string) error { warmed[jid] = cc; return nil },
		OnRebind:    func() { rebinds++ },
	})

	n, err := j.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n != 2 || rebinds != 2 {
		t.Fatalf("n=%d rebinds=%d want 2/2", n, rebinds)
	}
	if len(released) != 2 || len(evicted) != 2 {
		t.Fatalf("released=%v evicted=%v want 2 each", released, evicted)
	}
	if warmed["a"] != "US" || warmed["b"] != "IN" {
		t.Fatalf("warmed=%v want a->US b->IN", warmed)
	}
}

func TestJanitorTick_ReleaseErrorSkipsButContinues(t *testing.T) {
	ctx := context.Background()
	var warmed []string
	j := NewJanitor(JanitorConfig{Batch: 100}, JanitorDeps{
		ListDeadProxyAccounts: func(_ context.Context, _ int) ([]DeadProxyAccount, error) {
			return []DeadProxyAccount{{JID: "bad", CC: "US"}, {JID: "ok", CC: "US"}}, nil
		},
		Release: func(_ context.Context, jid string) error {
			if jid == "bad" {
				return errors.New("release failed")
			}
			return nil
		},
		Evict:       func(_ context.Context, _ string, _ time.Duration) {},
		RequestWarm: func(_ context.Context, jid, _ string) error { warmed = append(warmed, jid); return nil },
	})
	n, err := j.Tick(ctx)
	if err != nil {
		t.Fatalf("tick should not fail on per-account error: %v", err)
	}
	// "bad" 释放失败 → 跳过不补热；"ok" 正常处理。
	if n != 1 || len(warmed) != 1 || warmed[0] != "ok" {
		t.Fatalf("n=%d warmed=%v want 1 / [ok]", n, warmed)
	}
}
```

Run: `go test ./internal/control/ -run TestJanitor -count=1`
Expected: FAIL（未定义）

- [ ] **Step 2: 实现（janitor.go）**

```go
// internal/control/janitor.go
package control

import (
	"context"
	"log"
	"time"
)

// DeadProxyAccount 是绑在死代理上的活跃账号（携带死代理国家码，供同国重绑）。
type DeadProxyAccount struct {
	JID string
	CC  string
}

type JanitorConfig struct {
	Batch int // 单轮处理上限
}

// JanitorDeps 注入回调，保持 control 零引擎依赖。
type JanitorDeps struct {
	ListDeadProxyAccounts func(ctx context.Context, limit int) ([]DeadProxyAccount, error)
	Release               func(ctx context.Context, jid string) error
	Evict                 func(ctx context.Context, jid string, linger time.Duration)
	RequestWarm           func(ctx context.Context, jid, cc string) error
	OnRebind              func() // 可空：每成功触发一次重绑调用一次（接指标）
}

type Janitor struct {
	cfg  JanitorConfig
	deps JanitorDeps
}

func NewJanitor(cfg JanitorConfig, deps JanitorDeps) *Janitor {
	if cfg.Batch <= 0 {
		cfg.Batch = 256
	}
	return &Janitor{cfg: cfg, deps: deps}
}

// Tick 处理一批死代理账号：解绑 → 立即断会话 → 经 warm:req 触发粘性重绑。
// 返回成功触发重绑的账号数。单账号错误跳过、不中断整批。
func (j *Janitor) Tick(ctx context.Context) (int, error) {
	accts, err := j.deps.ListDeadProxyAccounts(ctx, j.cfg.Batch)
	if err != nil {
		return 0, err
	}
	rebound := 0
	for _, a := range accts {
		// 1. 解绑死代理：让 StickyBindProxy.getBound 转 false，下次 warm 才会重挑。
		if err := j.deps.Release(ctx, a.JID); err != nil {
			log.Printf("control: janitor release %s: %v", a.JID, err)
			continue // 未成功解绑就补热会被粘性跳过 → 跳过本账号
		}
		// 2. linger=0 立即断掉跑在死代理上的活会话。
		j.deps.Evict(ctx, a.JID, 0)
		// 3. 经 warm:req 触发重绑+重连（BindProxy 挑新 alive 代理，ApplyProxy 注入）。
		if err := j.deps.RequestWarm(ctx, a.JID, a.CC); err != nil {
			log.Printf("control: janitor warm-req %s: %v", a.JID, err)
			continue
		}
		rebound++
		if j.deps.OnRebind != nil {
			j.deps.OnRebind()
		}
	}
	return rebound, nil
}

// Run 每 interval 调 Tick；interval<=0 表示禁用（直接返回）。Tick 错误记录并继续。
func (j *Janitor) Run(ctx context.Context, interval time.Duration) error {
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
			if n, err := j.Tick(ctx); err != nil {
				log.Printf("control: janitor tick error: %v", err)
			} else if n > 0 {
				log.Printf("control: janitor rebound %d dead-proxy accounts", n)
			}
		}
	}
}
```

- [ ] **Step 3: 运行确认通过**

Run: `go test ./internal/control/ -run TestJanitor -count=1`
Expected: PASS

- [ ] **Step 4: 依赖隔离核对**

Run: `go list -deps ./internal/control/ | grep -E 'wadist/internal/(node|cluster|store)$' || echo "clean: no engine imports"`
Expected: `clean: no engine imports`

- [ ] **Step 5: 提交**

```bash
git add internal/control/janitor.go internal/control/janitor_test.go
git commit -m "feat(control): ProxyJanitor auto-rebinds accounts off dead proxies"
```

---

## Task 4: 装配 — config + cmd/wadist

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/wadist/main.go`

**Interfaces:**
- Consumes: `store.ListDeadProxyAccountsAll`/`ReleaseProxy`（Task 1 + 现有）、`cluster.Registry.Get` + `Session.GracefulClose`（现有 Evict 回调形状）、`metrics.IncProxyRebind`（Task 2）、`control.NewJanitor`（Task 3）、现有 `rdb`/`mgr`/`reg`/`sup`/`m`（metrics）。

- [ ] **Step 1: 写失败测试 — janitor 默认配置**

```go
// internal/config/config_test.go 追加
func TestLoad_ProxyJanitorDefaults(t *testing.T) {
	t.Setenv("WADIST_REDIS_ADDR", "x:1")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProxyJanitorInterval != 30*time.Second {
		t.Fatalf("ProxyJanitorInterval=%v want 30s", cfg.ProxyJanitorInterval)
	}
	if cfg.ProxyJanitorBatch != 256 {
		t.Fatalf("ProxyJanitorBatch=%d want 256", cfg.ProxyJanitorBatch)
	}
}
```

Run: `go test ./internal/config/ -run TestLoad_ProxyJanitorDefaults -count=1`
Expected: FAIL

- [ ] **Step 2: config 加字段 + 加载（config.go）**

`Config` struct 加：

```go
	ProxyJanitorInterval time.Duration
	ProxyJanitorBatch    int
```

`Load()` 内加（沿用既有 `msEnv`/`intEnv` helper，见控制面计划 Task 5；已存在则直接用）：

```go
	cfg.ProxyJanitorInterval = msEnv("WADIST_PROXY_JANITOR_INTERVAL_MS", 30000)
	cfg.ProxyJanitorBatch = intEnv("WADIST_PROXY_JANITOR_BATCH", 256)
```

Run: `go test ./internal/config/ -run TestLoad_ProxyJanitorDefaults -count=1`
Expected: PASS

- [ ] **Step 3: cmd/wadist 接线**

在控制面已构造 `sched`/`ws`/`deps`（`Evict`）之后，`sup.Go(ws.Run...)` 附近，新增：

```go
	janitor := control.NewJanitor(
		control.JanitorConfig{Batch: cfg.ProxyJanitorBatch},
		control.JanitorDeps{
			ListDeadProxyAccounts: func(ctx context.Context, limit int) ([]control.DeadProxyAccount, error) {
				rows, err := mgr.ListDeadProxyAccountsAll(ctx, limit)
				if err != nil {
					return nil, err
				}
				out := make([]control.DeadProxyAccount, len(rows))
				for i, r := range rows {
					out[i] = control.DeadProxyAccount{JID: r.JID, CC: r.CountryCode}
				}
				return out, nil
			},
			Release: func(ctx context.Context, jid string) error { return mgr.ReleaseProxy(ctx, jid) },
			Evict: func(ctx context.Context, jid string, linger time.Duration) {
				if s, ok := reg.Get(jid); ok {
					s.GracefulClose(ctx, linger)
				}
			},
			RequestWarm: func(ctx context.Context, jid, cc string) error {
				return rdb.RPush(ctx, "warm:req", jid+"|"+cc).Err()
			},
			OnRebind: func() { m.IncProxyRebind() },
		},
	)
	sup.Go(func(lctx context.Context) error { return janitor.Run(lctx, cfg.ProxyJanitorInterval) })
```

> `m` 是已构造的 `*metrics.Metrics`；`reg`/`rdb`/`mgr`/`sup` 均为现有变量。`Session.GracefulClose(ctx, linger)` 与控制面 `Evict` 回调用法一致。

- [ ] **Step 4: 构建 + 全量测试**

Run: `go build ./... && go test ./internal/control/ ./internal/store/ ./internal/metrics/ ./internal/config/ -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/wadist/main.go
git commit -m "feat: wire ProxyJanitor into node startup (auto-rebind dead-proxy accounts)"
```

---

## 验收（合并前）

- [ ] `make gate` 绿。
- [ ] `go list -deps ./internal/control/` 无 node/cluster/store import。
- [ ] `ListDeadProxyAccountsAll` 跨租户返回、代理复活后返回空、携带正确 country_code。
- [ ] Janitor Tick：每死代理账号执行 Release→Evict(linger=0)→RequestWarm(jid|cc)；Release 失败则跳过该账号不补热；单账号错误不中断整批。
- [ ] `wadist_proxy_rebind_total` 随重绑递增，无高基数 label。
- [ ] 未改 store 绑定/计费/防封事务逻辑（本计划仅新增一个只读方法）。

## Self-Review 备注

- **红线**：唯一 store 改动是新增只读 `ListDeadProxyAccountsAll`，逐字镜像现有 `ListAccountsByDeadProxies` 查询，不触碰任何写事务。已在 spec §3.1 标记需用户签字——执行前确认。
- **粘性正确性**：Janitor 先 `Release`（解绑）再 `RequestWarm`，确保下个 WorkingSet.Tick 的 `StickyBindProxy` 看到"未绑"→触发 `BindProxy` 挑新 alive 代理。若跳过 Release，粘性会 skip，重绑不发生——测试 `TestJanitorTick_ReleaseErrorSkipsButContinues` 守住这条不变式。
- **types 一致性**：`store.DeadProxyAccount{JID, CountryCode}` 与 `control.DeadProxyAccount{JID, CC}` 是两个包各自的类型，main 显式转换（Task 4 Step 3）——不共享，避免 control import store。
- 与调度闭环计划（[`2026-07-02-scheduler-loop-closure.md`](2026-07-02-scheduler-loop-closure.md)）无耦合，可独立先后执行。
