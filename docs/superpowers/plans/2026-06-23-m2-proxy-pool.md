# M2: 代理池与绑定 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 4G 代理池的原子抢占绑定、释放、失败下线/成功回血,以及把绑定的代理应用到 whatsmeow 客户端。

**Architecture:** 单事务内完成"归还旧代理 → `FOR UPDATE SKIP LOCKED` 抢一行可用代理并原子自增计数 → 回写 account_devices",500 路并发互不阻塞且不超卖;失败计数越阈值用偏索引自动剔除死代理。

**Tech Stack:** Go 1.22+,jackc/pgx/v5 (pgxpool),go.mau.fi/whatsmeow,testcontainers-go。

## Global Constraints

- 见 `2026-06-23-wa-distribution-roadmap.md` 的 Global Constraints,逐条适用。
- 模块 `github.com/acme/wadist`;代理逻辑置于既有 `store` 包(`internal/store/`),复用 `(*Manager).bizPool`。
- 占位符 `$1`-style;方言 "postgres";所有 DB 调用带 ctx;错误 `%w` 包裹。
- 迁移自幂等(`IF NOT EXISTS` / `DO $$ … duplicate_object` / `ADD COLUMN IF NOT EXISTS` / `DROP TRIGGER IF EXISTS`)。
- 代理 URL 在 M2 暂以明文存储;M10 安全合规里改为信封加密(届时 `proxy_url` → `proxy_url_enc`)。本里程碑不做加密。
- 集成测试用 testcontainers 真起 Postgres;本地运行需 `TESTCONTAINERS_RYUK_DISABLED=true`;`-race` 需 gcc(已装)。

## Dependencies(来自 M1,已存在)

- `(*Manager).bizPool *pgxpool.Pool`(`manager.go`)。
- `account_devices` 表(`migrations/0001_account_devices.sql`)与 `touch_updated_at()` 函数(0001 中 `CREATE OR REPLACE`,可复用)。
- 测试夹具 `testDSN(t)`(`testsupport_test.go`)。
- `newManager(ctx, Config, waLog.Logger)`(测试用,非单例)。

## VERIFIED API facts(已对装好的依赖核验,直接照用)

- `pgx.BeginTxFunc(ctx, db pgx.BeginTxer, pgx.TxOptions{}, func(pgx.Tx) error) error` 存在;`*pgxpool.Pool` 满足 `BeginTxer`(有 `BeginTx`)。
- `pgx.ErrNoRows` 用于 `errors.Is` 判空行。
- `(*whatsmeow.Client).SetProxyAddress(addr string, opts ...whatsmeow.SetProxyOptions) error` — 支持 `socks5://` / `http://` / `https://`;空串会**取消**代理(返回 nil),故 `ApplyProxy` 必须自行拦空绑定。
- `whatsmeow.NewClient(deviceStore *store.Device, log waLog.Logger) *whatsmeow.Client`。

## File Structure

- `migrations/0002_proxy_pool.sql` — `proxy_type_t` 枚举 + `proxy_pool` 表 + `account_devices.proxy_id/proxy_url_cache` 列 + 偏索引 + 触发器。
- `internal/store/proxy.go` — `ProxyBinding`、错误哨兵、`BindProxy`、`releaseWithinTx`、`ReleaseProxy`、`ReportProxyFailure`、`ReportProxySuccess`、`ListAccountsByDeadProxies`、`ApplyProxy`。
- `internal/store/proxy_testsupport_test.go` — `applyMigrations`、`seedProxy`、`seedAccount` 测试助手。
- `internal/store/proxy_test.go` — 各集成/单元测试。

**Interfaces Produced(下游 M6 分发、M9 接管消费):**
- `ProxyBinding{ ProxyID int64; ProxyURL, ProxyType, Country string }`
- `ErrNoProxyAvailable, ErrAccountMissing error`
- `(*Manager).BindProxy(ctx, accountJID, countryCode string) (*ProxyBinding, error)`
- `(*Manager).ReleaseProxy(ctx, accountJID string) error`
- `(*Manager).ReportProxyFailure(ctx, proxyID int64) (dead bool, err error)`
- `(*Manager).ReportProxySuccess(ctx, proxyID int64, latencyMs int) error`
- `(*Manager).ListAccountsByDeadProxies(ctx, tenantID int64, limit int) ([]string, error)`
- `ApplyProxy(client *whatsmeow.Client, b *ProxyBinding) error`

---

### Task 1: 迁移 0002 + 测试助手

**Files:**
- Create: `migrations/0002_proxy_pool.sql`
- Create: `internal/store/proxy_testsupport_test.go`
- Test: `internal/store/proxy_migration_test.go`

**Interfaces:**
- Produces: 测试助手 `applyMigrations(t, ctx, pool)`(按序应用 `migrations/*.sql`,跳过 `*.down.sql`);`seedProxy(t, ctx, pool, url, country string, maxBindings int) int64`;`seedAccount(t, ctx, pool, tenantID int64, jid, phone string)`。

- [ ] **Step 1: 写迁移(自幂等)**

```sql
-- migrations/0002_proxy_pool.sql
DO $$ BEGIN
    CREATE TYPE proxy_type_t AS ENUM ('socks5','http','https');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS proxy_pool (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    proxy_url        TEXT        NOT NULL UNIQUE,
    proxy_type       proxy_type_t NOT NULL DEFAULT 'socks5',
    country_code     CHAR(2)     NOT NULL,
    is_alive         BOOLEAN     NOT NULL DEFAULT TRUE,
    latency_ms       INT         NOT NULL DEFAULT 0,
    usage_count      BIGINT      NOT NULL DEFAULT 0,
    failure_count    INT         NOT NULL DEFAULT 0,
    max_bindings     INT         NOT NULL DEFAULT 1,
    current_bindings INT         NOT NULL DEFAULT 0,
    last_check_at    TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_bindings CHECK (current_bindings >= 0 AND current_bindings <= max_bindings)
);

CREATE INDEX IF NOT EXISTS idx_proxy_available
    ON proxy_pool (country_code, usage_count)
    WHERE is_alive = TRUE AND current_bindings < max_bindings;

ALTER TABLE account_devices ADD COLUMN IF NOT EXISTS proxy_id BIGINT
    REFERENCES proxy_pool(id) ON DELETE SET NULL;
ALTER TABLE account_devices ADD COLUMN IF NOT EXISTS proxy_url_cache TEXT;
CREATE INDEX IF NOT EXISTS idx_acc_proxy ON account_devices (proxy_id);

DROP TRIGGER IF EXISTS trg_proxy_touch ON proxy_pool;
CREATE TRIGGER trg_proxy_touch BEFORE UPDATE ON proxy_pool
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
```

- [ ] **Step 2: 写测试助手**

```go
// internal/store/proxy_testsupport_test.go
package store

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// applyMigrations 按文件名顺序应用所有 migrations/*.sql(跳过 *.down.sql)。
func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, ".down.sql") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
}

// seedProxy inserts one proxy and returns its id.
func seedProxy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, url, country string, maxBindings int) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx,
		`INSERT INTO proxy_pool (proxy_url, proxy_type, country_code, max_bindings)
		 VALUES ($1, 'socks5', $2, $3) RETURNING id`,
		url, country, maxBindings).Scan(&id)
	if err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	return id
}

// seedAccount inserts one active account_devices row.
func seedAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID int64, jid, phone string) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status)
		 VALUES ($1, $2, $3, 'active')`,
		tenantID, jid, phone)
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
}
```

- [ ] **Step 3: 写幂等测试**

```go
// internal/store/proxy_migration_test.go
package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrations_AllIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	applyMigrations(t, ctx, pool) // pass 1
	applyMigrations(t, ctx, pool) // pass 2 — must not error

	// proxy_pool exists and account_devices.proxy_id column exists
	var nTab, nCol int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='proxy_pool' AND table_schema='public'`).
		Scan(&nTab); err != nil {
		t.Fatalf("verify table: %v", err)
	}
	if nTab != 1 {
		t.Fatalf("proxy_pool table count = %d, want 1", nTab)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_name='account_devices' AND column_name='proxy_id' AND table_schema='public'`).
		Scan(&nCol); err != nil {
		t.Fatalf("verify column: %v", err)
	}
	if nCol != 1 {
		t.Fatalf("account_devices.proxy_id column count = %d, want 1", nCol)
	}
}
```

- [ ] **Step 4: 运行验证通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestMigrations_AllIdempotent -v`
Expected: PASS(应用两遍无错,proxy_pool 表与 proxy_id 列存在)。

- [ ] **Step 5: 提交**

```bash
git add migrations/0002_proxy_pool.sql internal/store/proxy_testsupport_test.go internal/store/proxy_migration_test.go
git commit -m "feat(store): add proxy_pool migration + test helpers"
```

---

### Task 2: BindProxy 原子抢占 + 绑定(含并发不超卖)

**Files:**
- Create: `internal/store/proxy.go`
- Test: `internal/store/proxy_test.go`

**Interfaces:**
- Consumes: `applyMigrations/seedProxy/seedAccount`(Task 1),`newManager`(M1)。
- Produces: `ProxyBinding`、`ErrNoProxyAvailable`、`ErrAccountMissing`、`(*Manager).BindProxy`、`releaseWithinTx`(包内)。

- [ ] **Step 1: 写失败测试**

```go
// internal/store/proxy_test.go
package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func newManagerWithSchema(t *testing.T) (*Manager, context.Context) {
	t.Helper()
	ctx := context.Background()
	m, err := newManager(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(m.Close)
	applyMigrations(t, ctx, m.BizPool())
	return m, ctx
}

func TestBindProxy_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://u:p@h:1080", "US", 1)
	seedAccount(t, ctx, m.BizPool(), 1, "111@s.whatsapp.net", "15550000001")

	b, err := m.BindProxy(ctx, "111@s.whatsapp.net", "US")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if b.ProxyID != pid || b.ProxyURL != "socks5://u:p@h:1080" || b.Country != "US" {
		t.Fatalf("binding wrong: %+v", b)
	}

	var cur, usage int
	var boundID int64
	m.BizPool().QueryRow(ctx, `SELECT current_bindings, usage_count FROM proxy_pool WHERE id=$1`, pid).Scan(&cur, &usage)
	if cur != 1 || usage != 1 {
		t.Fatalf("counts: current=%d usage=%d, want 1/1", cur, usage)
	}
	m.BizPool().QueryRow(ctx, `SELECT proxy_id FROM account_devices WHERE account_jid='111@s.whatsapp.net'`).Scan(&boundID)
	if boundID != pid {
		t.Fatalf("account proxy_id = %d, want %d", boundID, pid)
	}
}

func TestBindProxy_NoCapacity(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	seedAccount(t, ctx, m.BizPool(), 1, "111@s.whatsapp.net", "15550000001")
	// no proxies seeded for US
	if _, err := m.BindProxy(ctx, "111@s.whatsapp.net", "US"); !errors.Is(err, ErrNoProxyAvailable) {
		t.Fatalf("err = %v, want ErrNoProxyAvailable", err)
	}
}

func TestBindProxy_NoOversellUnderConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	// 3 proxies, each capacity 1 → at most 3 accounts can bind
	for i := 0; i < 3; i++ {
		seedProxy(t, ctx, m.BizPool(), "socks5://h:"+string(rune('a'+i)), "US", 1)
	}
	const n = 10
	jids := make([]string, n)
	for i := 0; i < n; i++ {
		jids[i] = "acc" + string(rune('A'+i)) + "@s.whatsapp.net"
		seedAccount(t, ctx, m.BizPool(), 1, jids[i], "1555000000"+string(rune('0'+i)))
	}

	var success int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(jid string) {
			defer wg.Done()
			if _, err := m.BindProxy(ctx, jid, "US"); err == nil {
				atomic.AddInt64(&success, 1)
			}
		}(jids[i])
	}
	wg.Wait()

	if success != 3 {
		t.Fatalf("successful binds = %d, want exactly 3 (capacity)", success)
	}
	// no proxy oversold
	var maxCur int
	m.BizPool().QueryRow(ctx, `SELECT COALESCE(max(current_bindings),0) FROM proxy_pool`).Scan(&maxCur)
	if maxCur > 1 {
		t.Fatalf("oversell detected: max current_bindings = %d", maxCur)
	}
}
```

- [ ] **Step 2: 运行验证失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestBindProxy -v`
Expected: FAIL,`m.BindProxy undefined`。

- [ ] **Step 3: 实现 proxy.go**

```go
// internal/store/proxy.go
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var (
	ErrNoProxyAvailable = errors.New("store: no alive proxy with free capacity")
	ErrAccountMissing   = errors.New("store: account_devices row not found")
)

// ProxyBinding is a snapshot of a successful proxy acquisition.
type ProxyBinding struct {
	ProxyID   int64
	ProxyURL  string
	ProxyType string
	Country   string
}

// BindProxy atomically: (1) returns any proxy currently bound to the account,
// (2) acquires one alive proxy with free capacity in the given country via
// FOR UPDATE SKIP LOCKED, incrementing its counters, (3) writes the binding to
// account_devices. All in one tx — any failure rolls back the counter increment.
func (m *Manager) BindProxy(ctx context.Context, accountJID, countryCode string) (*ProxyBinding, error) {
	var binding *ProxyBinding
	err := pgx.BeginTxFunc(ctx, m.bizPool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := releaseWithinTx(ctx, tx, accountJID); err != nil {
			return err
		}

		const acquireSQL = `
WITH picked AS (
    SELECT id FROM proxy_pool
     WHERE is_alive = TRUE AND current_bindings < max_bindings AND country_code = $1
     ORDER BY usage_count ASC, latency_ms ASC
     FOR UPDATE SKIP LOCKED
     LIMIT 1
)
UPDATE proxy_pool p
   SET current_bindings = p.current_bindings + 1,
       usage_count      = p.usage_count + 1
  FROM picked
 WHERE p.id = picked.id
RETURNING p.id, p.proxy_url, p.proxy_type::text, p.country_code;`

		var b ProxyBinding
		err := tx.QueryRow(ctx, acquireSQL, countryCode).
			Scan(&b.ProxyID, &b.ProxyURL, &b.ProxyType, &b.Country)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoProxyAvailable
		}
		if err != nil {
			return fmt.Errorf("acquire proxy: %w", err)
		}

		ct, err := tx.Exec(ctx,
			`UPDATE account_devices SET proxy_id=$1, proxy_url_cache=$2 WHERE account_jid=$3`,
			b.ProxyID, b.ProxyURL, accountJID)
		if err != nil {
			return fmt.Errorf("bind proxy to account: %w", err)
		}
		if ct.RowsAffected() == 0 {
			return ErrAccountMissing
		}
		binding = &b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return binding, nil
}

// releaseWithinTx returns the account's current proxy (if any) within an open tx.
// Idempotent: no-op when unbound. GREATEST guards against a negative counter.
func releaseWithinTx(ctx context.Context, tx pgx.Tx, accountJID string) error {
	var oldProxyID *int64
	err := tx.QueryRow(ctx,
		`SELECT proxy_id FROM account_devices WHERE account_jid=$1 FOR UPDATE`,
		accountJID).Scan(&oldProxyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccountMissing
	}
	if err != nil {
		return fmt.Errorf("lock account row: %w", err)
	}
	if oldProxyID == nil {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE proxy_pool SET current_bindings = GREATEST(current_bindings-1, 0) WHERE id=$1`,
		*oldProxyID); err != nil {
		return fmt.Errorf("decrement old proxy: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE account_devices SET proxy_id=NULL, proxy_url_cache=NULL WHERE account_jid=$1`,
		accountJID); err != nil {
		return fmt.Errorf("clear account binding: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: 运行验证通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestBindProxy -race -v`
Expected: PASS(成功绑定计数正确;无容量返回 ErrNoProxyAvailable;10 并发仅 3 成功且无超卖)。

- [ ] **Step 5: 提交**

```bash
git add internal/store/proxy.go internal/store/proxy_test.go
git commit -m "feat(store): atomic BindProxy with SKIP LOCKED, no oversell under concurrency"
```

---

### Task 3: ReleaseProxy

**Files:**
- Modify: `internal/store/proxy.go`
- Test: `internal/store/proxy_release_test.go`

**Interfaces:**
- Consumes: `releaseWithinTx`、`BindProxy`(Task 2)。
- Produces: `(*Manager).ReleaseProxy(ctx, accountJID string) error`。

- [ ] **Step 1: 写失败测试**

```go
// internal/store/proxy_release_test.go
package store

import (
	"context"
	"testing"
)

func TestReleaseProxy_DecrementsAndIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://h:1080", "US", 1)
	seedAccount(t, ctx, m.BizPool(), 1, "111@s.whatsapp.net", "15550000001")
	if _, err := m.BindProxy(ctx, "111@s.whatsapp.net", "US"); err != nil {
		t.Fatalf("bind: %v", err)
	}

	if err := m.ReleaseProxy(ctx, "111@s.whatsapp.net"); err != nil {
		t.Fatalf("release: %v", err)
	}
	assertReleased := func() {
		var cur int
		var pidNull *int64
		m.BizPool().QueryRow(ctx, `SELECT current_bindings FROM proxy_pool WHERE id=$1`, pid).Scan(&cur)
		m.BizPool().QueryRow(ctx, `SELECT proxy_id FROM account_devices WHERE account_jid='111@s.whatsapp.net'`).Scan(&pidNull)
		if cur != 0 {
			t.Fatalf("current_bindings = %d, want 0", cur)
		}
		if pidNull != nil {
			t.Fatalf("account proxy_id = %v, want NULL", *pidNull)
		}
	}
	assertReleased()

	// idempotent: second release is a no-op, must not error or go negative
	if err := m.ReleaseProxy(ctx, "111@s.whatsapp.net"); err != nil {
		t.Fatalf("second release: %v", err)
	}
	assertReleased()
}
```

- [ ] **Step 2: 运行验证失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestReleaseProxy -v`
Expected: FAIL,`m.ReleaseProxy undefined`。

- [ ] **Step 3: 实现 ReleaseProxy(追加到 proxy.go)**

```go
// ReleaseProxy returns the account's bound proxy. Idempotent.
func (m *Manager) ReleaseProxy(ctx context.Context, accountJID string) error {
	return pgx.BeginTxFunc(ctx, m.bizPool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return releaseWithinTx(ctx, tx, accountJID)
	})
}
```

- [ ] **Step 4: 运行验证通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestReleaseProxy -race -v`
Expected: PASS(计数归零、account 解绑、二次释放幂等)。

- [ ] **Step 5: 提交**

```bash
git add internal/store/proxy.go internal/store/proxy_release_test.go
git commit -m "feat(store): idempotent ReleaseProxy"
```

---

### Task 4: 失败下线 / 成功回血 / 死代理账号枚举

**Files:**
- Modify: `internal/store/proxy.go`
- Test: `internal/store/proxy_health_test.go`

**Interfaces:**
- Consumes: `seedProxy/seedAccount`(Task 1)。
- Produces: 常量 `proxyFailureThreshold = 5`;`(*Manager).ReportProxyFailure(ctx, proxyID int64) (dead bool, err error)`;`(*Manager).ReportProxySuccess(ctx, proxyID int64, latencyMs int) error`;`(*Manager).ListAccountsByDeadProxies(ctx, tenantID int64, limit int) ([]string, error)`。

- [ ] **Step 1: 写失败测试**

```go
// internal/store/proxy_health_test.go
package store

import (
	"context"
	"testing"
)

func TestReportProxyFailure_DisablesAtThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://h:1080", "US", 1)

	var dead bool
	var err error
	for i := 1; i < proxyFailureThreshold; i++ { // first threshold-1 failures: still alive
		if dead, err = m.ReportProxyFailure(ctx, pid); err != nil {
			t.Fatalf("failure %d: %v", i, err)
		}
		if dead {
			t.Fatalf("dead too early at failure %d", i)
		}
	}
	if dead, err = m.ReportProxyFailure(ctx, pid); err != nil || !dead { // threshold-th: dead
		t.Fatalf("at threshold dead=%v err=%v, want dead=true", dead, err)
	}
	var alive bool
	m.BizPool().QueryRow(ctx, `SELECT is_alive FROM proxy_pool WHERE id=$1`, pid).Scan(&alive)
	if alive {
		t.Fatal("proxy should be is_alive=false after threshold")
	}
}

func TestReportProxySuccess_ResetsAndRevives(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://h:1080", "US", 1)
	for i := 0; i < proxyFailureThreshold; i++ {
		m.ReportProxyFailure(ctx, pid)
	}
	if err := m.ReportProxySuccess(ctx, pid, 123); err != nil {
		t.Fatalf("success: %v", err)
	}
	var alive bool
	var fc, lat int
	m.BizPool().QueryRow(ctx, `SELECT is_alive, failure_count, latency_ms FROM proxy_pool WHERE id=$1`, pid).Scan(&alive, &fc, &lat)
	if !alive || fc != 0 || lat != 123 {
		t.Fatalf("after success: alive=%v fc=%d lat=%d, want true/0/123", alive, fc, lat)
	}
}

func TestListAccountsByDeadProxies(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	pid := seedProxy(t, ctx, m.BizPool(), "socks5://h:1080", "US", 1)
	seedAccount(t, ctx, m.BizPool(), 7, "live@s.whatsapp.net", "15550000001")
	if _, err := m.BindProxy(ctx, "live@s.whatsapp.net", "US"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	// kill the proxy
	for i := 0; i < proxyFailureThreshold; i++ {
		m.ReportProxyFailure(ctx, pid)
	}
	jids, err := m.ListAccountsByDeadProxies(ctx, 7, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jids) != 1 || jids[0] != "live@s.whatsapp.net" {
		t.Fatalf("dead-proxy accounts = %v, want [live@s.whatsapp.net]", jids)
	}
}
```

- [ ] **Step 2: 运行验证失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run 'TestReportProxy|TestListAccountsByDeadProxies' -v`
Expected: FAIL,`m.ReportProxyFailure undefined`。

- [ ] **Step 3: 实现(追加到 proxy.go)**

```go
// proxyFailureThreshold: consecutive failures before a proxy is taken offline.
const proxyFailureThreshold = 5

// ReportProxyFailure records one proxy-layer failure; auto-disables at threshold.
// Returns whether the proxy is now considered dead.
func (m *Manager) ReportProxyFailure(ctx context.Context, proxyID int64) (dead bool, err error) {
	const sql = `
UPDATE proxy_pool
   SET failure_count = failure_count + 1,
       is_alive = (failure_count + 1 < $2)
 WHERE id = $1
RETURNING NOT is_alive;`
	if err = m.bizPool.QueryRow(ctx, sql, proxyID, proxyFailureThreshold).Scan(&dead); err != nil {
		return false, fmt.Errorf("report proxy failure: %w", err)
	}
	return dead, nil
}

// ReportProxySuccess clears the failure count, revives the proxy, refreshes latency.
func (m *Manager) ReportProxySuccess(ctx context.Context, proxyID int64, latencyMs int) error {
	_, err := m.bizPool.Exec(ctx,
		`UPDATE proxy_pool
		    SET failure_count=0, is_alive=TRUE, latency_ms=$2, last_check_at=now()
		  WHERE id=$1`, proxyID, latencyMs)
	if err != nil {
		return fmt.Errorf("report proxy success: %w", err)
	}
	return nil
}

// ListAccountsByDeadProxies returns active accounts bound to dead proxies, for rebind.
func (m *Manager) ListAccountsByDeadProxies(ctx context.Context, tenantID int64, limit int) ([]string, error) {
	const sql = `
SELECT a.account_jid
  FROM account_devices a
  JOIN proxy_pool p ON p.id = a.proxy_id
 WHERE a.tenant_id = $1 AND p.is_alive = FALSE AND a.ban_status = 'active'
 LIMIT $2;`
	rows, err := m.bizPool.Query(ctx, sql, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("query dead-proxy accounts: %w", err)
	}
	defer rows.Close()

	var jids []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, fmt.Errorf("scan jid: %w", err)
		}
		jids = append(jids, jid)
	}
	return jids, rows.Err()
}
```

- [ ] **Step 4: 运行验证通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run 'TestReportProxy|TestListAccountsByDeadProxies' -race -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/store/proxy.go internal/store/proxy_health_test.go
git commit -m "feat(store): proxy failure auto-disable, success revive, dead-proxy account listing"
```

---

### Task 5: ApplyProxy(应用到 whatsmeow 客户端)

**Files:**
- Modify: `internal/store/proxy.go`
- Test: `internal/store/proxy_apply_test.go`

**Interfaces:**
- Consumes: `ProxyBinding`(Task 2),`(*Manager).NewDeviceStore`(M1)。
- Produces: `ApplyProxy(client *whatsmeow.Client, b *ProxyBinding) error`。

- [ ] **Step 1: 写失败测试**

```go
// internal/store/proxy_apply_test.go
package store

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestApplyProxy_EmptyBinding(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	client := whatsmeow.NewClient(m.NewDeviceStore(ctx), waLog.Noop)
	if err := ApplyProxy(client, nil); err == nil {
		t.Fatal("expected error for nil binding")
	}
	if err := ApplyProxy(client, &ProxyBinding{ProxyURL: ""}); err == nil {
		t.Fatal("expected error for empty proxy url")
	}
}

func TestApplyProxy_ValidSocks5(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newManagerWithSchema(t)
	client := whatsmeow.NewClient(m.NewDeviceStore(ctx), waLog.Noop)
	b := &ProxyBinding{ProxyID: 1, ProxyURL: "socks5://user:pass@127.0.0.1:1080", ProxyType: "socks5", Country: "US"}
	if err := ApplyProxy(client, b); err != nil {
		t.Fatalf("apply valid socks5: %v", err)
	}
}
```

- [ ] **Step 2: 运行验证失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestApplyProxy -v`
Expected: FAIL,`undefined: ApplyProxy`。

- [ ] **Step 3: 实现(追加到 proxy.go;在 import 块加入 whatsmeow)**

在 `proxy.go` 顶部 import 加入 `"go.mau.fi/whatsmeow"`,并追加:

```go
// ApplyProxy applies a bound proxy to a whatsmeow client. Must be called before
// client.Connect(). Rejects an empty binding (SetProxyAddress("") would silently
// UNSET the proxy, defeating per-account isolation).
func ApplyProxy(client *whatsmeow.Client, b *ProxyBinding) error {
	if b == nil || b.ProxyURL == "" {
		return errors.New("store: empty proxy binding")
	}
	if err := client.SetProxyAddress(b.ProxyURL); err != nil {
		return fmt.Errorf("apply proxy %s: %w", b.ProxyURL, err)
	}
	return nil
}
```

- [ ] **Step 4: 运行验证通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/store/ -run TestApplyProxy -race -v`
Expected: PASS(空绑定报错;合法 socks5 应用成功)。

- [ ] **Step 5: 提交**

```bash
git add internal/store/proxy.go internal/store/proxy_apply_test.go
git commit -m "feat(store): ApplyProxy to whatsmeow client with empty-binding guard"
```

---

## Self-Review

- **Spec 覆盖**:proxy_pool schema ✓(T1)、BindProxy CTE+SKIP LOCKED ✓(T2)、ReleaseProxy ✓(T3)、ReportProxyFailure/Success ✓(T4)、死代理重绑枚举 ✓(T4)、ApplyProxy ✓(T5)。roadmap M2 全覆盖。
- **占位符扫描**:无 TBD/TODO,每步含完整代码与命令。
- **类型一致性**:`ProxyBinding` 字段、`BindProxy/ReleaseProxy/ReportProxyFailure/ReportProxySuccess/ListAccountsByDeadProxies/ApplyProxy` 签名跨任务一致;`releaseWithinTx` 在 T2 定义、T3 复用;`proxyFailureThreshold` 在 T4 定义并被测试引用。
- **并发安全**:T2 含 10 并发不超卖断言(SKIP LOCKED + 单事务计数);`releaseWithinTx` 用 `FOR UPDATE` 锁账号行 + `GREATEST` 防负。
- **范围边界(显式)**:代理 URL 明文存储,加密留 M10;`ApplyProxy` 仅设置代理不连接;按国家路由依赖调用方传入 countryCode(account_devices 不引入 country_code 列)。
