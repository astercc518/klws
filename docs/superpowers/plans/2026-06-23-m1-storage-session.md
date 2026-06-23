# M1: 存储与会话持久化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 whatsmeow 会话的 PostgreSQL 持久化、连接池配置、按账号获取凭证的工厂函数,以及跨进程独占的 advisory lock。

**Architecture:** 用 pgx 的 `stdlib` 驱动拿到自管连接池的 `*sql.DB`,经 `sqlstore.NewWithDB` 注入 whatsmeow Container;业务表走独立 `pgxpool`;账号互斥用 Postgres session 级 advisory lock(持锁连接独占)。

**Tech Stack:** Go 1.22+,go.mau.fi/whatsmeow,jackc/pgx/v5(stdlib + pgxpool),testcontainers-go。

## Global Constraints

- 见主路线图 `2026-06-23-wa-distribution-roadmap.md` 的 Global Constraints,逐条适用。
- 模块路径占位:`github.com/acme/wadist`。
- 方言字符串恒为 `"postgres"`;占位符 `$1`。
- 所有 DB 调用带 ctx 超时;错误用 `%w` 包裹。

---

## File Structure

- `internal/store/config.go` — `Config` 结构与 `withDefaults`。
- `internal/store/manager.go` — `Manager`、`Init`、`newManager`、`BizPool`、`Close`。
- `internal/store/device.go` — `GetDeviceStore`、`NewDeviceStore`、`ErrDeviceNotFound`。
- `internal/store/lock.go` — `DeviceLock`、`AcquireDeviceLock`、`advisoryKey`、`ErrDeviceLocked`、`Healthy`、`Release`。
- `migrations/0001_account_devices.sql` — `ban_status_t` 枚举 + `account_devices` 表 + `updated_at` 触发器(自幂等)。
- `internal/store/testsupport_test.go` — testcontainers PG 测试夹具。
- `internal/store/*_test.go` — 各单元/集成测试。

**Interfaces Produced(后续里程碑消费):**
- `store.Config{ DSN string; MaxOpenConns,MinIdleConns int32; ConnMaxLifetime,ConnMaxIdleTime time.Duration; NodeID string }`
- `store.Init(ctx, cfg, logger) (*Manager, error)`
- `(*Manager).BizPool() *pgxpool.Pool`
- `(*Manager).Close()`
- `(*Manager).GetDeviceStore(ctx, accountJID string) (*store.Device, error)` — 未注册返回 `ErrDeviceNotFound`
- `(*Manager).NewDeviceStore(ctx) *store.Device`
- `(*Manager).AcquireDeviceLock(ctx, accountJID string) (*DeviceLock, error)` — 已被占返回 `ErrDeviceLocked`
- `(*DeviceLock).Release(ctx)`,`(*DeviceLock).Healthy(ctx) bool`

---

### Task 1: Config 与默认值

**Files:**
- Create: `internal/store/config.go`
- Test: `internal/store/config_test.go`

**Interfaces:**
- Produces: `Config` 结构体;`(*Config).withDefaults()`。

- [ ] **Step 1: 写失败测试**

```go
// internal/store/config_test.go
package store

import (
	"testing"
	"time"
)

func TestConfig_withDefaults(t *testing.T) {
	c := Config{DSN: "postgres://x"}
	c.withDefaults()
	if c.MaxOpenConns != 50 {
		t.Fatalf("MaxOpenConns = %d, want 50", c.MaxOpenConns)
	}
	if c.MinIdleConns != 5 {
		t.Fatalf("MinIdleConns = %d, want 5", c.MinIdleConns)
	}
	if c.ConnMaxLifetime != 30*time.Minute {
		t.Fatalf("ConnMaxLifetime = %v, want 30m", c.ConnMaxLifetime)
	}
	if c.ConnMaxIdleTime != 5*time.Minute {
		t.Fatalf("ConnMaxIdleTime = %v, want 5m", c.ConnMaxIdleTime)
	}
}

func TestConfig_withDefaults_keepsExplicit(t *testing.T) {
	c := Config{DSN: "x", MaxOpenConns: 80}
	c.withDefaults()
	if c.MaxOpenConns != 80 {
		t.Fatalf("explicit MaxOpenConns overwritten: %d", c.MaxOpenConns)
	}
}
```

- [ ] **Step 2: 运行验证失败**

Run: `go test ./internal/store/ -run TestConfig -v`
Expected: FAIL,`undefined: Config`。

- [ ] **Step 3: 实现 Config**

```go
// internal/store/config.go
package store

import "time"

// Config 连接池配置。MaxOpenConns 匹配 Postgres 承载力,而非 Goroutine 数。
type Config struct {
	DSN             string
	MaxOpenConns    int32
	MinIdleConns    int32
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	NodeID          string
}

func (c *Config) withDefaults() {
	if c.MaxOpenConns == 0 {
		c.MaxOpenConns = 50
	}
	if c.MinIdleConns == 0 {
		c.MinIdleConns = 5
	}
	if c.ConnMaxLifetime == 0 {
		c.ConnMaxLifetime = 30 * time.Minute
	}
	if c.ConnMaxIdleTime == 0 {
		c.ConnMaxIdleTime = 5 * time.Minute
	}
}
```

- [ ] **Step 4: 运行验证通过**

Run: `go test ./internal/store/ -run TestConfig -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/store/config.go internal/store/config_test.go
git commit -m "feat(store): add Config with pool defaults"
```

---

### Task 2: account_devices 迁移 + 自幂等测试夹具

**Files:**
- Create: `migrations/0001_account_devices.sql`
- Create: `internal/store/testsupport_test.go`
- Test: `internal/store/migration_test.go`

**Interfaces:**
- Produces: 测试夹具 `testDSN(t) string`(起一次性 PG,返回 DSN)。

- [ ] **Step 1: 写迁移(自幂等)**

```sql
-- migrations/0001_account_devices.sql
DO $$ BEGIN
    CREATE TYPE ban_status_t AS ENUM ('active','flagged','banned','logged_out','init');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS account_devices (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id         BIGINT      NOT NULL,
    account_jid       TEXT        NOT NULL UNIQUE,
    phone_number      TEXT        NOT NULL,
    push_name         TEXT,
    ban_status        ban_status_t NOT NULL DEFAULT 'init',
    ban_checked_at    TIMESTAMPTZ,
    owner_node        TEXT,
    last_connected_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_acc_tenant ON account_devices (tenant_id);

CREATE OR REPLACE FUNCTION touch_updated_at() RETURNS trigger AS $$
BEGIN NEW.updated_at = now(); RETURN NEW; END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_acc_touch ON account_devices;
CREATE TRIGGER trg_acc_touch BEFORE UPDATE ON account_devices
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
```

- [ ] **Step 2: 写测试夹具**

```go
// internal/store/testsupport_test.go
package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// testDSN 起一次性 Postgres,返回 DSN;测试结束自动销毁。
func testDSN(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("app_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5432/tcp").WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	return dsn
}

// readMigration 读取 0001 迁移内容。
func readMigration(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../migrations/0001_account_devices.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	return string(b)
}
```

- [ ] **Step 3: 写幂等测试**

```go
// internal/store/migration_test.go
package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigration_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	sql := readMigration(t)
	for i := 0; i < 2; i++ { // 连续两次,第二次不得报错
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("apply migration #%d: %v", i+1, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='account_devices'`).
		Scan(&n); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if n != 1 {
		t.Fatalf("account_devices table count = %d, want 1", n)
	}
}
```

- [ ] **Step 4: 运行验证通过**

Run: `go test ./internal/store/ -run TestMigration -v`
Expected: PASS(两次 apply 均成功,表存在)。

- [ ] **Step 5: 提交**

```bash
git add migrations/0001_account_devices.sql internal/store/testsupport_test.go internal/store/migration_test.go
git commit -m "feat(store): add idempotent account_devices migration + test fixture"
```

---

### Task 3: Manager 初始化(自管池 + whatsmeow Container)

**Files:**
- Create: `internal/store/manager.go`
- Test: `internal/store/manager_test.go`

**Interfaces:**
- Consumes: `Config`(Task 1),`testDSN`(Task 2)。
- Produces: `Init(ctx, Config, waLog.Logger) (*Manager, error)`;`(*Manager).BizPool() *pgxpool.Pool`;`(*Manager).Close()`;字段 `container *sqlstore.Container`、`bizPool *pgxpool.Pool`、`cfg Config`。

- [ ] **Step 1: 写失败测试**

```go
// internal/store/manager_test.go
package store

import (
	"context"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestManager_Init_UpgradesAndPings(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, err := Init(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer m.Close()

	// whatsmeow Upgrade 应已建出会话表
	var n int
	if err := m.BizPool().QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name='whatsmeow_device'`).
		Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("whatsmeow_device table count = %d, want 1", n)
	}
}
```

> 注意:`Init` 用 `sync.Once` 全局单例,测试内多次调用会复用首个 DSN。本测试为进程内唯一 `Init`;若同包内多个集成测试都需独立 DB,改用未导出的 `newManager(ctx,cfg,log)` 直接构造(见 Step 3 暴露它供测试)。

- [ ] **Step 2: 运行验证失败**

Run: `go test ./internal/store/ -run TestManager_Init -v`
Expected: FAIL,`undefined: Init`。

- [ ] **Step 3: 实现 Manager**

```go
// internal/store/manager.go
package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // 注册 "pgx" database/sql 驱动
	"github.com/jackc/pgx/v5/pgxpool"

	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type Manager struct {
	cfg       Config
	container *sqlstore.Container
	bizPool   *pgxpool.Pool
	log       waLog.Logger
}

var (
	mgr     *Manager
	mgrOnce sync.Once
	mgrErr  error
)

// Init 幂等初始化全局单例。
func Init(ctx context.Context, cfg Config, logger waLog.Logger) (*Manager, error) {
	mgrOnce.Do(func() {
		cfg.withDefaults()
		mgr, mgrErr = newManager(ctx, cfg, logger)
	})
	return mgr, mgrErr
}

// newManager 构造一个独立 Manager(不走单例),供测试与多实例场景。
func newManager(ctx context.Context, cfg Config, logger waLog.Logger) (*Manager, error) {
	cfg.withDefaults()

	sqlDB, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open whatsmeow sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(int(cfg.MaxOpenConns))
	sqlDB.SetMaxIdleConns(int(cfg.MinIdleConns))
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping whatsmeow db: %w", err)
	}

	container := sqlstore.NewWithDB(sqlDB, "postgres", logger)
	if err := container.Upgrade(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("upgrade whatsmeow schema: %w", err)
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("parse biz pool config: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxOpenConns
	poolCfg.MinConns = cfg.MinIdleConns
	poolCfg.MaxConnLifetime = cfg.ConnMaxLifetime
	poolCfg.MaxConnIdleTime = cfg.ConnMaxIdleTime
	poolCfg.HealthCheckPeriod = 30 * time.Second

	bizPool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("create biz pool: %w", err)
	}

	return &Manager{cfg: cfg, container: container, bizPool: bizPool, log: logger}, nil
}

func (m *Manager) BizPool() *pgxpool.Pool { return m.bizPool }

func (m *Manager) Close() {
	if m.bizPool != nil {
		m.bizPool.Close()
	}
}
```

- [ ] **Step 4: 运行验证通过**

Run: `go test ./internal/store/ -run TestManager_Init -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/store/manager.go internal/store/manager_test.go
git commit -m "feat(store): Manager with self-managed pool and whatsmeow container"
```

---

### Task 4: GetDeviceStore / NewDeviceStore

**Files:**
- Create: `internal/store/device.go`
- Test: `internal/store/device_test.go`

**Interfaces:**
- Consumes: `newManager`(Task 3),`testDSN`(Task 2)。
- Produces: `ErrDeviceNotFound error`;`(*Manager).GetDeviceStore(ctx, accountJID string) (*store.Device, error)`;`(*Manager).NewDeviceStore(ctx) *store.Device`。

- [ ] **Step 1: 写失败测试**

```go
// internal/store/device_test.go
package store

import (
	"context"
	"errors"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestGetDeviceStore_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, err := newManager(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	defer m.Close()

	_, err = m.GetDeviceStore(ctx, "1234567890.0:0@s.whatsapp.net")
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("err = %v, want ErrDeviceNotFound", err)
	}
}

func TestGetDeviceStore_BadJID(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, _ := newManager(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	defer m.Close()

	if _, err := m.GetDeviceStore(ctx, "not-a-jid"); err == nil {
		t.Fatal("expected parse error for bad jid")
	}
}

func TestNewDeviceStore_NonNil(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, _ := newManager(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	defer m.Close()

	if d := m.NewDeviceStore(ctx); d == nil {
		t.Fatal("NewDeviceStore returned nil")
	}
}
```

- [ ] **Step 2: 运行验证失败**

Run: `go test ./internal/store/ -run TestGetDeviceStore -v`
Expected: FAIL,`m.GetDeviceStore undefined`。

- [ ] **Step 3: 实现 device.go**

```go
// internal/store/device.go
package store

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

var ErrDeviceNotFound = errors.New("store: device not registered")

// GetDeviceStore 实时从 Postgres 取指定账号的 whatsmeow 凭证。
// 未注册返回 ErrDeviceNotFound。返回的 Device 为该账号专属,不可跨账号复用。
func (m *Manager) GetDeviceStore(ctx context.Context, accountJID string) (*store.Device, error) {
	jid, err := types.ParseJID(accountJID)
	if err != nil {
		return nil, fmt.Errorf("parse jid %q: %w", accountJID, err)
	}
	device, err := m.container.GetDevice(ctx, jid)
	if err != nil {
		return nil, fmt.Errorf("get device %s: %w", jid, err)
	}
	if device == nil {
		return nil, ErrDeviceNotFound
	}
	return device, nil
}

// NewDeviceStore 为新账号创建空白 Device,供后续扫码/配对。
func (m *Manager) NewDeviceStore(_ context.Context) *store.Device {
	return m.container.NewDevice()
}
```

- [ ] **Step 4: 运行验证通过**

Run: `go test ./internal/store/ -run 'TestGetDeviceStore|TestNewDeviceStore' -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/store/device.go internal/store/device_test.go
git commit -m "feat(store): GetDeviceStore/NewDeviceStore factory functions"
```

---

### Task 5: 跨进程独占 advisory lock

**Files:**
- Create: `internal/store/lock.go`
- Test: `internal/store/lock_test.go`

**Interfaces:**
- Consumes: `newManager`(Task 3),`(*Manager).bizPool`。
- Produces: `ErrDeviceLocked error`;`(*Manager).AcquireDeviceLock(ctx, accountJID string) (*DeviceLock, error)`;`(*DeviceLock).Release(ctx)`;`(*DeviceLock).Healthy(ctx) bool`;`advisoryKey(string) int64`。

- [ ] **Step 1: 写失败测试**

```go
// internal/store/lock_test.go
package store

import (
	"context"
	"errors"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestAcquireDeviceLock_MutualExclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, err := newManager(ctx, Config{DSN: testDSN(t)}, waLog.Noop)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	defer m.Close()

	const jid = "1234567890.0:0@s.whatsapp.net"

	l1, err := m.AcquireDeviceLock(ctx, jid)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !l1.Healthy(ctx) {
		t.Fatal("lock should be healthy while held")
	}

	// 第二次抢同一 jid:不同连接 → 抢不到
	if _, err := m.AcquireDeviceLock(ctx, jid); !errors.Is(err, ErrDeviceLocked) {
		t.Fatalf("second acquire err = %v, want ErrDeviceLocked", err)
	}

	// 释放后可再抢
	l1.Release(ctx)
	l2, err := m.AcquireDeviceLock(ctx, jid)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	l2.Release(ctx)
}
```

- [ ] **Step 2: 运行验证失败**

Run: `go test ./internal/store/ -run TestAcquireDeviceLock -v`
Expected: FAIL,`m.AcquireDeviceLock undefined`。

- [ ] **Step 3: 实现 lock.go**

```go
// internal/store/lock.go
package store

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrDeviceLocked = errors.New("store: device owned by another process")

// DeviceLock 持有一条专用连接以维持 session 级 advisory lock。
type DeviceLock struct {
	conn *pgxpool.Conn
	key  int64
}

func advisoryKey(accountJID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(accountJID))
	return int64(h.Sum64())
}

// AcquireDeviceLock 非阻塞抢占账号独占权;失败返回 ErrDeviceLocked。
func (m *Manager) AcquireDeviceLock(ctx context.Context, accountJID string) (*DeviceLock, error) {
	key := advisoryKey(accountJID)

	conn, err := m.bizPool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for lock: %w", err)
	}

	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		conn.Release()
		return nil, fmt.Errorf("try advisory lock: %w", err)
	}
	if !ok {
		conn.Release()
		return nil, ErrDeviceLocked
	}
	return &DeviceLock{conn: conn, key: key}, nil
}

// Healthy 探测持锁连接是否仍存活(即锁是否仍归我持有)。
func (l *DeviceLock) Healthy(ctx context.Context) bool {
	if l == nil || l.conn == nil {
		return false
	}
	return l.conn.Ping(ctx) == nil
}

// Release 释放锁并归还连接。进程崩溃时连接断开,Postgres 自动回收锁。
func (l *DeviceLock) Release(ctx context.Context) {
	if l == nil || l.conn == nil {
		return
	}
	_, _ = l.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", l.key)
	l.conn.Release()
	l.conn = nil
}
```

- [ ] **Step 4: 运行验证通过**

Run: `go test ./internal/store/ -run TestAcquireDeviceLock -race -v`
Expected: PASS(互斥成立,释放后可再抢,无竞态)。

- [ ] **Step 5: 提交**

```bash
git add internal/store/lock.go internal/store/lock_test.go
git commit -m "feat(store): cross-process advisory device lock with fencing probe"
```

---

## Self-Review

- **Spec 覆盖**:Config+池 ✓(T1/T3)、whatsmeow 持久化 ✓(T3)、GetDeviceStore 工厂 ✓(T4)、多进程安全 advisory lock ✓(T5)、迁移可重入 ✓(T2)。M1 范围内无遗漏。
- **占位符扫描**:无 TBD/TODO,每步含完整代码与命令。
- **类型一致性**:`newManager`/`Init` 签名在 T3 定义,T4/T5 测试一致复用;`Config` 字段名跨任务一致;`DeviceLock` 的 `conn/key` 字段在 T5 内自洽。
- **范围边界(显式声明)**:M1 不覆盖"已配对 Device 的正向 GetDevice 读取"(需真实 WA 配对,属人工/E2E,排除在自动化外);`PutDevice`、设备封号字段写入留待 M9/M6 按需补。
