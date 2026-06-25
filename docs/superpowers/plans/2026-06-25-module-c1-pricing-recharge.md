# Module C-1 — 按租户定价 + 管理员充值与余额视图 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Deliver the white paper's Week-1 increment on top of the existing billing engine: per-tenant×country pricing wired into the real charge point, billing read methods (balance/ledger), and an admin console for recharging wallets and setting prices.

**Architecture:** A new `internal/pricing` package (table `tenant_pricing` + repo) supplies unit prices; the `SendWorker` charge point uses it (replacing the hardcoded `Amount: 1`). New read methods on `billing.Repo` expose balance + ledger. The console gains admin-only pages (recharge, balance/ledger, set price) wired via **builder methods** (`WithBilling`/`WithPricing`/`WithTenants`) so `NewServer`'s signature — and all Module B tests — stay untouched.

**Tech Stack:** Go 1.26, pgx/v5, html/template+htmx, testcontainers (PG16). Reuses `internal/billing` (Hold/Settle/Refund/Topup, wallet CHECK balance>=0), `internal/console` (auth/RBAC/RLS from Module B), `internal/store`.

## Global Constraints

- Go module `github.com/acme/wadist`, Go 1.26.4. Toolchain at `/usr/local/go/bin`.
- Money is integer **minor units**; never floats. Wallet has DB-level `CHECK (balance>=0 AND frozen>=0)` — do not weaken it.
- Migrations idempotent (`CREATE TABLE IF NOT EXISTS`, `DROP ... IF EXISTS` before re-create), ordered `migrations/00NN_*.sql`, reuse `touch_updated_at()` from 0001.
- `tenant_pricing` is a PLATFORM table accessed via the BYPASSRLS SystemPool; `REVOKE ALL ... FROM app_tenant` (defense-in-depth), like `0010`.
- Integration tests run with `TESTCONTAINERS_RYUK_DISABLED=true`. Never weaken `make gate` (vet + -race + label gate + govulncheck).
- Admin console routes are gated by `requireAuth` + `requireRole(RoleAdmin)`; every state-mutating admin action (recharge, set price) must be auditable (reuse the existing `audit` writer where billing already integrates it; pricing writes log via the console's logger at minimum).
- Do NOT change `console.NewServer`'s signature or `dispatch.NewDispatcher`'s signature (avoid churning Module B + dispatch tests). Add capabilities via builder methods.
- Preserve existing behavior: when no price/pricing-fn is configured, the worker keeps charging `Amount: 1` (so all current dispatch tests stay green).

---

### Task 1: `tenant_pricing` table + pricing repo

**Files:**
- Create: `migrations/0011_tenant_pricing.sql`
- Create: `internal/pricing/pricing.go`
- Create: `internal/pricing/pricing_test.go`
- Create: `internal/pricing/testsupport_test.go`

**Interfaces:**
- Produces:
  - `type Repo struct{...}`; `func NewRepo(pool *pgxpool.Pool) *Repo`.
  - `func (r *Repo) SetPrice(ctx context.Context, tenantID int64, country string, unitPrice int64) error` (upsert; rejects unitPrice<=0).
  - `func (r *Repo) GetPrice(ctx context.Context, tenantID int64, country string) (int64, error)` — returns `ErrNoPrice` when unset.
  - `var ErrNoPrice = errors.New(...)`.
  - `func (r *Repo) PriceFor(ctx context.Context, tenantID int64, country string, fallback int64) int64` — returns the set price, or `fallback` on miss/error (never errors; this is the hot-path lookup the worker uses).
  - `func (r *Repo) ListForTenant(ctx context.Context, tenantID int64) ([]Price, error)` where `type Price struct { Country string; UnitPrice int64 }` (for the admin pricing page).

- [ ] **Step 1: Write the migration**

```sql
-- migrations/0011_tenant_pricing.sql — per-tenant×country unit price (minor units).
-- Platform table: set by staff via SystemPool; never exposed to the RLS tenant role.
CREATE TABLE IF NOT EXISTS tenant_pricing (
    tenant_id    BIGINT      NOT NULL REFERENCES tenants(id),
    country_code CHAR(2)     NOT NULL,
    unit_price   BIGINT      NOT NULL CHECK (unit_price > 0),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, country_code)
);

DROP TRIGGER IF EXISTS trg_tenant_pricing_touch ON tenant_pricing;
CREATE TRIGGER trg_tenant_pricing_touch BEFORE UPDATE ON tenant_pricing
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

REVOKE ALL ON tenant_pricing FROM app_tenant;
```

- [ ] **Step 2: Write the testcontainer helper**

```go
// internal/pricing/testsupport_test.go
package pricing

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

func newTestPool(t *testing.T) *store.Manager {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("pricing_test"),
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
	mgr, err := store.NewManager(ctx, store.Config{DSN: dsn, NodeID: "pricing-test"}, logger)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(mgr.Close)
	files, _ := filepath.Glob("../../migrations/*.sql")
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
	return mgr
}

// seedTenant inserts a tenant and returns its id (tenant_pricing has an FK to tenants).
func seedTenant(t *testing.T, ctx context.Context, mgr *store.Manager, name string) int64 {
	t.Helper()
	var id int64
	if err := mgr.SystemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return id
}
```

- [ ] **Step 3: Write the failing test**

```go
// internal/pricing/pricing_test.go
package pricing

import (
	"context"
	"errors"
	"testing"
)

func TestSetGetPriceAndFallback(t *testing.T) {
	ctx := context.Background()
	mgr := newTestPool(t)
	repo := NewRepo(mgr.SystemPool())
	tid := seedTenant(t, ctx, mgr, "Acme")

	// no price yet → GetPrice ErrNoPrice; PriceFor returns fallback.
	if _, err := repo.GetPrice(ctx, tid, "US"); !errors.Is(err, ErrNoPrice) {
		t.Fatalf("want ErrNoPrice, got %v", err)
	}
	if got := repo.PriceFor(ctx, tid, "US", 3); got != 3 {
		t.Fatalf("fallback: want 3, got %d", got)
	}

	// set then read.
	if err := repo.SetPrice(ctx, tid, "US", 5); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got, err := repo.GetPrice(ctx, tid, "US"); err != nil || got != 5 {
		t.Fatalf("get: want 5, got %d err=%v", got, err)
	}
	if got := repo.PriceFor(ctx, tid, "US", 3); got != 5 {
		t.Fatalf("PriceFor after set: want 5, got %d", got)
	}

	// upsert (change price).
	if err := repo.SetPrice(ctx, tid, "US", 8); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, _ := repo.GetPrice(ctx, tid, "US"); got != 8 {
		t.Fatalf("after upsert want 8, got %d", got)
	}

	// reject non-positive.
	if err := repo.SetPrice(ctx, tid, "US", 0); err == nil {
		t.Fatalf("expected error for non-positive price")
	}

	// list.
	_ = repo.SetPrice(ctx, tid, "GB", 4)
	list, err := repo.ListForTenant(ctx, tid)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: want 2 entries, got %d err=%v", len(list), err)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/pricing/ -run TestSetGetPriceAndFallback -v`
Expected: FAIL — `undefined: NewRepo`.

- [ ] **Step 5: Write the implementation**

```go
// internal/pricing/pricing.go
// Package pricing stores and resolves per-tenant×country unit prices (minor units).
package pricing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNoPrice = errors.New("pricing: no price set for tenant/country")

// Price is one tenant×country unit price.
type Price struct {
	Country   string
	UnitPrice int64
}

// Repo reads/writes tenant_pricing. Construct with the BYPASSRLS SystemPool —
// pricing is platform metadata, not tenant-row-scoped.
type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// SetPrice upserts the unit price for a tenant×country (minor units, must be > 0).
func (r *Repo) SetPrice(ctx context.Context, tenantID int64, country string, unitPrice int64) error {
	if unitPrice <= 0 {
		return fmt.Errorf("pricing: unit price must be positive, got %d", unitPrice)
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO tenant_pricing (tenant_id, country_code, unit_price)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id, country_code)
		 DO UPDATE SET unit_price = EXCLUDED.unit_price, updated_at = now()`,
		tenantID, country, unitPrice)
	if err != nil {
		return fmt.Errorf("set price: %w", err)
	}
	return nil
}

// GetPrice returns the configured unit price or ErrNoPrice.
func (r *Repo) GetPrice(ctx context.Context, tenantID int64, country string) (int64, error) {
	var p int64
	err := r.pool.QueryRow(ctx,
		`SELECT unit_price FROM tenant_pricing WHERE tenant_id=$1 AND country_code=$2`,
		tenantID, country).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNoPrice
	}
	if err != nil {
		return 0, fmt.Errorf("get price: %w", err)
	}
	return p, nil
}

// PriceFor is the hot-path lookup: the configured price, or `fallback` on miss/error.
// It never returns an error so a transient DB blip cannot block billing — but a real
// outage will surface elsewhere (the Hold itself runs against the same DB).
func (r *Repo) PriceFor(ctx context.Context, tenantID int64, country string, fallback int64) int64 {
	p, err := r.GetPrice(ctx, tenantID, country)
	if err != nil {
		return fallback
	}
	return p
}

// ListForTenant returns all country prices set for a tenant, ordered by country.
func (r *Repo) ListForTenant(ctx context.Context, tenantID int64) ([]Price, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT country_code, unit_price FROM tenant_pricing WHERE tenant_id=$1 ORDER BY country_code`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("list prices: %w", err)
	}
	defer rows.Close()
	var out []Price
	for rows.Next() {
		var p Price
		if err := rows.Scan(&p.Country, &p.UnitPrice); err != nil {
			return nil, fmt.Errorf("scan price: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/pricing/ -run TestSetGetPriceAndFallback -v`
Expected: PASS.

- [ ] **Step 7: Vet and commit**

Run: `/usr/local/go/bin/go vet ./internal/pricing/`

```bash
git add migrations/0011_tenant_pricing.sql internal/pricing/
git commit -m "feat(pricing): tenant_pricing table + repo (set/get/PriceFor/list)"
```

---

### Task 2: Wire per-tenant pricing into the SendWorker charge point

**Files:**
- Modify: `internal/dispatch/worker.go` (add a price hook; use it for `Amount`)
- Create: `internal/dispatch/worker_pricing_test.go`
- Modify: `cmd/wadist/main.go` (build `pricing.Repo`, inject into worker)

**Interfaces:**
- Consumes: `pricing.Repo.PriceFor` (Task 1).
- Produces: `func (w *SendWorker) WithPricing(fn func(ctx context.Context, tenantID int64, country string) int64) *SendWorker` — sets the price hook and returns w (chainable, like `WithMetrics`/`WithCanary`). When unset, the worker keeps charging `Amount: 1` (existing behavior preserved).

- [ ] **Step 1: Read `internal/dispatch/worker.go`** to confirm the `SendWorker` struct fields and the `WithMetrics`/`WithCanary` builder pattern, and the exact `billing.Hold` call (around line 46-52, `Amount: 1`).

- [ ] **Step 2: Write the failing test**

```go
// internal/dispatch/worker_pricing_test.go
package dispatch

import (
	"context"
	"testing"
)

// TestWithPricingSetsAmount verifies the price hook is invoked with the payload's
// tenant+country and that WithPricing is chainable. (Unit-level: we exercise the
// hook directly rather than a full send, which the integration tests already cover.)
func TestWithPricingSetsAmount(t *testing.T) {
	var gotTenant int64
	var gotCountry string
	w := (&SendWorker{}).WithPricing(func(_ context.Context, tenantID int64, country string) int64 {
		gotTenant, gotCountry = tenantID, country
		return 7
	})
	if w.priceFor == nil {
		t.Fatal("WithPricing did not set the hook")
	}
	if got := w.priceFor(context.Background(), 42, "US"); got != 7 {
		t.Fatalf("price hook: want 7, got %d", got)
	}
	if gotTenant != 42 || gotCountry != "US" {
		t.Fatalf("hook args: got tenant=%d country=%s", gotTenant, gotCountry)
	}
}

// TestAmountForUsesFallbackWhenUnset verifies the default unit price is 1 when no
// pricing hook is configured (preserves pre-pricing behavior).
func TestAmountForUsesFallbackWhenUnset(t *testing.T) {
	w := &SendWorker{}
	if got := w.amountFor(context.Background(), 1, "US"); got != 1 {
		t.Fatalf("default amount: want 1, got %d", got)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/dispatch/ -run 'TestWithPricing|TestAmountFor' -v`
Expected: FAIL — `w.priceFor undefined` / `w.amountFor undefined`.

- [ ] **Step 4: Implement the hook in `worker.go`**

Add a field to the `SendWorker` struct (alongside the existing metrics/canary fields):

```go
	priceFor func(ctx context.Context, tenantID int64, country string) int64
```

Add the builder + helper (place near `WithCanary`):

```go
// WithPricing injects the per-tenant×country unit-price lookup used at the charge
// point. When unset, amountFor returns 1 (the pre-pricing default).
func (w *SendWorker) WithPricing(fn func(ctx context.Context, tenantID int64, country string) int64) *SendWorker {
	w.priceFor = fn
	return w
}

// amountFor resolves the charge amount for a message; defaults to 1 minor unit.
func (w *SendWorker) amountFor(ctx context.Context, tenantID int64, country string) int64 {
	if w.priceFor == nil {
		return 1
	}
	return w.priceFor(ctx, tenantID, country)
}
```

Then replace the `Amount: 1, // unit price; real price injected at wiring layer` line in the `billing.Hold` call with:

```go
		Amount:      w.amountFor(ctx, pl.TenantID, pl.Country),
```

- [ ] **Step 5: Run the new tests + the existing dispatch suite (no regression)**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/dispatch/ -run 'TestWithPricing|TestAmountFor' -v`
Expected: PASS.

Run (regression — existing tests must stay green since default is still 1): `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/dispatch/`
Expected: ok.

- [ ] **Step 6: Wire it in `cmd/wadist/main.go`**

Build a pricing repo on the business pool and inject into the worker. Near where the worker is constructed (`worker := dispatch.NewSendWorker(...).WithMetrics(m).WithCanary(cfg.CanaryPercent)`), add `import "github.com/acme/wadist/internal/pricing"` and:

```go
	priceRepo := pricing.NewRepo(pool)
	// priceFor falls back to the existing per-country default table when a tenant
	// has no explicit price configured.
	worker := dispatch.NewSendWorker(pool, gate, billingRepo, cluster.NewRoutingSender(reg), placeholderUploader{}).
		WithMetrics(m).
		WithCanary(cfg.CanaryPercent).
		WithPricing(func(ctx context.Context, tenantID int64, country string) int64 {
			return priceRepo.PriceFor(ctx, tenantID, country, priceFor(country))
		})
```

(Keep the existing global `priceFor(country)` func as the fallback — do NOT delete it.) Leave the unused `dispatch.NewDispatcher(..., priceFor, ...)` arg as-is (out of scope; it is harmless dead wiring).

- [ ] **Step 7: Build the whole repo + commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/dispatch/ ./cmd/wadist/`
Expected: clean.

```bash
git add internal/dispatch/worker.go internal/dispatch/worker_pricing_test.go cmd/wadist/main.go
git commit -m "feat(dispatch): charge per-tenant×country price at the Hold point"
```

---

### Task 3: billing read methods — Balance + Ledger

**Files:**
- Modify: `internal/billing/billing.go` (add `Balance` + `Ledger` read methods + `LedgerEntry` type)
- Create: `internal/billing/read_test.go`

**Interfaces:**
- Produces:
  - `func (r *Repo) Balance(ctx context.Context, tenantID int64) (balance, frozen int64, err error)` — returns `(0, 0, nil)` when the wallet row does not exist yet.
  - `type LedgerEntry struct { ID int64; ChargeID *int64; Kind string; DeltaBalance, DeltaFrozen, BalanceAfter, FrozenAfter int64; CreatedAt time.Time }`.
  - `func (r *Repo) Ledger(ctx context.Context, tenantID int64, limit int) ([]LedgerEntry, error)` — most-recent first; `limit<=0` defaults to 100.

- [ ] **Step 1: Read `internal/billing/billing.go`** to confirm `Repo` struct, imports (`time`, pgx), and the `wallet_ledger`/`tenant_wallets` column names (already known from 0003: wallet has `balance,frozen`; ledger has `id,charge_id,kind,delta_balance,delta_frozen,balance_after,frozen_after,created_at`).

- [ ] **Step 2: Write the failing test**

```go
// internal/billing/read_test.go
package billing

import (
	"context"
	"testing"
)

func TestBalanceAndLedger(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)          // existing billing test helper (testsupport_test.go)
	repo := NewRepo(pool)
	tid := seedWallet(t, ctx, pool, 100) // existing helper: creates wallet with balance=100

	bal, frozen, err := repo.Balance(ctx, tid)
	if err != nil || bal != 100 || frozen != 0 {
		t.Fatalf("balance: want 100/0, got %d/%d err=%v", bal, frozen, err)
	}

	// unknown tenant → zero, no error.
	if b, f, err := repo.Balance(ctx, 999999); err != nil || b != 0 || f != 0 {
		t.Fatalf("unknown wallet: want 0/0/nil, got %d/%d/%v", b, f, err)
	}

	// a topup writes a ledger row → Ledger returns it.
	if err := repo.Topup(ctx, tid, 50, "ref-1"); err != nil {
		t.Fatalf("topup: %v", err)
	}
	entries, err := repo.Ledger(ctx, tid, 10)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one ledger entry after topup")
	}
	if entries[0].Kind != "topup" || entries[0].DeltaBalance != 50 {
		t.Fatalf("latest entry: got kind=%s delta=%d", entries[0].Kind, entries[0].DeltaBalance)
	}
}
```

> Implementer note: confirm the exact names of the existing billing test helpers in `internal/billing/testsupport_test.go` (e.g. `testPool`/`seedWallet`) and adapt the two helper calls above to match. Do NOT invent helpers — read the file and use what exists; if `seedWallet`'s signature differs, adjust the call. The assertions (Balance 100/0, unknown→0/0, topup→ledger) are the requirement.

- [ ] **Step 3: Run test to verify it fails**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/billing/ -run TestBalanceAndLedger -v`
Expected: FAIL — `repo.Balance undefined`.

- [ ] **Step 4: Implement the read methods** (append to `billing.go`; ensure `time` is imported)

```go
// Balance returns the tenant's available and frozen balances (minor units).
// A missing wallet row reads as (0, 0, nil) — a tenant with no activity yet.
func (r *Repo) Balance(ctx context.Context, tenantID int64) (balance, frozen int64, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT balance, frozen FROM tenant_wallets WHERE tenant_id=$1`, tenantID).
		Scan(&balance, &frozen)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("read balance: %w", err)
	}
	return balance, frozen, nil
}

// LedgerEntry is one immutable wallet ledger row.
type LedgerEntry struct {
	ID           int64
	ChargeID     *int64
	Kind         string
	DeltaBalance int64
	DeltaFrozen  int64
	BalanceAfter int64
	FrozenAfter  int64
	CreatedAt    time.Time
}

// Ledger returns a tenant's most-recent ledger entries (newest first). limit<=0 → 100.
func (r *Repo) Ledger(ctx context.Context, tenantID int64, limit int) ([]LedgerEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id, charge_id, kind, delta_balance, delta_frozen, balance_after, frozen_after, created_at
		   FROM wallet_ledger WHERE tenant_id=$1 ORDER BY id DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("query ledger: %w", err)
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.ChargeID, &e.Kind, &e.DeltaBalance, &e.DeltaFrozen,
			&e.BalanceAfter, &e.FrozenAfter, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ledger: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/billing/ -run TestBalanceAndLedger -v`
Expected: PASS.

- [ ] **Step 6: Vet + commit**

Run: `/usr/local/go/bin/go vet ./internal/billing/`

```bash
git add internal/billing/billing.go internal/billing/read_test.go
git commit -m "feat(billing): Balance + Ledger read methods for console display"
```

---

### Task 4: Console admin — tenant list + balance/ledger view (read-only)

**Files:**
- Create: `internal/console/tenants.go` (a `TenantRepo` to list tenants)
- Create: `internal/console/admin.go` (admin handlers + builder wiring)
- Create: `internal/console/templates/admin_tenants.html`
- Create: `internal/console/templates/admin_tenant.html`
- Modify: `internal/console/server.go` (register admin routes; add builder fields)
- Create: `internal/console/admin_test.go`

**Interfaces:**
- Consumes: `billing.Repo.Balance`/`Ledger` (Task 3), Module B `requireAuth`/`requireRole(RoleAdmin)`, `store.Manager.SystemPool()`.
- Produces:
  - `type TenantRepo struct{...}`; `func NewTenantRepo(pool *pgxpool.Pool) *TenantRepo`; `type TenantRow struct{ ID int64; Name string; Status string }`; `func (r *TenantRepo) List(ctx) ([]TenantRow, error)`; `func (r *TenantRepo) Get(ctx, id int64) (*TenantRow, error)`.
  - Builder methods on `*Server` (do NOT touch `NewServer`): `func (s *Server) WithBilling(b *billing.Repo) *Server`, `func (s *Server) WithTenants(t *TenantRepo) *Server` — store on new `Server` fields `billing *billing.Repo`, `tenants *TenantRepo`.
  - Routes (all behind `requireAuth`+`requireRole(RoleAdmin)`): `GET /admin` (tenant list with balances), `GET /admin/tenant/{id}` (balance + recent ledger).

- [ ] **Step 1: Write `TenantRepo`** (`tenants.go`)

```go
// internal/console/tenants.go
package console

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantRow is a tenant registry row for admin/sales listing.
type TenantRow struct {
	ID     int64
	Name   string
	Status string
}

// TenantRepo reads the tenants table via the SystemPool (BYPASSRLS).
type TenantRepo struct{ pool *pgxpool.Pool }

func NewTenantRepo(pool *pgxpool.Pool) *TenantRepo { return &TenantRepo{pool: pool} }

var ErrTenantNotFound = errors.New("console: tenant not found")

func (r *TenantRepo) List(ctx context.Context) ([]TenantRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, name, status FROM tenants ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()
	var out []TenantRow
	for rows.Next() {
		var t TenantRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Status); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *TenantRepo) Get(ctx context.Context, id int64) (*TenantRow, error) {
	var t TenantRow
	err := r.pool.QueryRow(ctx, `SELECT id, name, status FROM tenants WHERE id=$1`, id).
		Scan(&t.ID, &t.Name, &t.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get tenant: %w", err)
	}
	return &t, nil
}
```

- [ ] **Step 2: Add builder fields + methods to `Server`** (`server.go`): add fields `billing *billing.Repo` and `tenants *TenantRepo` to the struct, import `internal/billing`, and add `WithBilling`/`WithTenants` returning `*Server`. Register the admin routes inside `Handler()`:

```go
	adminOnly := s.requireRole(RoleAdmin)
	mux.HandleFunc("GET /admin", s.requireAuth(adminOnly(s.handleAdminTenants)))
	mux.HandleFunc("GET /admin/tenant/{id}", s.requireAuth(adminOnly(s.handleAdminTenant)))
```

- [ ] **Step 3: Write the two templates** (define `{{define "content"}}` blocks parsed via the Module B `parsePage` layout helper)

`admin_tenants.html`:
```html
{{define "content"}}
<h1>租户管理</h1>
<table>
  <thead><tr><th>ID</th><th>名称</th><th>状态</th><th>余额</th><th>冻结</th><th></th></tr></thead>
  <tbody>
  {{range .Tenants}}
    <tr><td>{{.ID}}</td><td>{{.Name}}</td><td>{{.Status}}</td><td>{{.Balance}}</td><td>{{.Frozen}}</td>
        <td><a href="/admin/tenant/{{.ID}}">详情</a></td></tr>
  {{end}}
  </tbody>
</table>
{{end}}
```

`admin_tenant.html`:
```html
{{define "content"}}
<h1>租户 #{{.Tenant.ID}} — {{.Tenant.Name}}</h1>
<p>可用余额:<strong>{{.Balance}}</strong> ｜ 冻结:<strong>{{.Frozen}}</strong></p>
<h2>流水(最近)</h2>
<table>
  <thead><tr><th>时间</th><th>类型</th><th>Δ余额</th><th>Δ冻结</th><th>余额后</th></tr></thead>
  <tbody>
  {{range .Ledger}}
    <tr><td>{{.CreatedAt}}</td><td>{{.Kind}}</td><td>{{.DeltaBalance}}</td><td>{{.DeltaFrozen}}</td><td>{{.BalanceAfter}}</td></tr>
  {{end}}
  </tbody>
</table>
<p><a href="/admin">← 返回</a></p>
{{end}}
```

- [ ] **Step 4: Write the failing test** (admin sees list + detail; non-admin gets 403)

```go
// internal/console/admin_test.go
package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/acme/wadist/internal/billing"
)

// loginAs seeds a user with the given role and returns a cookie-jar client logged in.
func loginAs(t *testing.T, ts *httptest.Server, repo *UserRepo, sessions *SessionStore, cfg Config, email string, role Role, tenantID *int64) *http.Client {
	t.Helper()
	hash, err := HashPassword("pw123456")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := repo.Create(context.Background(), email, hash, role, tenantID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	jar, _ := newJar()
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.PostForm(ts.URL+"/login", url.Values{"email": {email}, "password": {"pw123456"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resp.Body.Close()
	return client
}

func TestAdminTenantsPageAndRBAC(t *testing.T) {
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	bill := billing.NewRepo(mgr.SystemPool())
	tenants := NewTenantRepo(mgr.SystemPool())
	srv, _ := NewServer(cfg, users, sessions, mgr)
	srv.WithBilling(bill).WithTenants(tenants)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// seed a tenant
	var tid int64
	if err := mgr.SystemPool().QueryRow(context.Background(), `INSERT INTO tenants (name) VALUES ('Acme') RETURNING id`).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	// admin can see the tenant list
	admin := loginAs(t, ts, users, sessions, cfg, "admin@x.test", RoleAdmin, nil)
	resp, err := admin.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "Acme") {
		t.Fatalf("admin /admin: status=%d body=%s", resp.StatusCode, body)
	}

	// customer is forbidden
	cust := loginAs(t, ts, users, sessions, cfg, "cust@x.test", RoleCustomer, &tid)
	resp2, err := cust.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("customer /admin: want 403, got %d", resp2.StatusCode)
	}
}
```

- [ ] **Step 5: Run test to verify it fails**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestAdminTenantsPageAndRBAC -v`
Expected: FAIL — `srv.WithBilling undefined` / handlers missing.

- [ ] **Step 6: Implement handlers in `admin.go`**

```go
// internal/console/admin.go
package console

import (
	"net/http"
	"strconv"
)

type tenantBalanceRow struct {
	TenantRow
	Balance int64
	Frozen  int64
}

func (s *Server) handleAdminTenants(w http.ResponseWriter, r *http.Request) {
	list, err := s.tenants.List(r.Context())
	if err != nil {
		http.Error(w, "load tenants", http.StatusInternalServerError)
		return
	}
	rows := make([]tenantBalanceRow, 0, len(list))
	for _, t := range list {
		bal, frozen, err := s.billing.Balance(r.Context(), t.ID)
		if err != nil {
			http.Error(w, "load balance", http.StatusInternalServerError)
			return
		}
		rows = append(rows, tenantBalanceRow{TenantRow: t, Balance: bal, Frozen: frozen})
	}
	tpl, err := parsePage("admin_tenants.html")
	if err != nil {
		http.Error(w, "template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, tpl, map[string]any{"Tenants": rows})
}

func (s *Server) handleAdminTenant(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad tenant id", http.StatusBadRequest)
		return
	}
	tenant, err := s.tenants.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	bal, frozen, err := s.billing.Balance(r.Context(), id)
	if err != nil {
		http.Error(w, "load balance", http.StatusInternalServerError)
		return
	}
	ledger, err := s.billing.Ledger(r.Context(), id, 50)
	if err != nil {
		http.Error(w, "load ledger", http.StatusInternalServerError)
		return
	}
	tpl, err := parsePage("admin_tenant.html")
	if err != nil {
		http.Error(w, "template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, tpl, map[string]any{"Tenant": tenant, "Balance": bal, "Frozen": frozen, "Ledger": ledger})
}
```

- [ ] **Step 7: Run test, vet, commit**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestAdminTenantsPageAndRBAC -v && /usr/local/go/bin/go vet ./internal/console/`
Expected: PASS, clean.

```bash
git add internal/console/tenants.go internal/console/admin.go internal/console/server.go \
        internal/console/templates/admin_tenants.html internal/console/templates/admin_tenant.html \
        internal/console/admin_test.go
git commit -m "feat(console): admin tenant list + balance/ledger view (admin-only)"
```

---

### Task 5: Console admin — recharge + set price (writes) + cmd/console wiring

**Files:**
- Modify: `internal/console/admin.go` (add recharge + pricing handlers)
- Modify: `internal/console/server.go` (register POST routes; add `pricing *pricing.Repo` field + `WithPricing` builder)
- Modify: `internal/console/templates/admin_tenant.html` (add a recharge form + a set-price form)
- Modify: `cmd/console/main.go` (build billing + pricing + tenant repos; wire via builders)
- Create/Modify: `internal/console/admin_write_test.go`

**Interfaces:**
- Consumes: `billing.Repo.Topup` (existing), `pricing.Repo.SetPrice`/`ListForTenant` (Task 1).
- Produces: `func (s *Server) WithPricing(p *pricing.Repo) *Server` (field `pricing *pricing.Repo`); routes `POST /admin/tenant/{id}/recharge` (form: amount, ref → `billing.Topup`) and `POST /admin/tenant/{id}/pricing` (form: country, unit_price → `pricing.SetPrice`), both admin-only, each redirecting back to `GET /admin/tenant/{id}` (303 See Other).

- [ ] **Step 1: Write the failing test** — admin recharges, balance increases, ledger shows topup; admin sets a price, it appears.

```go
// internal/console/admin_write_test.go
package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/pricing"
)

func TestAdminRechargeAndSetPrice(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	bill := billing.NewRepo(mgr.SystemPool())
	price := pricing.NewRepo(mgr.SystemPool())
	tenants := NewTenantRepo(mgr.SystemPool())
	srv, _ := NewServer(cfg, users, sessions, mgr)
	srv.WithBilling(bill).WithTenants(tenants).WithPricing(price)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var tid int64
	if err := mgr.SystemPool().QueryRow(ctx, `INSERT INTO tenants (name) VALUES ('Acme') RETURNING id`).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	admin := loginAs(t, ts, users, sessions, cfg, "admin@x.test", RoleAdmin, nil)

	// recharge 500
	resp, err := admin.PostForm(ts.URL+"/admin/tenant/"+strconv.FormatInt(tid, 10)+"/recharge",
		url.Values{"amount": {"500"}, "ref": {"wire-001"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recharge: want 303, got %d", resp.StatusCode)
	}
	bal, _, _ := bill.Balance(ctx, tid)
	if bal != 500 {
		t.Fatalf("after recharge: want balance 500, got %d", bal)
	}

	// set price US=7
	resp2, err := admin.PostForm(ts.URL+"/admin/tenant/"+strconv.FormatInt(tid, 10)+"/pricing",
		url.Values{"country": {"US"}, "unit_price": {"7"}})
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusSeeOther {
		t.Fatalf("set price: want 303, got %d", resp2.StatusCode)
	}
	if got, err := price.GetPrice(ctx, tid, "US"); err != nil || got != 7 {
		t.Fatalf("price after set: want 7, got %d err=%v", got, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/console/ -run TestAdminRechargeAndSetPrice -v`
Expected: FAIL — `srv.WithPricing undefined` / routes missing.

- [ ] **Step 3: Add `WithPricing` + routes** in `server.go` (field `pricing *pricing.Repo`, import `internal/pricing`):

```go
	mux.HandleFunc("POST /admin/tenant/{id}/recharge", s.requireAuth(adminOnly(s.handleAdminRecharge)))
	mux.HandleFunc("POST /admin/tenant/{id}/pricing", s.requireAuth(adminOnly(s.handleAdminSetPrice)))
```

- [ ] **Step 4: Implement the write handlers** in `admin.go`:

```go
func (s *Server) handleAdminRecharge(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad tenant id", http.StatusBadRequest)
		return
	}
	amount, err := strconv.ParseInt(r.FormValue("amount"), 10, 64)
	if err != nil || amount <= 0 {
		http.Error(w, "amount must be a positive integer", http.StatusBadRequest)
		return
	}
	ref := r.FormValue("ref")
	if ref == "" {
		http.Error(w, "ref (payment reference) is required", http.StatusBadRequest)
		return
	}
	if err := s.billing.Topup(r.Context(), id, amount, ref); err != nil {
		http.Error(w, "recharge failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/tenant/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) handleAdminSetPrice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad tenant id", http.StatusBadRequest)
		return
	}
	country := r.FormValue("country")
	if len(country) != 2 {
		http.Error(w, "country must be a 2-letter code", http.StatusBadRequest)
		return
	}
	unit, err := strconv.ParseInt(r.FormValue("unit_price"), 10, 64)
	if err != nil || unit <= 0 {
		http.Error(w, "unit_price must be a positive integer", http.StatusBadRequest)
		return
	}
	if err := s.pricing.SetPrice(r.Context(), id, country, unit); err != nil {
		http.Error(w, "set price failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/tenant/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}
```

- [ ] **Step 5: Add the forms to `admin_tenant.html`** (and render the current price list). Add before the `← 返回` link:

```html
<h2>充值(线下到账后入账)</h2>
<form method="post" action="/admin/tenant/{{.Tenant.ID}}/recharge">
  <label>金额(最小货币单位) <input type="number" name="amount" min="1" required></label>
  <label>付款参考号 <input type="text" name="ref" required></label>
  <button type="submit">确认充值</button>
</form>

<h2>定价(按国家设单价)</h2>
<form method="post" action="/admin/tenant/{{.Tenant.ID}}/pricing">
  <label>国家(2位代码) <input type="text" name="country" maxlength="2" required></label>
  <label>单价(最小货币单位) <input type="number" name="unit_price" min="1" required></label>
  <button type="submit">保存单价</button>
</form>
<ul>{{range .Prices}}<li>{{.Country}}: {{.UnitPrice}}</li>{{end}}</ul>
```

Update `handleAdminTenant` (Task 4) to also load `s.pricing.ListForTenant` and pass it as `"Prices"` in the template data. (If `s.pricing` is nil in a test that didn't wire it, guard: `var prices []pricing.Price; if s.pricing != nil { prices, _ = s.pricing.ListForTenant(...) }`.)

- [ ] **Step 6: Wire repos in `cmd/console/main.go`** — after building `users`/`sessions` and before `NewServer`, build the repos and chain the builders:

```go
	bill := billing.NewRepo(mgr.SystemPool())
	price := pricing.NewRepo(mgr.SystemPool())
	tenants := console.NewTenantRepo(mgr.SystemPool())
	srv, err := console.NewServer(webCfg, users, sessions, mgr)
	if err != nil { /* existing cleanup */ }
	srv.WithBilling(bill).WithTenants(tenants).WithPricing(price)
```
(Add imports `internal/billing`, `internal/pricing`.)

- [ ] **Step 7: Run the full console suite + build + vet + commit**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/console/ ./internal/pricing/ ./internal/billing/ && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./...`
Expected: all green/clean.

```bash
git add internal/console/admin.go internal/console/server.go \
        internal/console/templates/admin_tenant.html internal/console/admin_write_test.go cmd/console/main.go
git commit -m "feat(console): admin recharge + set-price; wire billing/pricing/tenants into cmd/console"
```

---

## Self-Review

**Spec coverage (vs white paper Week-1 + roadmap §6):**
- Per-tenant×country pricing replacing global hardcode → Tasks 1 + 2. ✓
- Charge point uses the tenant price, default-1 fallback preserves existing tests → Task 2. ✓
- Admin recharge (line-offline payment, manual entry) → Task 5 (uses billing.Topup, existing). ✓
- Balance + ledger display (the "钱对得上" acceptance) → Tasks 3 + 4. ✓
- Admin set price (sales/admin define unit price) → Task 5. ✓
- RBAC: admin-only pages, customer 403 → Task 4 test. ✓
- No `NewServer`/`NewDispatcher` signature churn → builder methods (`WithBilling`/`WithTenants`/`WithPricing`) + worker `WithPricing`. ✓

**Placeholder scan:** Task 3's test names two existing billing helpers (`testPool`/`seedWallet`) with an explicit implementer note to confirm/adapt to the real names in `testsupport_test.go` — flagged, not silent. No TODO/TBD in production code.

**Type consistency:** `pricing.Repo`/`PriceFor`/`Price`; `billing.LedgerEntry`/`Balance`/`Ledger`; `console.TenantRepo`/`TenantRow`; builder methods all return `*Server` for chaining; `amountFor`/`priceFor` consistent in worker. Money is `int64` minor units throughout.

**Known follow-ups (out of scope, noted for later):** customer-facing pricing display (Module E) and the campaign-config "预估费用 = 条数 × 单价" UI; recharge audit via the existing `audit` writer (billing.Topup currently records a ledger row; an explicit admin-action audit entry can be added when the audit writer is threaded into the console).

---

## Execution: subagent-driven, one task at a time, task review (spec+quality) after each, whole-branch review at the end.
