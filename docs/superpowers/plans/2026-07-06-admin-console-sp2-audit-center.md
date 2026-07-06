# Admin Console SP2 — Audit Center Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the platform's audit trail real — instrument every admin write action to append an `audit_log` row, add a filtered `GET /admin/audit` read endpoint, and surface an Audit Log tab in the admin UI.

**Architecture:** All backend work lives in `internal/api` (HTTP layer) — a centralized audit helper on `Server`, per-handler instrumentation, and a read handler. No engine package (billing/dispatch/store/sendgate/cluster) is touched. Frontend adds a two-tab audit page. Testing is **hybrid**: pure-unit tests for the query builder and audit-arg mapping (no DB, matching `campaign_test.go`), plus one testcontainers DB round-trip for `recordAudit` + the read handler + campaign-stop. Per-handler "did it write a row" assertions are out (they'd need a full `Server` harness); those are covered by review + build.

**Tech Stack:** Go 1.26, gin, pgx/v5, testcontainers-go (postgres:16); Next.js 16.2.9 frontend.

## Global Constraints

- **No engine-package edits.** Only `internal/api/*` and `frontend/*` change. If a change seems to need `internal/{billing,dispatch,store,sendgate,cluster}`, stop — it's out of scope.
- **Append-only audit.** Never UPDATE/DELETE `audit_log`. Only INSERT + SELECT.
- **Never log or store secrets in `details`.** `user.password_reset` details is `{}` — the password must never appear in an audit row or a log line.
- **Best-effort writes never break the action.** `recordAudit` (post-action) logs on failure and returns; it must not surface an error to the user or undo the committed action. Only `recordAuditTx` (campaign stop/resume, in-tx) may fail the action.
- **Backend tests need Docker** (testcontainers). Run with `go test ./internal/api/...`. The pure-unit tests run without Docker.
- **Frontend:** no JS test runner — verify via `npm run build` + `eslint` from `/var/klwa/frontend`. `npm run lint` has a repo-wide, ACCEPTED `react-hooks/set-state-in-effect` error in every `admin-*` component; the bar is "no NEW error type / no unused-var in files you touch," confirmed via `npx eslint <file>`.
- **Money is integer cents** in any UI money rendering — use the existing `usd()` helper.
- Work on branch `feat/admin-sp2-audit-center`. Commit after each task with the exact message shown.
- Backend commits: run `go build ./... && go vet ./internal/api/...` before committing.

---

### Task 1: Audit helper core + pure-arg test + Server pool seam

Central helper so 12+ handlers don't repeat INSERT SQL, plus a test-only pool seam so the DB tests (Task 3+) don't need a full `store.Manager`.

**Files:**
- Create: `internal/api/audit.go`
- Modify: `internal/api/router.go` (add one field to `Server`)
- Test: `internal/api/audit_test.go`

**Interfaces:**
- Produces (used by Tasks 2–5): `type auditEvent struct{ TenantID, ActorID int64; Action, ResourceType string; ResourceID int64; Details any }`; `func actorID(c *gin.Context) int64`; `func auditArgs(e auditEvent) []any`; `func (s *Server) systemPool() *pgxpool.Pool`; `func (s *Server) recordAudit(ctx context.Context, e auditEvent)`; `func (s *Server) recordAuditTx(ctx context.Context, tx pgx.Tx, e auditEvent) error`.
- Consumes: existing `sessionFrom(c) *sessionData` (has `.UserID`), `s.deps.Mgr.SystemPool()`.

- [ ] **Step 1: Add the test-only pool seam to `Server`**

In `internal/api/router.go`, add a field to the `Server` struct (after `deps Deps`):

```go
	// sysPool, when non-nil, overrides deps.Mgr.SystemPool() — set ONLY by tests
	// so audit read/write can run against a raw pool without a full store.Manager.
	sysPool *pgxpool.Pool
```

Add the pgxpool import to `router.go` if not already present: `"github.com/jackc/pgx/v5/pgxpool"`.

- [ ] **Step 2: Write the failing pure-arg test**

Create `internal/api/audit_test.go`:

```go
package api

import (
	"encoding/json"
	"testing"
)

func TestAuditArgs(t *testing.T) {
	// Zero ids → nil (SQL NULL); empty resource_type → nil; details marshaled.
	args := auditArgs(auditEvent{
		TenantID: 7, ActorID: 0, Action: "finance.topup",
		ResourceType: "tenant", ResourceID: 7,
		Details: map[string]any{"amount": 100, "ref": "x"},
	})
	if len(args) != 6 {
		t.Fatalf("want 6 args, got %d", len(args))
	}
	if args[0] != int64(7) {
		t.Errorf("tenant arg = %v, want 7", args[0])
	}
	if args[1] != nil {
		t.Errorf("zero actor should be nil, got %v", args[1])
	}
	if args[2] != "finance.topup" {
		t.Errorf("action = %v", args[2])
	}
	if args[3] != "tenant" {
		t.Errorf("resource_type = %v", args[3])
	}
	details, ok := args[5].([]byte)
	if !ok {
		t.Fatalf("details arg not []byte: %T", args[5])
	}
	var m map[string]any
	if err := json.Unmarshal(details, &m); err != nil || m["ref"] != "x" {
		t.Errorf("details json = %s (err %v)", details, err)
	}

	// nil Details → "{}"; empty resource_type → nil.
	args = auditArgs(auditEvent{Action: "user.password_reset"})
	if args[3] != nil {
		t.Errorf("empty resource_type should be nil, got %v", args[3])
	}
	if string(args[5].([]byte)) != "{}" {
		t.Errorf("nil details should marshal to {}, got %s", args[5])
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestAuditArgs`
Expected: FAIL — `undefined: auditArgs` / `auditEvent`.

- [ ] **Step 4: Implement `internal/api/audit.go`**

```go
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const auditInsertSQL = `
INSERT INTO audit_log (tenant_id, actor_id, action, resource_type, resource_id, details)
VALUES ($1, $2, $3, $4, $5, $6)`

// auditEvent is one row to append to audit_log. Zero id fields → SQL NULL.
type auditEvent struct {
	TenantID     int64  // 0 → NULL
	ActorID      int64  // 0 → NULL
	Action       string // required, e.g. "finance.topup"
	ResourceType string // "" → NULL
	ResourceID   int64  // 0 → NULL
	Details      any    // JSON-marshaled; nil / marshal-error → "{}"
}

// actorID returns the acting session user id, or 0 when unauthenticated.
func actorID(c *gin.Context) int64 {
	if sd := sessionFrom(c); sd != nil {
		return sd.UserID
	}
	return 0
}

// auditArgs maps an event to the 6 positional INSERT args.
func auditArgs(e auditEvent) []any {
	var details []byte
	if e.Details != nil {
		if b, err := json.Marshal(e.Details); err == nil {
			details = b
		}
	}
	if details == nil {
		details = []byte("{}")
	}
	return []any{
		nilIfZero(e.TenantID), nilIfZero(e.ActorID), e.Action,
		nilIfEmpty(e.ResourceType), nilIfZero(e.ResourceID), details,
	}
}

func nilIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// systemPool returns the BYPASSRLS pool. sysPool is a test-only override.
func (s *Server) systemPool() *pgxpool.Pool {
	if s.sysPool != nil {
		return s.sysPool
	}
	return s.deps.Mgr.SystemPool()
}

// recordAudit appends best-effort AFTER the action committed. On failure it logs
// and returns — it never fails the caller's already-committed action.
func (s *Server) recordAudit(ctx context.Context, e auditEvent) {
	if _, err := s.systemPool().Exec(ctx, auditInsertSQL, auditArgs(e)...); err != nil {
		log.Printf("[audit] record %s: %v", e.Action, err)
	}
}

// recordAuditTx appends within an existing tx (atomic with the action). Returns
// the error so the caller can roll the whole action back on audit failure.
func (s *Server) recordAuditTx(ctx context.Context, tx pgx.Tx, e auditEvent) error {
	if _, err := tx.Exec(ctx, auditInsertSQL, auditArgs(e)...); err != nil {
		return fmt.Errorf("audit %s: %w", e.Action, err)
	}
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes + build**

Run: `go test ./internal/api/ -run TestAuditArgs && go build ./... && go vet ./internal/api/...`
Expected: PASS, clean build/vet.

- [ ] **Step 6: Commit**

```bash
git add internal/api/audit.go internal/api/audit_test.go internal/api/router.go
git commit -m "feat(api): audit helper (recordAudit/recordAuditTx) + Server pool seam

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Audit list query builder + pure tests

The dynamic-WHERE + limit/offset logic, extracted pure so it is unit-testable without a DB (the read handler in Task 3 calls it).

**Files:**
- Modify: `internal/api/audit.go`
- Test: `internal/api/audit_test.go`

**Interfaces:**
- Produces (used by Task 3): `type auditFilter struct{ Action string; ActorID, TenantID int64; Since, Until time.Time; Limit, Offset int }`; `func buildAuditWhere(f auditFilter) (string, []any)`; `func buildAuditListSQL(f auditFilter) (string, []any)`; `func buildAuditCountSQL(f auditFilter) (string, []any)`; `const auditSelectBase`.

- [ ] **Step 1: Write failing tests**

Append to `internal/api/audit_test.go` (add `"time"` and `"strings"` to its imports):

```go
func TestBuildAuditWhere_empty(t *testing.T) {
	where, args := buildAuditWhere(auditFilter{})
	if where != "" || len(args) != 0 {
		t.Errorf("empty filter: where=%q args=%v", where, args)
	}
}

func TestBuildAuditWhere_filters(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	where, args := buildAuditWhere(auditFilter{Action: "finance.topup", ActorID: 3, Since: since})
	// three conditions, joined by AND, params $1..$3 in add-order
	if !strings.Contains(where, "a.action = $1") ||
		!strings.Contains(where, "a.actor_id = $2") ||
		!strings.Contains(where, "a.occurred_at >= $3") {
		t.Errorf("where missing conditions: %q", where)
	}
	if strings.Count(where, " AND ") != 2 {
		t.Errorf("want 2 ANDs, got %q", where)
	}
	if len(args) != 3 || args[0] != "finance.topup" || args[1] != int64(3) || args[2] != since {
		t.Errorf("args = %v", args)
	}
}

func TestBuildAuditListSQL_clampAndPaging(t *testing.T) {
	// limit 0 → 100; offset default 0; limit/offset are the last two args.
	q, args := buildAuditListSQL(auditFilter{})
	if !strings.Contains(q, "ORDER BY a.id DESC") {
		t.Errorf("missing order: %q", q)
	}
	if len(args) != 2 || args[0] != 100 || args[1] != 0 {
		t.Errorf("default limit/offset args = %v", args)
	}
	// limit > 500 → 500
	_, args = buildAuditListSQL(auditFilter{Limit: 999, Offset: 40})
	if args[0] != 500 || args[1] != 40 {
		t.Errorf("clamp args = %v", args)
	}
	// with a filter, limit/offset params come after the filter param
	q, args = buildAuditListSQL(auditFilter{Action: "x", Limit: 10})
	if !strings.Contains(q, "LIMIT $2 OFFSET $3") {
		t.Errorf("param numbering wrong: %q", q)
	}
	if len(args) != 3 || args[0] != "x" || args[1] != 10 || args[2] != 0 {
		t.Errorf("args = %v", args)
	}
}

func TestBuildAuditCountSQL(t *testing.T) {
	q, args := buildAuditCountSQL(auditFilter{TenantID: 9})
	if !strings.HasPrefix(q, "SELECT count(*) FROM audit_log a") || !strings.Contains(q, "a.tenant_id = $1") {
		t.Errorf("count sql = %q", q)
	}
	if len(args) != 1 || args[0] != int64(9) {
		t.Errorf("args = %v", args)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/api/ -run TestBuildAudit`
Expected: FAIL — undefined `auditFilter` / `buildAudit*`.

- [ ] **Step 3: Implement the builders in `audit.go`**

Add `"strings"` and `"time"` to `audit.go` imports, then append:

```go
// auditFilter is the parsed, validated query for the audit list endpoint.
type auditFilter struct {
	Action   string
	ActorID  int64
	TenantID int64
	Since    time.Time // zero = unset
	Until    time.Time // zero = unset
	Limit    int
	Offset   int
}

const auditSelectBase = `
SELECT a.id, a.occurred_at::text, a.tenant_id, t.name, a.actor_id, u.email,
       a.action, a.resource_type, a.resource_id, a.details
  FROM audit_log a
  LEFT JOIN tenants t        ON t.id = a.tenant_id
  LEFT JOIN console_users u  ON u.id = a.actor_id`

// buildAuditWhere returns the " WHERE ..." clause (or "") and its positional args.
func buildAuditWhere(f auditFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(tmpl string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(tmpl, len(args)))
	}
	if f.Action != "" {
		add("a.action = $%d", f.Action)
	}
	if f.ActorID != 0 {
		add("a.actor_id = $%d", f.ActorID)
	}
	if f.TenantID != 0 {
		add("a.tenant_id = $%d", f.TenantID)
	}
	if !f.Since.IsZero() {
		add("a.occurred_at >= $%d", f.Since)
	}
	if !f.Until.IsZero() {
		add("a.occurred_at <= $%d", f.Until)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func clampLimit(n int) int {
	if n <= 0 {
		return 100
	}
	if n > 500 {
		return 500
	}
	return n
}

// buildAuditListSQL returns the full ordered/paginated list query and its args.
func buildAuditListSQL(f auditFilter) (string, []any) {
	where, args := buildAuditWhere(f)
	off := f.Offset
	if off < 0 {
		off = 0
	}
	args = append(args, clampLimit(f.Limit), off)
	q := auditSelectBase + where +
		fmt.Sprintf(" ORDER BY a.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	return q, args
}

// buildAuditCountSQL returns the total-count query for the same filter.
func buildAuditCountSQL(f auditFilter) (string, []any) {
	where, args := buildAuditWhere(f)
	return "SELECT count(*) FROM audit_log a" + where, args
}
```

- [ ] **Step 4: Run to verify pass + build**

Run: `go test ./internal/api/ -run 'TestBuildAudit|TestAuditArgs' && go build ./...`
Expected: PASS, clean.

- [ ] **Step 5: Commit**

```bash
git add internal/api/audit.go internal/api/audit_test.go
git commit -m "feat(api): audit list query builder (dynamic filters + limit clamp)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: DB test harness + read endpoint + round-trip test

Adds the api-package testcontainers harness and the `GET /admin/audit` handler, tested end-to-end (this also exercises `recordAudit`).

**Files:**
- Create: `internal/api/testsupport_test.go`
- Modify: `internal/api/admin_api.go` (add `auditRow`, `parseAuditFilter`, `handleAdminListAudit`)
- Modify: `internal/api/router.go` (register route)
- Test: `internal/api/audit_db_test.go`

**Interfaces:**
- Consumes: `buildAuditListSQL`/`buildAuditCountSQL`/`auditFilter` (Task 2); `recordAudit`/`systemPool` (Task 1).
- Produces: route `GET /api/v1/admin/audit`; `type auditRow` (JSON as in the spec); test helpers `testPool(t)`, `applyAllMigrations(t,ctx,pool)`.

- [ ] **Step 1: Create the DB harness `internal/api/testsupport_test.go`**

Copied from the proven `internal/store` pattern (glob-and-apply all migrations against a superuser testcontainers pool):

```go
package api

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func testDSN(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("app_test"),
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
	return dsn
}

// applyAllMigrations runs every forward migration in order against pool.
func applyAllMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
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

// testPool returns a migrated superuser pool for api DB tests.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyAllMigrations(t, ctx, pool)
	return pool
}
```

- [ ] **Step 2: Write the failing read-endpoint DB test**

Create `internal/api/audit_db_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// seedTenantUser inserts a tenant + an admin console user, returning their ids.
func seedTenantUser(t *testing.T, ctx context.Context, s *Server, email string) (int64, int64) {
	t.Helper()
	var tid, uid int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO tenants (name, status) VALUES ('Acme','active') RETURNING id`).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO console_users (email, password_hash, role) VALUES ($1,'x','admin') RETURNING id`, email).Scan(&uid); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return tid, uid
}

func TestHandleAdminListAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	s := &Server{sysPool: testPool(t)}
	tid, uid := seedTenantUser(t, ctx, s, "admin@acme.test")

	// Two distinct audit events.
	s.recordAudit(ctx, auditEvent{TenantID: tid, ActorID: uid, Action: "finance.topup",
		ResourceType: "tenant", ResourceID: tid, Details: map[string]any{"amount": 100, "ref": "r1"}})
	s.recordAudit(ctx, auditEvent{TenantID: tid, ActorID: uid, Action: "user.disable",
		ResourceType: "user", ResourceID: uid, Details: map[string]any{"disabled": true}})

	call := func(query string) map[string]any {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/audit"+query, nil)
		s.handleAdminListAudit(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d body %s", w.Code, w.Body.String())
		}
		var env struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return env.Data
	}

	// No filter: both rows, total 2, newest first, join populated.
	all := call("")
	rows := all["rows"].([]any)
	if len(rows) != 2 || all["total"].(float64) != 2 {
		t.Fatalf("want 2 rows/total, got rows=%d total=%v", len(rows), all["total"])
	}
	first := rows[0].(map[string]any)
	if first["action"] != "user.disable" { // id DESC → last inserted first
		t.Errorf("newest-first broken: %v", first["action"])
	}
	if first["actor_email"] != "admin@acme.test" || first["tenant_name"] != "Acme" {
		t.Errorf("join missing: %v", first)
	}

	// Filter by action.
	only := call("?action=finance.topup")
	if len(only["rows"].([]any)) != 1 || only["total"].(float64) != 1 {
		t.Errorf("action filter: %v", only)
	}
	// Filter by actor.
	if call("?actor_id=999999")["total"].(float64) != 0 {
		t.Errorf("unknown actor should return 0")
	}
	// Pagination: limit 1 → 1 row but total still 2.
	pg := call("?limit=1")
	if len(pg["rows"].([]any)) != 1 || pg["total"].(float64) != 2 {
		t.Errorf("limit paging: %v", pg)
	}
}
```

- [ ] **Step 3: Run to verify fail**

Run: `go test ./internal/api/ -run TestHandleAdminListAudit`
Expected: FAIL — `s.handleAdminListAudit` undefined.

- [ ] **Step 4: Implement the handler + filter parser in `admin_api.go`**

Add near the other admin types/handlers (ensure imports include `"encoding/json"`, `"strconv"`, `"time"`, `"net/http"` — most already present):

```go
type auditRow struct {
	ID           int64           `json:"id"`
	OccurredAt   string          `json:"occurred_at"`
	TenantID     *int64          `json:"tenant_id"`
	TenantName   *string         `json:"tenant_name"`
	ActorID      *int64          `json:"actor_id"`
	ActorEmail   *string         `json:"actor_email"`
	Action       string          `json:"action"`
	ResourceType *string         `json:"resource_type"`
	ResourceID   *int64          `json:"resource_id"`
	Details      json.RawMessage `json:"details"`
}

// parseAuditFilter reads the optional query params for the audit list.
func parseAuditFilter(c *gin.Context) (auditFilter, error) {
	f := auditFilter{Action: c.Query("action")}
	if v := c.Query("actor_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return f, fmt.Errorf("bad actor_id")
		}
		f.ActorID = n
	}
	if v := c.Query("tenant_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return f, fmt.Errorf("bad tenant_id")
		}
		f.TenantID = n
	}
	if v := c.Query("since"); v != "" {
		ts, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, fmt.Errorf("bad since (want RFC3339)")
		}
		f.Since = ts
	}
	if v := c.Query("until"); v != "" {
		ts, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, fmt.Errorf("bad until (want RFC3339)")
		}
		f.Until = ts
	}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Offset = n
		}
	}
	return f, nil
}

// handleAdminListAudit: GET /api/v1/admin/audit — filtered, paginated audit log
// with actor-email and tenant-name joins.
func (s *Server) handleAdminListAudit(c *gin.Context) {
	f, err := parseAuditFilter(c)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	ctx := c.Request.Context()
	listSQL, listArgs := buildAuditListSQL(f)
	rows, err := s.systemPool().Query(ctx, listSQL, listArgs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "audit query")
		return
	}
	defer rows.Close()
	out := make([]auditRow, 0)
	for rows.Next() {
		var r auditRow
		var details []byte
		if err := rows.Scan(&r.ID, &r.OccurredAt, &r.TenantID, &r.TenantName,
			&r.ActorID, &r.ActorEmail, &r.Action, &r.ResourceType, &r.ResourceID, &details); err != nil {
			fail(c, http.StatusInternalServerError, "scan audit")
			return
		}
		r.Details = details
		out = append(out, r)
	}
	countSQL, countArgs := buildAuditCountSQL(f)
	var total int64
	if err := s.systemPool().QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "audit count")
		return
	}
	ok(c, gin.H{"rows": out, "total": total})
}
```

- [ ] **Step 5: Register the route**

In `internal/api/router.go`, inside the `admin` group (near `admin.GET("/campaigns", ...)`), add:

```go
		admin.GET("/audit", s.handleAdminListAudit)
```

- [ ] **Step 6: Run test to verify pass**

Run: `go test ./internal/api/ -run TestHandleAdminListAudit -v`
Expected: PASS (Docker required; testcontainers pulls postgres:16 on first run).

- [ ] **Step 7: Full build/vet + commit**

Run: `go build ./... && go vet ./internal/api/...`

```bash
git add internal/api/testsupport_test.go internal/api/audit_db_test.go internal/api/admin_api.go internal/api/router.go
git commit -m "feat(api): GET /admin/audit read endpoint (filters+paging+joins) + db harness

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Instrument the best-effort write handlers

Add `s.recordAudit(...)` to the 12 admin write handlers that commit through their own path. No automated per-handler test (would need a full `Server` harness — out of scope per the hybrid decision); verification is build/vet + code review that each call sits on the success path with the correct action/resource/details.

**Files:**
- Modify: `internal/api/admin_api.go`

**Interfaces:**
- Consumes: `s.recordAudit`, `actorID`, `auditEvent` (Task 1).

- [ ] **Step 1: Add the audit calls**

In each handler below, insert the call on the SUCCESS path, immediately before the final `ok(c, ...)`. Use `ctx := c.Request.Context()` if the handler doesn't already have `ctx` in scope. `actorID(c)` supplies the actor.

`handleAdminTopup` (before `ok`):
```go
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: req.TenantID, ActorID: actorID(c),
		Action: "finance.topup", ResourceType: "tenant", ResourceID: req.TenantID,
		Details: map[string]any{"amount": req.Amount, "ref": req.Ref}})
```

`handleAdminSetPricing`:
```go
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: req.TenantID, ActorID: actorID(c),
		Action: "finance.pricing", ResourceType: "tenant", ResourceID: req.TenantID,
		Details: map[string]any{"country": req.Country, "unit_price": req.UnitPrice}})
```
(Use the actual field names on that handler's `req` struct — confirm `Country`/`UnitPrice` match; adjust to the real names if they differ.)

`handleAdminCreateTenant` (after the RETURNING id scan, before `ok`; `id` holds the new tenant id):
```go
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: id, ActorID: actorID(c),
		Action: "tenant.create", ResourceType: "tenant", ResourceID: id,
		Details: map[string]any{"name": req.Name}})
```

`handleAdminCreateUser` (after `s.deps.Users.Create` returns `id`):
```go
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "user.create", ResourceType: "user", ResourceID: id,
		Details: map[string]any{"email": req.Email, "role": req.Role, "tenant_id": req.TenantID}})
```

`handleAdminSetUserDisabled` (`id` = target user id, `req.Disabled` the new state):
```go
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "user.disable", ResourceType: "user", ResourceID: id,
		Details: map[string]any{"disabled": req.Disabled}})
```

`handleAdminResetUserPassword` (`id` = target user id — details is empty, NEVER the password):
```go
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "user.password_reset", ResourceType: "user", ResourceID: id})
```

`handleAdminAssignSales` (`tenantID`, `req.SalesUserID`):
```go
	s.recordAudit(c.Request.Context(), auditEvent{TenantID: tenantID, ActorID: actorID(c),
		Action: "sales.assign", ResourceType: "tenant", ResourceID: tenantID,
		Details: map[string]any{"sales_user_id": req.SalesUserID}})
```

`handleAdminUpdateRiskConfig` (after the successful UPSERT `Exec`, before `ok`):
```go
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "risk.update", ResourceType: "system_risk_config", ResourceID: 1,
		Details: map[string]any{
			"min_delay_seconds": *req.MinDelaySeconds, "max_delay_seconds": *req.MaxDelaySeconds,
			"daily_limit_per_device": *req.DailyLimitPerDevice, "ban_rate_circuit_breaker": *req.BanRateCircuitBreaker,
			"circuit_breaker_enabled": req.CircuitBreakerEnabled, "circuit_breaker_dry_run": req.CircuitBreakerDryRun,
			"min_sample": *req.MinSample, "window_seconds": *req.WindowSeconds, "eval_interval_seconds": *req.EvalIntervalSeconds}})
```

`handleAdminImportProxies` (before the `ok(c, gin.H{"submitted": ...})`; reuse the computed `imported`):
```go
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "proxy.import", ResourceType: "proxy",
		Details: map[string]any{"submitted": len(req.Proxies), "imported": imported, "skipped": len(req.Proxies) - imported}})
```

`handleAdminImportDevices` (mirror, with `req.Devices`):
```go
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "device.import", ResourceType: "device",
		Details: map[string]any{"submitted": len(req.Devices), "imported": imported, "skipped": len(req.Devices) - imported}})
```

`handleAdminBindDeviceProxy` (`id` = device id from `c.Param("id")`; `req.ProxyID` or the actual bound proxy id field — confirm the struct field name):
```go
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "device.bind_proxy", ResourceType: "device", ResourceID: id,
		Details: map[string]any{"proxy_id": req.ProxyID}})
```

`handleAdminUnbindDeviceProxy` (`id` = device id):
```go
	s.recordAudit(c.Request.Context(), auditEvent{ActorID: actorID(c),
		Action: "device.unbind_proxy", ResourceType: "device", ResourceID: id})
```

Where a handler already parses its path/body id into a differently-named variable (e.g. `deviceID` instead of `id`), use that handler's actual variable — do not introduce a new parse.

- [ ] **Step 2: Build + vet**

Run: `go build ./... && go vet ./internal/api/...`
Expected: clean. Fix any field-name mismatches surfaced by the compiler (the struct field names above must match each handler's real `req` struct).

- [ ] **Step 3: Re-run the audit tests (no regression)**

Run: `go test ./internal/api/ -run 'Audit'`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/api/admin_api.go
git commit -m "feat(api): audit all admin write actions (topup/pricing/user/tenant/sales/risk/resources)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Campaign stop — wrap in tx + atomic audit

`handleAdminStopCampaign` is currently a single `UPDATE`; make the update + audit atomic (mirroring resume) and audit only when a running campaign was actually paused. This one gets a DB test (needs only `sysPool`).

**Files:**
- Modify: `internal/api/admin_api.go` (`handleAdminStopCampaign`)
- Test: `internal/api/audit_db_test.go`

**Interfaces:**
- Consumes: `recordAuditTx` (Task 1); `testPool` (Task 3).

- [ ] **Step 1: Write the failing DB test**

Append to `internal/api/audit_db_test.go`:

```go
func seedRunningCampaign(t *testing.T, ctx context.Context, s *Server, tenantID int64) int64 {
	t.Helper()
	var tmpl, camp int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES ($1,'text','hi') RETURNING id`, tenantID).Scan(&tmpl); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state) VALUES ($1,$2,'running') RETURNING id`, tenantID, tmpl).Scan(&camp); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	return camp
}

func TestHandleAdminStopCampaign_auditAtomic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	s := &Server{sysPool: testPool(t)}
	tid, _ := seedTenantUser(t, ctx, s, "op@acme.test")
	camp := seedRunningCampaign(t, ctx, s, tid)

	stop := func() int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/campaigns/stop", nil)
		c.Params = gin.Params{{Key: "id", Value: strconvI(camp)}}
		s.handleAdminStopCampaign(c)
		return w.Code
	}

	if code := stop(); code != http.StatusOK {
		t.Fatalf("first stop status = %d", code)
	}
	// state paused + exactly one campaign.stop audit row.
	var state string
	s.systemPool().QueryRow(ctx, `SELECT state::text FROM campaigns WHERE id=$1`, camp).Scan(&state)
	if state != "paused" {
		t.Errorf("state = %q, want paused", state)
	}
	var n int
	s.systemPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='campaign.stop' AND resource_id=$1`, camp).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 stop audit row, got %d", n)
	}

	// Second stop: already paused → 409 and NO new audit row.
	if code := stop(); code != http.StatusConflict {
		t.Errorf("second stop status = %d, want 409", code)
	}
	s.systemPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='campaign.stop' AND resource_id=$1`, camp).Scan(&n)
	if n != 1 {
		t.Errorf("no-op stop must not audit; count now %d", n)
	}
}
```

Add a tiny helper at the bottom of the test file (avoids importing strconv just for this):

```go
func strconvI(v int64) string { return fmt.Sprintf("%d", v) }
```

and add `"fmt"` to the test file's imports.

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/api/ -run TestHandleAdminStopCampaign_auditAtomic`
Expected: FAIL — either no audit row (current handler writes none) or the count assertion fails.

- [ ] **Step 3: Rewrite `handleAdminStopCampaign` to be tx + atomic audit**

Replace the handler body with a `BeginTxFunc` mirroring resume (`admin_api.go` resume handler is the reference). Ensure `github.com/jackc/pgx/v5` is imported:

```go
func (s *Server) handleAdminStopCampaign(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad campaign id")
		return
	}
	ctx := c.Request.Context()
	paused := false
	err = pgx.BeginTxFunc(ctx, s.systemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
		var tenantID int64
		e := tx.QueryRow(ctx,
			`UPDATE campaigns SET state='paused' WHERE id=$1 AND state='running' RETURNING tenant_id`, id).
			Scan(&tenantID)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil // not running / not found; paused stays false
		}
		if e != nil {
			return e
		}
		paused = true
		return s.recordAuditTx(ctx, tx, auditEvent{TenantID: tenantID, ActorID: actorID(c),
			Action: "campaign.stop", ResourceType: "campaign", ResourceID: id})
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "stop campaign failed")
		return
	}
	if !paused {
		fail(c, http.StatusConflict, "campaign not found or not in 'running' state")
		return
	}
	ok(c, gin.H{"id": id, "state": "paused"})
}
```

(Keep the exact conflict message/behavior the frontend expects — the SP1 campaigns component disables Stop unless `state==='running'` and treats 409 as a no-op.)

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/api/ -run TestHandleAdminStopCampaign_auditAtomic -v`
Expected: PASS.

- [ ] **Step 5: Full build/vet + full api test + commit**

Run: `go build ./... && go vet ./internal/api/... && go test ./internal/api/ -run 'Audit|Stop'`

```bash
git add internal/api/admin_api.go internal/api/audit_db_test.go
git commit -m "feat(api): campaign.stop writes audit atomically (tx), no-op stop audits nothing

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Frontend — two-tab audit page + audit log table

`/admin/audit` becomes two tabs; the existing campaign view is preserved as the second tab.

**Files:**
- Modify: `frontend/app/admin/audit/page.tsx`
- Create: `frontend/components/admin-audit-tabs.tsx`
- Create: `frontend/components/admin-audit-log.tsx`

**Interfaces:**
- Consumes: `GET /admin/audit` → `{ rows: AuditRow[], total: number }`; the existing `AdminCampaigns` component; `ProDataTable`, `Badge`, `api`/`ApiError`.
- `AuditRow` (matches backend `auditRow`): `{ id:number; occurred_at:string; tenant_id:number|null; tenant_name:string|null; actor_id:number|null; actor_email:string|null; action:string; resource_type:string|null; resource_id:number|null; details:unknown }`.

- [ ] **Step 1: Point the page at the new tabs component**

Replace the body of `frontend/app/admin/audit/page.tsx`'s render so it uses `AdminAuditTabs` instead of `AdminCampaigns` (keep the existing `PageHeader`):

```tsx
import { PageHeader } from "@/components/admin/page-header";
import { AdminAuditTabs } from "@/components/admin-audit-tabs";

export default function AdminAuditPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="God View · Risk"
        title="风控与审计"
        description="平台操作审计日志与全租户群发任务监控。"
      />
      <AdminAuditTabs />
    </div>
  );
}
```

- [ ] **Step 2: Create the two-tab container**

Create `frontend/components/admin-audit-tabs.tsx` — a lightweight local segmented control (no new shared primitive):

```tsx
"use client";

import { useState } from "react";
import { cn } from "@/lib/utils";
import { AdminAuditLog } from "@/components/admin-audit-log";
import { AdminCampaigns } from "@/components/admin-campaigns";

type Tab = "audit" | "tasks";

const TABS: { key: Tab; label: string }[] = [
  { key: "audit", label: "审计日志" },
  { key: "tasks", label: "任务监控" },
];

export function AdminAuditTabs() {
  const [tab, setTab] = useState<Tab>("audit");
  return (
    <div className="space-y-4">
      <div className="inline-flex rounded-lg border bg-muted/40 p-0.5">
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={cn(
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
              tab === t.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>
      {tab === "audit" ? <AdminAuditLog /> : <AdminCampaigns />}
    </div>
  );
}
```

- [ ] **Step 3: Create the audit log table**

Create `frontend/components/admin-audit-log.tsx`:

```tsx
"use client";

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";

interface AuditRow {
  id: number;
  occurred_at: string;
  tenant_id: number | null;
  tenant_name: string | null;
  actor_id: number | null;
  actor_email: string | null;
  action: string;
  resource_type: string | null;
  resource_id: number | null;
  details: unknown;
}

// Coarse color families by action prefix (finance/user/risk/campaign/…).
function actionVariant(action: string): "default" | "secondary" | "outline" {
  if (action.startsWith("finance.")) return "default";
  if (action.startsWith("risk.") || action.startsWith("campaign.")) return "secondary";
  return "outline";
}

export function AdminAuditLog() {
  const [rows, setRows] = useState<AuditRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const data = await api.get<{ rows: AuditRow[]; total: number }>("/admin/audit?limit=200");
      setRows(data.rows);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const columns: Column<AuditRow>[] = [
    { key: "occurred_at", header: "时间", cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.occurred_at}</span> },
    { key: "actor", header: "操作人", cell: (r) => <span className="text-sm">{r.actor_email ?? (r.actor_id ? `#${r.actor_id}` : "系统")}</span> },
    { key: "action", header: "动作", cell: (r) => <Badge variant={actionVariant(r.action)}>{r.action}</Badge> },
    { key: "target", header: "对象", cell: (r) => <span className="text-sm text-muted-foreground">{r.resource_type ? `${r.resource_type}${r.resource_id ? ` #${r.resource_id}` : ""}` : "—"}</span> },
    { key: "tenant", header: "租户", cell: (r) => <span className="text-sm text-muted-foreground">{r.tenant_name ?? (r.tenant_id ? `#${r.tenant_id}` : "—")}</span> },
    {
      key: "details",
      header: "详情",
      cell: (r) => {
        const s = JSON.stringify(r.details ?? {});
        return <span className="font-mono text-xs text-muted-foreground" title={s}>{s.length > 48 ? s.slice(0, 47) + "…" : s}</span>;
      },
    },
  ];

  return (
    <ProDataTable
      data={rows}
      error={error}
      columns={columns}
      getRowKey={(r) => r.id}
      search={{ placeholder: "搜索动作/操作人/对象…", accessor: (r) => `${r.action} ${r.actor_email ?? ""} ${r.resource_type ?? ""} ${r.tenant_name ?? ""}` }}
      emptyState="暂无审计记录"
    />
  );
}
```

- [ ] **Step 4: Build + lint**

Run (from `/var/klwa/frontend`): `npm run build && npx eslint components/admin-audit-tabs.tsx components/admin-audit-log.tsx app/admin/audit/page.tsx`
Expected: build clean; eslint shows at most the accepted `react-hooks/set-state-in-effect` on `admin-audit-log.tsx`'s load effect (same house pattern), no other error type, no unused vars.

- [ ] **Step 5: Manual smoke (if a live API is available; otherwise note it)**

`npm run dev`, open `/admin/audit`: default tab 审计日志 renders the table (empty state on a fresh DB); switching to 任务监控 shows the unchanged campaign view. After performing an admin action (e.g. a top-up), it appears as a row. If no live API/DB, state that in the report rather than faking it.

- [ ] **Step 6: Commit**

```bash
git add frontend/app/admin/audit/page.tsx frontend/components/admin-audit-tabs.tsx frontend/components/admin-audit-log.tsx
git commit -m "feat(admin): /admin/audit two tabs — audit log + task monitor

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review (completed by author)

**Spec coverage:**
- Component 1 (audit helper: `recordAudit`/`recordAuditTx`/`actorID`/pool seam) → Task 1. ✓
- Component 2 (instrument all write handlers) → Task 4 (12 best-effort) + Task 5 (campaign.stop atomic); `campaign.resume` left as-is per spec. ✓
- Component 3 (`GET /admin/audit` filters+paging+joins) → Task 2 (builder) + Task 3 (handler/route/DB test). ✓
- Component 4 (two-tab UI) → Task 6. ✓
- Risk page "持久化并审计" becomes true via Task 4's `risk.update` instrumentation; no copy change (spec). ✓
- Testing = hybrid: pure unit (Tasks 1–2), DB round-trip for recordAudit+read (Task 3) and campaign.stop (Task 5); per-handler write assertions intentionally omitted (documented). ✓
- Non-goals (export/alerting/refund actions/full-atomic/backfill) absent. ✓

**Placeholder scan:** none — every code step has full code. Two spots direct the implementer to confirm a real `req` struct field name against the compiler (pricing `Country`/`UnitPrice`, bind `ProxyID`); this is verification, not a placeholder — the code is written and will compile-fail loudly if a name differs.

**Type consistency:** `auditEvent`, `auditArgs`, `auditFilter`, `buildAuditWhere/ListSQL/CountSQL`, `systemPool()`, `sysPool` field, `auditRow`/`AuditRow` JSON shape, and `handleAdminListAudit` are consistent across Tasks 1→6. Test helpers `testPool`/`applyAllMigrations`/`seedTenantUser`/`seedRunningCampaign` are defined once (Task 3/5) and reused. Route string `/admin/audit` matches the frontend fetch.
