# Module B — Platform Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up `cmd/console`, an authenticated server-rendered web tier with login, Redis-backed sessions, RBAC, and tenant-context (RLS) middleware — the foundation every role UI (Modules C/D/E) builds on.

**Architecture:** A new binary `cmd/console` in the same Go module, reusing `internal/store` (the double-pool RLS Manager) and `internal/config`. Web logic lives in a focused `internal/console` package (config, password, users, session, middleware, server, templates). Server-rendered Go `html/template` + htmx; no SPA, no JS build. Two new platform tables (`tenants`, `console_users`) accessed via the BYPASSRLS `SystemPool`; customer requests run through `WithTenant` for DB-level RLS isolation.

**Tech Stack:** Go 1.26, `net/http`, `html/template`, `embed`, `golang.org/x/crypto/argon2`, `github.com/redis/go-redis/v9`, `github.com/jackc/pgx/v5/pgxpool`. Tests use `net/http/httptest` and testcontainers (PG16 + Redis7), `-race`.

## Global Constraints

- Go 1.26.4; module `github.com/acme/wadist`.
- Reuse existing infra: `internal/store` Manager (`SystemPool()` BYPASSRLS, `WithTenant(ctx, tenantID)` RLS tx), `internal/config` `config.Load()`, `internal/log` `log.Production()`.
- Integration tests use testcontainers with `TESTCONTAINERS_RYUK_DISABLED=true` honored via the Makefile; never weaken `make gate` (vet + `-race` + label gate + govulncheck).
- Migrations are idempotent (`CREATE TABLE IF NOT EXISTS`, `DROP ... IF EXISTS` before `CREATE`), lexicographically ordered `migrations/NNNN_*.sql`, and reuse the existing `touch_updated_at()` trigger function defined in `0001_account_devices.sql`.
- No high-cardinality Prometheus labels (label gate). Module B adds no metrics.
- All writes that mutate platform/business state must be auditable later; B itself only writes `console_users`/`tenants` via admin tooling/tests — full audit wiring is Module C.
- Role values are exactly the strings `admin`, `sales`, `customer`.

---

### Task 1: Password hashing (argon2id)

**Files:**
- Create: `internal/console/password.go`
- Test: `internal/console/password_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `console.HashPassword(plain string) (string, error)` and `console.VerifyPassword(encoded, plain string) (bool, error)`. `encoded` is a self-describing PHC-style string (`$argon2id$v=19$m=65536,t=1,p=4$<b64salt>$<b64hash>`).

- [ ] **Step 1: Write the failing test**

```go
// internal/console/password_test.go
package console

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "" || hash == "correct horse battery staple" {
		t.Fatalf("hash must be non-empty and not plaintext, got %q", hash)
	}
	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify correct: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(hash, "wrong password")
	if err != nil {
		t.Fatalf("verify wrong returned err: %v", err)
	}
	if ok {
		t.Fatalf("verify wrong must be false")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	if _, err := VerifyPassword("not-a-valid-hash", "x"); err == nil {
		t.Fatalf("expected error for malformed hash")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `/usr/local/go/bin/go test ./internal/console/ -run TestHashAndVerifyPassword -v`
Expected: FAIL — `undefined: HashPassword`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/console/password.go
// Package console implements the wadist management web platform: login,
// sessions, RBAC, tenant-context middleware, and server-rendered pages.
package console

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemoryKiB = 64 * 1024 // 64 MiB
	argonTime      = 1
	argonThreads   = 4
	argonKeyLen    = 32
	argonSaltLen   = 16
)

// HashPassword returns a PHC-style argon2id encoding of plain.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("argon2: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether plain matches the PHC-encoded argon2id hash.
func VerifyPassword(encoded, plain string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=65536,t=1,p=4", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("argon2: malformed hash")
	}
	var version, mem, time, threads int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("argon2: bad version: %w", err)
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &time, &threads); err != nil {
		return false, fmt.Errorf("argon2: bad params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("argon2: bad salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("argon2: bad hash: %w", err)
	}
	got := argon2.IDKey([]byte(plain), salt, uint32(time), uint32(mem), uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/console/ -run TestHashAndVerify -v && /usr/local/go/bin/go test ./internal/console/ -run TestVerifyPasswordRejectsMalformedHash -v`
Expected: PASS for both.

- [ ] **Step 5: Tidy and commit**

```bash
/usr/local/go/bin/go mod tidy
git add go.mod go.sum internal/console/password.go internal/console/password_test.go
git commit -m "feat(console): argon2id password hashing"
```

---

### Task 2: Platform tables + UserRepo

**Files:**
- Create: `migrations/0009_tenants.sql`
- Create: `migrations/0010_console_users.sql`
- Create: `internal/console/users.go`
- Create: `internal/console/users_test.go`
- Create: `internal/console/testsupport_test.go` (testcontainer + migration helper, shared by later tasks)

**Interfaces:**
- Consumes: `HashPassword`/`VerifyPassword` from Task 1.
- Produces:
  - `type Role string`; consts `RoleAdmin Role = "admin"`, `RoleSales Role = "sales"`, `RoleCustomer Role = "customer"`.
  - `type User struct { ID int64; Email string; Role Role; TenantID *int64; Disabled bool }`.
  - `var ErrUserNotFound = errors.New(...)`, `var ErrInvalidCredentials = errors.New(...)`.
  - `type UserRepo struct{ ... }`; `func NewUserRepo(pool *pgxpool.Pool) *UserRepo`.
  - `func (r *UserRepo) Create(ctx context.Context, email, passwordHash string, role Role, tenantID *int64) (int64, error)`.
  - `func (r *UserRepo) Authenticate(ctx context.Context, email, plain string) (*User, error)`.
  - Test helper (in `testsupport_test.go`): `func newTestManager(t *testing.T) *store.Manager` (starts PG16 testcontainer, applies all migrations, returns a non-singleton Manager) and `func ptrInt64(v int64) *int64`.

- [ ] **Step 1: Write the migrations**

```sql
-- migrations/0009_tenants.sql — tenant registry for the web platform.
-- Tenants are NOT RLS-scoped to themselves; they are platform metadata
-- accessed by staff via the BYPASSRLS app_system pool.
CREATE TABLE IF NOT EXISTS tenants (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT        NOT NULL,
    status         TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended')),
    sales_owner_id BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

DROP TRIGGER IF EXISTS trg_tenants_touch ON tenants;
CREATE TRIGGER trg_tenants_touch BEFORE UPDATE ON tenants
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
```

```sql
-- migrations/0010_console_users.sql — console login accounts (admin/sales/customer).
CREATE TABLE IF NOT EXISTS console_users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL CHECK (role IN ('admin','sales','customer')),
    tenant_id     BIGINT      REFERENCES tenants(id),
    disabled      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- customers MUST map to a tenant; staff (admin/sales) MUST NOT.
    CONSTRAINT console_users_tenant_role CHECK (
        (role = 'customer' AND tenant_id IS NOT NULL) OR
        (role IN ('admin','sales') AND tenant_id IS NULL)
    )
);

DROP TRIGGER IF EXISTS trg_console_users_touch ON console_users;
CREATE TRIGGER trg_console_users_touch BEFORE UPDATE ON console_users
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- sales ownership FK (added after both tables exist).
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_sales_owner_fk;
ALTER TABLE tenants ADD CONSTRAINT tenants_sales_owner_fk
    FOREIGN KEY (sales_owner_id) REFERENCES console_users(id);

-- Defense in depth: the RLS tenant role must never read login/registry tables.
-- 0008's ALTER DEFAULT PRIVILEGES auto-grants new tables to app_tenant; revoke it.
-- (Roles app_tenant/app_system are created idempotently in 0008, which runs first.)
REVOKE ALL ON tenants, console_users FROM app_tenant;
```

- [ ] **Step 2: Write the testcontainer + migration helper**

```go
// internal/console/testsupport_test.go
package console

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	walog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/store"
)

// newTestManager starts a throwaway PG16, applies every migration, and returns
// a non-singleton store.Manager wired to it. The container is torn down on cleanup.
func newTestManager(t *testing.T) *store.Manager {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("console_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}

	logger, flush, err := walog.Production()
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(flush)

	mgr, err := store.NewManager(ctx, store.Config{DSN: dsn, NodeID: "console-test"}, logger)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(mgr.Close)

	applyAllMigrations(t, ctx, mgr)
	return mgr
}

func applyAllMigrations(t *testing.T, ctx context.Context, mgr *store.Manager) {
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
		if _, err := mgr.SystemPool().Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
}

func ptrInt64(v int64) *int64 { return &v }
```

- [ ] **Step 3: Write the failing UserRepo test**

```go
// internal/console/users_test.go
package console

import (
	"context"
	"errors"
	"testing"
)

func TestUserRepoAuthenticate(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	repo := NewUserRepo(mgr.SystemPool())

	hash, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	// admin staff user (no tenant).
	if _, err := repo.Create(ctx, "admin@acme.test", hash, RoleAdmin, nil); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	u, err := repo.Authenticate(ctx, "admin@acme.test", "s3cret-pw")
	if err != nil {
		t.Fatalf("authenticate ok: %v", err)
	}
	if u.Role != RoleAdmin || u.TenantID != nil {
		t.Fatalf("unexpected user: %+v", u)
	}

	if _, err := repo.Authenticate(ctx, "admin@acme.test", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: want ErrInvalidCredentials, got %v", err)
	}
	if _, err := repo.Authenticate(ctx, "nobody@acme.test", "x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: want ErrInvalidCredentials, got %v", err)
	}
}

func TestUserRepoCustomerRequiresTenant(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	repo := NewUserRepo(mgr.SystemPool())
	hash, _ := HashPassword("pw")

	// customer WITHOUT tenant violates the CHECK constraint.
	if _, err := repo.Create(ctx, "c1@acme.test", hash, RoleCustomer, nil); err == nil {
		t.Fatalf("expected constraint error for customer without tenant")
	}

	var tenantID int64
	if err := mgr.SystemPool().QueryRow(ctx,
		`INSERT INTO tenants (name) VALUES ('Acme') RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	id, err := repo.Create(ctx, "c1@acme.test", hash, RoleCustomer, ptrInt64(tenantID))
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	u, err := repo.Authenticate(ctx, "c1@acme.test", "pw")
	if err != nil {
		t.Fatalf("auth customer: %v", err)
	}
	if u.ID != id || u.TenantID == nil || *u.TenantID != tenantID {
		t.Fatalf("unexpected customer: %+v", u)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestUserRepo -v`
Expected: FAIL — `undefined: NewUserRepo` / `undefined: RoleAdmin`.

- [ ] **Step 5: Write the UserRepo implementation**

```go
// internal/console/users.go
package console

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Role is a console access role.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleSales    Role = "sales"
	RoleCustomer Role = "customer"
)

// User is a console login account (password hash deliberately excluded).
type User struct {
	ID       int64
	Email    string
	Role     Role
	TenantID *int64 // non-nil iff Role == RoleCustomer
	Disabled bool
}

var (
	ErrUserNotFound       = errors.New("console: user not found")
	ErrInvalidCredentials = errors.New("console: invalid credentials")
)

// UserRepo reads/writes console_users. It MUST be given the BYPASSRLS system
// pool: login/registry tables are platform metadata, not tenant-scoped.
type UserRepo struct {
	pool *pgxpool.Pool
}

func NewUserRepo(pool *pgxpool.Pool) *UserRepo { return &UserRepo{pool: pool} }

// Create inserts a console_users row and returns its id.
func (r *UserRepo) Create(ctx context.Context, email, passwordHash string, role Role, tenantID *int64) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx,
		`INSERT INTO console_users (email, password_hash, role, tenant_id)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		email, passwordHash, string(role), tenantID,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create console user: %w", err)
	}
	return id, nil
}

// Authenticate verifies credentials and returns the user. A missing user,
// wrong password, or disabled account all return ErrInvalidCredentials (no
// account enumeration via differing errors).
func (r *UserRepo) Authenticate(ctx context.Context, email, plain string) (*User, error) {
	var (
		u    User
		hash string
		role string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, role, tenant_id, disabled
		   FROM console_users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &hash, &role, &u.TenantID, &u.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("query console user: %w", err)
	}
	if u.Disabled {
		return nil, ErrInvalidCredentials
	}
	ok, err := VerifyPassword(hash, plain)
	if err != nil {
		return nil, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	u.Role = Role(role)
	return &u, nil
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestUserRepo -v`
Expected: PASS (both `TestUserRepoAuthenticate` and `TestUserRepoCustomerRequiresTenant`).

- [ ] **Step 7: Verify migration idempotency, then commit**

Run: `/usr/local/go/bin/go vet ./internal/console/`
Expected: no output.

```bash
git add migrations/0009_tenants.sql migrations/0010_console_users.sql \
        internal/console/users.go internal/console/users_test.go internal/console/testsupport_test.go
git commit -m "feat(console): tenants + console_users tables and UserRepo"
```

---

### Task 3: Redis-backed session store + signed cookie

**Files:**
- Create: `internal/console/session.go`
- Create: `internal/console/session_test.go`

**Interfaces:**
- Consumes: `Role` from Task 2.
- Produces:
  - `type SessionData struct { UserID int64; Role Role; TenantID *int64 }`.
  - `type SessionStore struct{ ... }`; `func NewSessionStore(rdb *redis.Client, ttl time.Duration) *SessionStore`.
  - `func (s *SessionStore) Create(ctx context.Context, d SessionData) (sid string, err error)`.
  - `func (s *SessionStore) Get(ctx context.Context, sid string) (*SessionData, error)` — returns `ErrNoSession` when absent/expired.
  - `func (s *SessionStore) Destroy(ctx context.Context, sid string) error`.
  - `var ErrNoSession = errors.New(...)`.
  - `func SignCookie(secret []byte, sid string) string` and `func VerifyCookie(secret []byte, value string) (sid string, ok bool)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/console/session_test.go
package console

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	ctx := context.Background()
	c, err := tcredis.Run(ctx, "redis:7")
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	uri, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis uri: %v", err)
	}
	opt, err := redis.ParseURL(uri)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	rdb := redis.NewClient(opt)
	t.Cleanup(func() { _ = rdb.Close() })
	_ = testcontainers.TerminateContainer // keep import used if needed
	return rdb
}

func TestSessionStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewSessionStore(newTestRedis(t), time.Hour)

	sid, err := store.Create(ctx, SessionData{UserID: 42, Role: RoleCustomer, TenantID: ptrInt64(7)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.Get(ctx, sid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserID != 42 || got.Role != RoleCustomer || got.TenantID == nil || *got.TenantID != 7 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if err := store.Destroy(ctx, sid); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := store.Get(ctx, sid); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after destroy want ErrNoSession, got %v", err)
	}
}

func TestSignVerifyCookie(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	signed := SignCookie(secret, "session-id-123")
	sid, ok := VerifyCookie(secret, signed)
	if !ok || sid != "session-id-123" {
		t.Fatalf("verify failed: sid=%q ok=%v", sid, ok)
	}
	if _, ok := VerifyCookie(secret, signed+"tamper"); ok {
		t.Fatalf("tampered cookie must fail")
	}
	if _, ok := VerifyCookie([]byte("wrong-secret-wrong-secret-32byte"), signed); ok {
		t.Fatalf("wrong secret must fail")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run 'TestSessionStoreRoundTrip|TestSignVerifyCookie' -v`
Expected: FAIL — `undefined: NewSessionStore`.

- [ ] **Step 3: Write the implementation**

```go
// internal/console/session.go
package console

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// SessionData is the server-side session payload stored in Redis.
type SessionData struct {
	UserID   int64  `json:"uid"`
	Role     Role   `json:"role"`
	TenantID *int64 `json:"tid,omitempty"`
}

var ErrNoSession = errors.New("console: no session")

const sessionKeyPrefix = "console:session:"

// SessionStore persists sessions in Redis with a TTL.
type SessionStore struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewSessionStore(rdb *redis.Client, ttl time.Duration) *SessionStore {
	return &SessionStore{rdb: rdb, ttl: ttl}
}

func newSID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *SessionStore) Create(ctx context.Context, d SessionData) (string, error) {
	sid, err := newSID()
	if err != nil {
		return "", fmt.Errorf("session id: %w", err)
	}
	payload, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("marshal session: %w", err)
	}
	if err := s.rdb.Set(ctx, sessionKeyPrefix+sid, payload, s.ttl).Err(); err != nil {
		return "", fmt.Errorf("redis set: %w", err)
	}
	return sid, nil
}

func (s *SessionStore) Get(ctx context.Context, sid string) (*SessionData, error) {
	raw, err := s.rdb.Get(ctx, sessionKeyPrefix+sid).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("redis get: %w", err)
	}
	var d SessionData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return &d, nil
}

func (s *SessionStore) Destroy(ctx context.Context, sid string) error {
	if err := s.rdb.Del(ctx, sessionKeyPrefix+sid).Err(); err != nil {
		return fmt.Errorf("redis del: %w", err)
	}
	return nil
}

// SignCookie returns "<sid>.<base64 hmac>" so a tampered sid is detectable.
func SignCookie(secret []byte, sid string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sid))
	return sid + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyCookie validates the HMAC and returns the embedded sid.
func VerifyCookie(secret []byte, value string) (string, bool) {
	i := strings.LastIndexByte(value, '.')
	if i <= 0 {
		return "", false
	}
	sid, sig := value[:i], value[i+1:]
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sid))
	if subtle.ConstantTimeCompare(want, mac.Sum(nil)) != 1 {
		return "", false
	}
	return sid, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run 'TestSessionStoreRoundTrip|TestSignVerifyCookie' -v`
Expected: PASS. (If `go vet` flags the unused `testcontainers` import in the test, delete the `_ = testcontainers.TerminateContainer` line and the import.)

- [ ] **Step 5: Commit**

```bash
/usr/local/go/bin/go mod tidy
git add go.mod go.sum internal/console/session.go internal/console/session_test.go
git commit -m "feat(console): redis-backed sessions + signed cookies"
```

---

### Task 4: Console config + server skeleton (templates, health, login page)

**Files:**
- Create: `internal/console/config.go`
- Create: `internal/console/templates.go`
- Create: `internal/console/templates/layout.html`
- Create: `internal/console/templates/login.html`
- Create: `internal/console/templates/dashboard.html`
- Create: `internal/console/server.go`
- Create: `internal/console/server_test.go`

**Interfaces:**
- Consumes: `UserRepo` (Task 2), `SessionStore`/`SignCookie`/`VerifyCookie` (Task 3).
- Produces:
  - `type Config struct { Addr string; SessionKey []byte; SessionTTL time.Duration; CookieName string; CookieSecure bool }`; `func LoadConfig() (Config, error)`.
  - `type Server struct{ ... }`; `func NewServer(cfg Config, users *UserRepo, sessions *SessionStore, mgr *store.Manager) (*Server, error)`.
  - `func (s *Server) Handler() http.Handler`, `func (s *Server) Start() error`, `func (s *Server) Addr() string`, `func (s *Server) Shutdown(ctx context.Context) error`, `func (s *Server) SetReady(bool)`.
  - Routes so far: `GET /healthz`, `GET /readyz`, `GET /login` (renders login page). Auth POST/dashboard added in Task 5.

- [ ] **Step 1: Write the templates**

```html
<!-- internal/console/templates/layout.html -->
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>wadist console</title>
  <script src="/static/htmx.min.js" defer></script>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body>
  <main>{{template "content" .}}</main>
</body>
</html>{{end}}
```

```html
<!-- internal/console/templates/login.html -->
{{define "content"}}
<h1>wadist 控制台登录</h1>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<form method="post" action="/login">
  <label>邮箱 <input type="email" name="email" required autofocus></label>
  <label>密码 <input type="password" name="password" required></label>
  <button type="submit">登录</button>
</form>
{{end}}
```

```html
<!-- internal/console/templates/dashboard.html -->
{{define "content"}}
<h1>控制台</h1>
<p>已登录:角色 <strong>{{.Role}}</strong></p>
<form method="post" action="/logout"><button type="submit">登出</button></form>
{{end}}
```

- [ ] **Step 2: Write the template loader**

```go
// internal/console/templates.go
package console

import (
	"embed"
	"fmt"
	"html/template"
	"io"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// renderSet parses layout.html with one page template so {{template "content"}}
// resolves to that page. Returned templates are rendered via the "layout" name.
func parsePage(page string) (*template.Template, error) {
	return template.ParseFS(templateFS, "templates/layout.html", "templates/"+page)
}

func render(w io.Writer, t *template.Template, data any) error {
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		return fmt.Errorf("render template: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Add a placeholder static asset (so the embed compiles) and vendor htmx**

```bash
mkdir -p internal/console/static
printf '/* wadist console minimal styles */\nbody{font-family:system-ui,sans-serif;margin:2rem;max-width:60rem}\n.error{color:#b00020}\nlabel{display:block;margin:.5rem 0}\n' > internal/console/static/app.css
curl -fsSL https://unpkg.com/htmx.org@2.0.3/dist/htmx.min.js -o internal/console/static/htmx.min.js
# Offline fallback if curl is unavailable: a stub keeps the embed + tests valid;
# replace with the real library before shipping interactivity.
[ -s internal/console/static/htmx.min.js ] || printf '/* htmx placeholder: vendor real htmx before release */\n' > internal/console/static/htmx.min.js
```

- [ ] **Step 4: Write the failing server test**

```go
// internal/console/server_test.go
package console

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Addr:         "127.0.0.1:0",
		SessionKey:   []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL:   time.Hour,
		CookieName:   "wadist_session",
		CookieSecure: false,
	}
}

func TestServerLoginPageAndHealth(t *testing.T) {
	mgr := newTestManager(t)
	srv, err := NewServer(testConfig(), NewUserRepo(mgr.SystemPool()), NewSessionStore(newTestRedis(t), time.Hour), mgr)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// /healthz
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// /login renders the form
	resp, err = http.Get(ts.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `action="/login"`) {
		t.Fatalf("login page not rendered: %d\n%s", resp.StatusCode, b)
	}

	// static asset served
	resp, err = http.Get(ts.URL + "/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("static css: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
```

- [ ] **Step 5: Run test to verify it fails**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestServerLoginPageAndHealth -v`
Expected: FAIL — `undefined: NewServer` / `undefined: Config`.

- [ ] **Step 6: Write config + server skeleton**

```go
// internal/console/config.go
package console

import (
	"encoding/base64"
	"fmt"
	"os"
	"time"
)

// Config is the console web-tier configuration (store/redis config comes from
// internal/config separately and is shared with the node).
type Config struct {
	Addr         string
	SessionKey   []byte
	SessionTTL   time.Duration
	CookieName   string
	CookieSecure bool
}

// LoadConfig reads WADIST_CONSOLE_* env vars. SessionKey is required (base64-std,
// >=32 bytes) so sessions survive restarts and cannot be forged.
func LoadConfig() (Config, error) {
	cfg := Config{
		Addr:         getenv("WADIST_CONSOLE_ADDR", ":8080"),
		SessionTTL:   24 * time.Hour,
		CookieName:   getenv("WADIST_CONSOLE_COOKIE_NAME", "wadist_session"),
		CookieSecure: getenv("WADIST_CONSOLE_COOKIE_SECURE", "true") != "false",
	}
	raw := os.Getenv("WADIST_CONSOLE_SESSION_KEY")
	if raw == "" {
		return Config{}, fmt.Errorf("console: WADIST_CONSOLE_SESSION_KEY is required")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return Config{}, fmt.Errorf("console: WADIST_CONSOLE_SESSION_KEY base64: %w", err)
	}
	if len(key) < 32 {
		return Config{}, fmt.Errorf("console: WADIST_CONSOLE_SESSION_KEY must be >=32 bytes, got %d", len(key))
	}
	cfg.SessionKey = key
	if v := os.Getenv("WADIST_CONSOLE_SESSION_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.SessionTTL = d
		}
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
```

```go
// internal/console/server.go
package console

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/acme/wadist/internal/store"
)

// Server is the console HTTP server. It mirrors metrics.Server's lifecycle
// (synchronous bind, background serve, SetReady drain) for consistency.
type Server struct {
	cfg      Config
	users    *UserRepo
	sessions *SessionStore
	mgr      *store.Manager
	loginTpl *templateHandle
	dashTpl  *templateHandle
	srv      *http.Server
	ln       net.Listener
	ready    atomic.Bool
}

// templateHandle wraps a parsed template so handlers stay terse.
type templateHandle struct{ t interface{ render(any) error } }

func NewServer(cfg Config, users *UserRepo, sessions *SessionStore, mgr *store.Manager) (*Server, error) {
	return &Server{cfg: cfg, users: users, sessions: sessions, mgr: mgr}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.ready.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("draining"))
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(mustStaticSub()))))
	mux.HandleFunc("GET /login", s.handleLoginPage)
	return mux
}

func (s *Server) handleLoginPage(w http.ResponseWriter, _ *http.Request) {
	t, err := parsePage("login.html")
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"Error": ""})
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	s.ready.Store(true)
	return nil
}

func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.cfg.Addr
}

func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
```

Add the static sub-FS helper to `templates.go`:

```go
// append to internal/console/templates.go
import "io/fs"

func mustStaticSub() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded FS is compile-time constant; this never fails
	}
	return sub
}
```

Remove the unused `templateHandle` indirection (it was a sketch): delete the `loginTpl`/`dashTpl`/`templateHandle` fields and type from `server.go` — handlers call `parsePage` directly. (Keeping the struct minimal avoids dead code that `go vet`/staticcheck would flag.)

- [ ] **Step 7: Run test to verify it passes**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestServerLoginPageAndHealth -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
/usr/local/go/bin/go vet ./internal/console/
git add internal/console/config.go internal/console/templates.go internal/console/server.go \
        internal/console/server_test.go internal/console/templates internal/console/static
git commit -m "feat(console): config + server skeleton (login page, health, static)"
```

---

### Task 5: Auth flow + RBAC middleware

**Files:**
- Create: `internal/console/middleware.go`
- Modify: `internal/console/server.go` (add POST /login, POST /logout, GET / dashboard; wire middleware)
- Create: `internal/console/auth_test.go`

**Interfaces:**
- Consumes: `Server`, `UserRepo`, `SessionStore`, `SignCookie`/`VerifyCookie`, `SessionData`, `Role`.
- Produces:
  - `func sessionFrom(ctx context.Context) (*SessionData, bool)`.
  - `func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc` — loads session from cookie into context or 302 → /login.
  - `func (s *Server) requireRole(roles ...Role) func(http.HandlerFunc) http.HandlerFunc` — 403 if session role not allowed.
  - New routes: `POST /login`, `POST /logout`, `GET /` (dashboard, behind requireAuth).

- [ ] **Step 1: Write the failing test**

```go
// internal/console/auth_test.go
package console

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// loginClient logs in and returns a cookie-jar client + the server.
func newAuthedServer(t *testing.T) (*httptest.Server, *store0) {
	t.Helper()
	return nil, nil // replaced below; placeholder to keep structure — see real impl
}

func TestLoginLogoutFlow(t *testing.T) {
	mgr := newTestManager(t)
	repo := NewUserRepo(mgr.SystemPool())
	hash, _ := HashPassword("pw12345")
	if _, err := repo.Create(t.Context(), "admin@acme.test", hash, RoleAdmin, nil); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	srv, _ := NewServer(testConfig(), repo, NewSessionStore(newTestRedis(t), time.Hour), mgr)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	jar, _ := newJar()
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	// Unauthenticated dashboard → 302 to /login.
	resp, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/login" {
		t.Fatalf("unauth dashboard: status=%d loc=%s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp.Body.Close()

	// Bad credentials → 200 with error, no cookie.
	resp, err = client.PostForm(ts.URL+"/login", url.Values{"email": {"admin@acme.test"}, "password": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bad login status: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Good credentials → 302 to / and a session cookie set.
	resp, err = client.PostForm(ts.URL+"/login", url.Values{"email": {"admin@acme.test"}, "password": {"pw12345"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("good login: status=%d loc=%s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp.Body.Close()

	// Authenticated dashboard → 200 showing role.
	resp, err = client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "admin") {
		t.Fatalf("auth dashboard: status=%d body=%s", resp.StatusCode, body)
	}

	// Logout → 302 to /login; dashboard again unauthenticated.
	resp, err = client.PostForm(ts.URL+"/logout", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("after logout dashboard should redirect, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
```

Add small test helpers at the bottom of `auth_test.go` (and delete the `newAuthedServer`/`store0` placeholder stub above — it exists only to show intent; the real test uses these helpers):

```go
import (
	"io"
	"net/http/cookiejar"
)

type store0 = struct{} // unused alias removed with the placeholder

func newJar() (*cookiejar.Jar, error) { return cookiejar.New(nil) }

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
```

> Note for implementer: delete the placeholder `newAuthedServer` func and the `store0` alias entirely — they were scaffolding to show the test's shape. The real test (`TestLoginLogoutFlow`) and the `newJar`/`readAll` helpers are what compile. Keep imports tidy (`io`, `net/http/cookiejar`, `net/url`, `strings`, `time`).

- [ ] **Step 2: Run test to verify it fails**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestLoginLogoutFlow -v`
Expected: FAIL (compile error or 404 on POST /login — routes not defined yet).

- [ ] **Step 3: Write the middleware**

```go
// internal/console/middleware.go
package console

import (
	"context"
	"net/http"
)

type ctxKey int

const sessionCtxKey ctxKey = 0

func sessionFrom(ctx context.Context) (*SessionData, bool) {
	d, ok := ctx.Value(sessionCtxKey).(*SessionData)
	return d, ok
}

// requireAuth loads the session from the signed cookie into the request context,
// or redirects to /login.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.cfg.CookieName)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		sid, ok := VerifyCookie(s.cfg.SessionKey, c.Value)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		data, err := s.sessions.Get(r.Context(), sid)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), sessionCtxKey, data)
		next(w, r.WithContext(ctx))
	}
}

// requireRole returns middleware that 403s unless the session role is allowed.
// It must wrap a handler already behind requireAuth.
func (s *Server) requireRole(roles ...Role) func(http.HandlerFunc) http.HandlerFunc {
	allowed := make(map[Role]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			data, ok := sessionFrom(r.Context())
			if !ok || !allowed[data.Role] {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next(w, r)
		}
	}
}
```

- [ ] **Step 4: Wire the auth routes into `Handler()`**

In `internal/console/server.go`, add these registrations inside `Handler()` (after the `GET /login` line):

```go
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("GET /{$}", s.requireAuth(s.handleDashboard))
```

And add the handlers to `server.go`:

```go
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	email := r.FormValue("email")
	password := r.FormValue("password")
	user, err := s.users.Authenticate(r.Context(), email, password)
	if err != nil {
		t, perr := parsePage("login.html")
		if perr != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = render(w, t, map[string]any{"Error": "邮箱或密码错误"})
		return
	}
	sid, err := s.sessions.Create(r.Context(), SessionData{UserID: user.ID, Role: user.Role, TenantID: user.TenantID})
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    SignCookie(s.cfg.SessionKey, sid),
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cfg.CookieName); err == nil {
		if sid, ok := VerifyCookie(s.cfg.SessionKey, c.Value); ok {
			_ = s.sessions.Destroy(r.Context(), sid)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: s.cfg.CookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data, _ := sessionFrom(r.Context())
	t, err := parsePage("dashboard.html")
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"Role": string(data.Role)})
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestLoginLogoutFlow -v`
Expected: PASS.

- [ ] **Step 6: Add an RBAC unit test**

```go
// append to internal/console/auth_test.go
func TestRequireRoleForbidsWrongRole(t *testing.T) {
	srv, _ := NewServer(testConfig(), nil, nil, nil)
	h := srv.requireRole(RoleAdmin)(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// inject a customer session directly
	req := httptest.NewRequest("GET", "/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), sessionCtxKey, &SessionData{UserID: 1, Role: RoleCustomer, TenantID: ptrInt64(9)}))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("customer hitting admin-only route: want 403, got %d", rec.Code)
	}
}
```

Add `"context"` to the `auth_test.go` imports.

- [ ] **Step 7: Run all console tests, then commit**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/console/ -v`
Expected: PASS (all tests).

```bash
/usr/local/go/bin/go vet ./internal/console/
git add internal/console/middleware.go internal/console/server.go internal/console/auth_test.go
git commit -m "feat(console): login/logout flow + RBAC middleware"
```

---

### Task 6: Tenant-context middleware + RLS isolation proof

**Files:**
- Modify: `internal/console/middleware.go` (add `withTenantTx` helper)
- Create: `internal/console/tenant_test.go`

**Interfaces:**
- Consumes: `store.Manager.WithTenant`, `sessionFrom`, `SessionData`.
- Produces: `func (s *Server) withTenantTx(ctx context.Context) (pgx.Tx, error)` — begins an RLS tx scoped to the session's tenant; errors if the session is not a customer (no tenant). This is the seam Module E uses for all customer DB reads/writes.

This task proves the foundation's central security guarantee: a customer session sees only its own tenant's rows. It uses the existing tenant-scoped `account_devices` table (already RLS-enforced) seeded for two tenants.

- [ ] **Step 1: Write the failing test**

```go
// internal/console/tenant_test.go
package console

import (
	"context"
	"testing"
)

func TestWithTenantTxIsolatesRows(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)

	// Two tenants, one account_devices row each (RLS-enforced table).
	var tA, tB int64
	if err := mgr.SystemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('A') RETURNING id`).Scan(&tA); err != nil {
		t.Fatalf("tenant A: %v", err)
	}
	if err := mgr.SystemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('B') RETURNING id`).Scan(&tB); err != nil {
		t.Fatalf("tenant B: %v", err)
	}
	seedDevice(t, ctx, mgr, tA, "jidA@s.whatsapp.net")
	seedDevice(t, ctx, mgr, tB, "jidB@s.whatsapp.net")

	srv, _ := NewServer(testConfig(), nil, nil, mgr)

	// Customer of tenant A should see exactly one device (its own).
	cctx := context.WithValue(ctx, sessionCtxKey, &SessionData{UserID: 1, Role: RoleCustomer, TenantID: &tA})
	tx, err := srv.withTenantTx(cctx)
	if err != nil {
		t.Fatalf("withTenantTx: %v", err)
	}
	defer tx.Rollback(ctx)
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_devices`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("RLS leak: customer A saw %d devices, want 1", count)
	}

	// A non-customer session must be rejected (no tenant scope).
	actx := context.WithValue(ctx, sessionCtxKey, &SessionData{UserID: 2, Role: RoleAdmin})
	if _, err := srv.withTenantTx(actx); err == nil {
		t.Fatalf("admin session must not get a tenant tx")
	}
}
```

Add the `seedDevice` helper at the bottom of `tenant_test.go`. It inserts a minimal `account_devices` row; column set is taken from `migrations/0001_account_devices.sql`:

```go
func seedDevice(t *testing.T, ctx context.Context, mgr interface {
	SystemPool() *pgxpoolPool
}, tenantID int64, jid string) {
	t.Helper()
	_, err := mgr.SystemPool().Exec(ctx,
		`INSERT INTO account_devices (jid, tenant_id, status) VALUES ($1, $2, 'active')`,
		jid, tenantID)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
}
```

> Implementer note: replace the inline interface with the real type. Import `"github.com/jackc/pgx/v5/pgxpool"` and use `mgr *store.Manager` directly (it has `SystemPool() *pgxpool.Pool`). The `pgxpoolPool` placeholder above is illustrative — write `func seedDevice(t *testing.T, ctx context.Context, mgr *store.Manager, tenantID int64, jid string)` and add `"github.com/acme/wadist/internal/store"` to imports. Before writing the INSERT, run `psql`/inspect `migrations/0001_account_devices.sql` to confirm the NOT NULL columns; if `status` is not a column or other columns are NOT NULL without defaults, adjust the column list accordingly (the test must insert a valid row).

- [ ] **Step 2: Run test to verify it fails**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestWithTenantTxIsolatesRows -v`
Expected: FAIL — `srv.withTenantTx undefined`.

- [ ] **Step 3: Implement `withTenantTx`**

```go
// append to internal/console/middleware.go
import (
	"errors"

	"github.com/jackc/pgx/v5"
)

// withTenantTx begins an RLS-scoped transaction for the current customer session.
// Callers (Module E handlers) use the returned tx for all tenant-scoped queries;
// it sees only rows where tenant_id matches the session tenant. Rollback/commit
// is the caller's responsibility.
func (s *Server) withTenantTx(ctx context.Context) (pgx.Tx, error) {
	data, ok := sessionFrom(ctx)
	if !ok || data.Role != RoleCustomer || data.TenantID == nil {
		return nil, errors.New("console: withTenantTx requires a customer session with a tenant")
	}
	return s.mgr.WithTenant(ctx, *data.TenantID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestWithTenantTxIsolatesRows -v`
Expected: PASS.

> If the test fails with a count of 2 (no isolation), the test Manager is using the superuser DSN for the tenant pool (RLS not enforced for the table owner). In that case the test must build an `app_tenant`-role DSN and pass it as `store.Config.AppTenantDSN` in `newTestManager` — mirror `buildAppTenantDSN` in `internal/store/rls_test.go`. Update `newTestManager` to set `AppTenantDSN` to the app_tenant DSN so `WithTenant` uses the RLS-constrained role. Re-run.

- [ ] **Step 5: Commit**

```bash
/usr/local/go/bin/go vet ./internal/console/
git add internal/console/middleware.go internal/console/tenant_test.go internal/console/testsupport_test.go
git commit -m "feat(console): tenant-context RLS tx middleware + isolation test"
```

---

### Task 7: `cmd/console` entrypoint + graceful shutdown + smoke test

**Files:**
- Create: `cmd/console/main.go`
- Create: `cmd/console/main_smoke_test.go`
- Modify: `Makefile` (add `console-run` + ensure `go build ./...` covers it — no change needed if build is `./...`)

**Interfaces:**
- Consumes: `config.Load()` (store + redis), `console.LoadConfig()`, `console.NewServer`, `store.Init`, `console.NewSessionStore`, `console.NewUserRepo`.
- Produces: a runnable binary; `run(ctx, ...) (*console.Server, func(), error)` seam mirroring `cmd/wadist`'s `run`.

- [ ] **Step 1: Write the failing smoke test**

```go
// cmd/console/main_smoke_test.go
package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestRun_BootsLogin requires a live PG + Redis (WADIST_POSTGRES_DSN set) and
// skips otherwise, matching cmd/wadist's smoke test convention.
func TestRun_BootsLogin(t *testing.T) {
	if testing.Short() || os.Getenv("WADIST_POSTGRES_DSN") == "" {
		t.Skip("integration: set WADIST_POSTGRES_DSN (+ WADIST_REDIS_ADDR)")
	}
	t.Setenv("WADIST_CONSOLE_ADDR", "127.0.0.1:0")
	t.Setenv("WADIST_CONSOLE_SESSION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=") // 32 bytes b64
	t.Setenv("WADIST_CONSOLE_COOKIE_SECURE", "false")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, stop, err := run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer stop()

	resp, err := http.Get("http://" + srv.Addr() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `action="/login"`) {
		t.Fatalf("login not served: %d\n%s", resp.StatusCode, b)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `/usr/local/go/bin/go test ./cmd/console/ -run TestRun_BootsLogin -v`
Expected: FAIL — `undefined: run` (or build error: package has no non-test files).

- [ ] **Step 3: Write the entrypoint**

```go
// cmd/console/main.go
// Command console is the wadist management web platform (admin/sales/customer).
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/acme/wadist/internal/config"
	"github.com/acme/wadist/internal/console"
	walog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/store"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	srv, stop, err := run(ctx)
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	log.Printf("console up on %s", srv.Addr())
	<-ctx.Done()
	log.Printf("shutdown signal received")
	stop()
}

// run assembles the console server and returns it plus a stop func. It is the
// seam the smoke test drives.
func run(ctx context.Context) (*console.Server, func(), error) {
	baseCfg, err := config.Load() // requires WADIST_POSTGRES_DSN; provides Store()+RedisAddr
	if err != nil {
		return nil, nil, err
	}
	webCfg, err := console.LoadConfig()
	if err != nil {
		return nil, nil, err
	}

	logger, flush, err := walog.Production()
	if err != nil {
		return nil, nil, err
	}
	mgr, err := store.Init(ctx, baseCfg.Store(), logger)
	if err != nil {
		flush()
		return nil, nil, err
	}

	rdb := goredis.NewClient(&goredis.Options{Addr: baseCfg.RedisAddr})
	sessions := console.NewSessionStore(rdb, webCfg.SessionTTL)
	users := console.NewUserRepo(mgr.SystemPool())

	srv, err := console.NewServer(webCfg, users, sessions, mgr)
	if err != nil {
		mgr.Close()
		flush()
		_ = rdb.Close()
		return nil, nil, err
	}
	if err := srv.Start(); err != nil {
		mgr.Close()
		flush()
		_ = rdb.Close()
		return nil, nil, err
	}

	stop := func() {
		srv.SetReady(false)
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(sctx)
		mgr.Close()
		flush()
		_ = rdb.Close()
	}
	return srv, stop, nil
}
```

- [ ] **Step 4: Run the smoke test against live services**

```bash
# reuse the E2E pattern: start throwaway PG+Redis, apply migrations, point env at them
export PATH=/usr/local/go/bin:$PATH
# (PG/Redis must be running; apply migrations via `make migrate-twice DSN=...` first)
WADIST_POSTGRES_DSN="postgres://app:app@127.0.0.1:5432/wadist?sslmode=disable" \
WADIST_REDIS_ADDR="127.0.0.1:6379" \
go test ./cmd/console/ -run TestRun_BootsLogin -v
```
Expected: PASS (or SKIP if no DSN — acceptable in unit-only environments; CI sets the DSN).

- [ ] **Step 5: Verify the whole module builds and the full suite is green**

Run: `/usr/local/go/bin/go build ./... && TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/console/ ./cmd/console/`
Expected: build OK; tests PASS.

- [ ] **Step 6: Add a Makefile convenience target and commit**

Add to `Makefile`:

```make
.PHONY: console-run
console-run: ## run the management console (needs WADIST_POSTGRES_DSN, WADIST_CONSOLE_SESSION_KEY)
	$(GO) run ./cmd/console
```

```bash
git add cmd/console/main.go cmd/console/main_smoke_test.go Makefile
git commit -m "feat(console): cmd/console entrypoint + graceful shutdown + smoke test"
```

---

## Self-Review

**1. Spec coverage** (against §3/§4-B of the roadmap spec):
- `cmd/console` separate binary reusing `internal/*` → Task 7. ✓
- Server-rendered `html/template` + htmx, no JS build → Tasks 4 (templates/embed), htmx vendored in Task 4 Step 3. ✓
- Redis-backed sessions → Task 3. ✓
- Login/logout → Task 5. ✓
- RBAC middleware → Task 5. ✓
- Tenant-context middleware (customer → `WithTenant`) → Task 6. ✓
- `tenants` + `console_users` tables → Task 2. ✓
- Role→pool mapping (admin/sales=SystemPool, customer=WithTenant RLS) → enforced by Task 2 (UserRepo on SystemPool) + Task 6 (withTenantTx). ✓
- argon2id password choice (was §10 open item, decided here) → Task 1. ✓
- Defense-in-depth REVOKE on platform tables from app_tenant → Task 2 migration. ✓

**2. Placeholder scan:** Two tests (Task 5 `newAuthedServer`/`store0`, Task 6 `seedDevice`'s `pgxpoolPool`) intentionally show scaffolding then give an explicit implementer note to replace it with the real type. These are flagged, not silent gaps. No `TODO`/`TBD` left in production code. The htmx vendoring has an explicit offline-fallback path so the embed always compiles.

**3. Type consistency:** `Role`/`SessionData`/`User` names are consistent across Tasks 2/3/5/6. `SessionStore` (not `Store`) used uniformly after Task 3. `withTenantTx` returns `pgx.Tx` consistent with `store.WithTenant`'s signature. `parsePage`/`render` used identically in Tasks 4 and 5. Server lifecycle methods (`Start`/`Addr`/`Shutdown`/`SetReady`) mirror `metrics.Server`.

**Known follow-ups for Module C and beyond (out of scope for B):** seeding the first admin user (a CLI/`make` target or migration), audit-log wiring on writes, and the per-tenant pricing/recharge use cases.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-25-module-b-platform-foundation.md`. Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session with checkpoints for review.
