# Admin Console SP4 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the admin console a real send-campaigns page, ledger filtering + CSV export, role-grouped user management, and billing statistics (platform dashboard + per-tenant bill), all read-only over `wallet_ledger`.

**Architecture:** Backend adds read-only handlers + CSV endpoints in `internal/api` following the SP3a paginated-list pattern (`buildXxxWhere` pure functions on the `s.systemPool()` seam), plus audit writes on the two export endpoints via the existing `recordAudit` helper. Frontend adds two nav destinations and upgrades three existing components, reusing `ProDataTable` server mode, `StatCard`, `CampaignDetailSheet`, and a new `api.download` helper. No engine packages (billing/dispatch/store/sendgate/cluster) are modified.

**Tech Stack:** Go 1.x + gin + pgx v5 (backend), Next.js (App Router) + React + Tailwind + @tremor/react (frontend). Tests: testcontainers-go with `postgres:16` (backend DB tests), `npm run build` + `npm run lint` (frontend — no JS test framework, house rule).

## Global Constraints

- **Money source of truth:** all amounts come from `wallet_ledger` (BIGINT minor units — cents). `billing_charges` is out of scope. Per-row net = `delta_balance + delta_frozen`. `hold`/`reject` net to 0.
- **Category sums** (over net): 充值 = kind `topup`; 消耗 = −(kind `settle`); 退款 = kind `refund`; 调整 = kind `adjust`; 净额 = 充值 − 消耗 + 退款 + 调整.
- **Timezone:** daily bucketing uses `date_trunc('day', created_at AT TIME ZONE 'Asia/Shanghai')`.
- **Date params:** `from`/`to` are `YYYY-MM-DD`, interpreted as Asia/Shanghai day bounds (`to` is inclusive → query uses `< to+1day`). Default range = last 30 days. Reject reversed or >366-day ranges with HTTP 400.
- **Handler seam:** every new query uses `s.systemPool()` (not `s.deps.Mgr.SystemPool()` directly) so tests can inject `sysPool`.
- **Audit on exports only:** `finance.ledger_export` and `finance.bill_export` via `s.recordAudit(ctx, auditEvent{...})`. No other new endpoint writes.
- **Response envelope:** JSON handlers return via `ok(c, data)` / `fail(c, status, msg)`. CSV endpoints write raw `text/csv` (NOT the envelope).
- **Redlines:** do not touch `internal/{billing,dispatch,store,sendgate,cluster}`. SP4 is `internal/api` + `cmd/console/main.go` + `frontend/` only.
- **Frontend money format:** existing `usd(cents)` = `Intl.NumberFormat("en-US",{style:"currency",currency:"USD"}).format(cents/100)`.

---

## File Structure

**Backend (`internal/api/`):**
- `finance_query.go` (CREATE) — pure filter/SQL builders: `ledgerFilter`, `buildLedgerWhere`, `campaignFilter`, `buildCampaignWhere`, `billRange`, `parseBillRange`. One responsibility: turn query params into WHERE clauses + validated ranges. No DB, no gin.
- `finance_stats.go` (CREATE) — handlers `handleAdminFinanceStats`, `handleAdminFinanceBill`, `handleAdminFinanceBillExport`.
- `admin_api.go` (MODIFY) — extend `handleAdminLedger` (filter + pagination + `{rows,total,refunds}`) and `handleAdminListCampaigns` (filter + `{rows,total,stats}`); add `handleAdminLedgerExport`.
- `csv.go` (CREATE) — tiny `writeCSV(c, filename, header []string, rows [][]string)` helper (uses stdlib `encoding/csv`).
- `router.go` (MODIFY) — register 4 new routes.
- `finance_query_test.go` (CREATE) — pure-function unit tests.
- `finance_db_test.go` (CREATE) — testcontainers DB tests for stats/bill/ledger/campaign/export.

**Frontend:**
- `lib/api.ts` (MODIFY) — add `api.download(path, filename)`.
- `components/admin/nav.ts` (MODIFY) — add 发送任务 + 账单统计 nav items.
- `components/admin-campaigns.tsx` (MODIFY) — server-mode table + state tabs + stat cards.
- `components/admin/pro-data-table.tsx` (MODIFY) — add optional `onRowClick`.
- `components/admin-ledger.tsx` (MODIFY) — kind tabs + server pagination + date range + export.
- `components/admin-users-table.tsx` (MODIFY) — role tabs + sales "名下客户" column.
- `components/admin-billing.tsx` (CREATE) — billing dashboard + per-tenant bill drill-down.
- `components/billing-trend-chart.tsx` (CREATE) — tremor AreaChart wrapper for daily series.
- `app/admin/campaigns/page.tsx` (CREATE) — route shell.
- `app/admin/billing/page.tsx` (CREATE) — route shell.
- `app/admin/audit/page.tsx` (MODIFY) — drop tabs, render `AdminAuditLog` directly.
- `components/admin-audit-tabs.tsx` (DELETE) — folded away.

---

## Task 1: CSV helper + `api.download`

Foundation for all export features. No feature behavior yet; just the two plumbing pieces, each with a smoke test.

**Files:**
- Create: `internal/api/csv.go`
- Create: `internal/api/csv_test.go`
- Modify: `frontend/lib/api.ts`

**Interfaces:**
- Produces (Go): `func writeCSV(c *gin.Context, filename string, header []string, rows [][]string)` — sets `Content-Type: text/csv; charset=utf-8` and `Content-Disposition: attachment; filename="<filename>"`, writes header + rows via `encoding/csv`.
- Produces (TS): `api.download(path: string, filename: string): Promise<void>` — GET with Bearer token, reads blob, triggers browser download. Throws `ApiError` on non-OK.

- [ ] **Step 1: Write the failing Go test**

Create `internal/api/csv_test.go`:

```go
package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWriteCSV(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	writeCSV(c, "ledger.csv", []string{"id", "kind"}, [][]string{{"1", "topup"}, {"2", "settle"}})

	if ct := w.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); cd != `attachment; filename="ledger.csv"` {
		t.Errorf("disposition = %q", cd)
	}
	want := "id,kind\n1,topup\n2,settle\n"
	if got := w.Body.String(); got != want {
		t.Errorf("body = %q want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/api/ -run TestWriteCSV`
Expected: FAIL — `undefined: writeCSV`.

- [ ] **Step 3: Implement `writeCSV`**

Create `internal/api/csv.go`:

```go
package api

import (
	"encoding/csv"
	"fmt"

	"github.com/gin-gonic/gin"
)

// writeCSV streams a CSV attachment. It bypasses the JSON envelope: callers use
// this only for file-download endpoints. encoding/csv emits \r\n per RFC 4180
// but we normalize to \n via a manual writer for deterministic output.
func writeCSV(c *gin.Context, filename string, header []string, rows [][]string) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w := csv.NewWriter(c.Writer)
	w.UseCRLF = false
	_ = w.Write(header)
	_ = w.WriteAll(rows)
	w.Flush()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/api/ -run TestWriteCSV`
Expected: PASS.

- [ ] **Step 5: Add `api.download` to frontend**

In `frontend/lib/api.ts`, after the `api` object (after line 137), add:

```ts
// ---------------------------------------------------------------------------
// File download (CSV export). Bearer-authenticated GET → blob → browser save.
// Kept separate from `request` because it does not unwrap the JSON envelope.
// ---------------------------------------------------------------------------

export async function download(path: string, filename: string): Promise<void> {
  const url = path.startsWith("http")
    ? path
    : `${API_BASE_URL}${path.startsWith("/") ? path : `/${path}`}`;
  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) headers["Authorization"] = `Bearer ${token}`;

  let res: Response;
  try {
    res = await fetch(url, { method: "GET", headers });
  } catch {
    throw new ApiError(0, "network error: could not reach API");
  }
  if (res.status === 401) {
    clearToken();
    if (typeof window !== "undefined" && window.location.pathname !== LOGIN_PATH) {
      window.location.href = LOGIN_PATH;
    }
    throw new ApiError(401, "unauthorized");
  }
  if (!res.ok) throw new ApiError(res.status, res.statusText);

  const blob = await res.blob();
  const objectUrl = window.URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = objectUrl;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.URL.revokeObjectURL(objectUrl);
}
```

Then add `download` to the exported `api` object so callers use `api.download(...)`. Change the `export const api = {` block to include:

```ts
export const api = {
  get: <T>(path: string, options?: RequestOptions) =>
    request<T>(path, { ...options, method: "GET" }),
  post: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: "POST", body }),
  put: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: "PUT", body }),
  patch: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>(path, { ...options, method: "PATCH", body }),
  delete: <T>(path: string, options?: RequestOptions) =>
    request<T>(path, { ...options, method: "DELETE" }),
  download,
};
```

(Define `download` as a `function` declaration above the object, as shown, so it is hoisted.)

- [ ] **Step 6: Verify frontend builds**

Run: `cd /var/klwa/frontend && npm run lint && npm run build`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
cd /var/klwa && git add internal/api/csv.go internal/api/csv_test.go frontend/lib/api.ts
git commit -m "feat(admin): CSV writer + api.download helper (SP4 plumbing)"
```

---

## Task 2: Ledger filter builder (pure function)

**Files:**
- Create: `internal/api/finance_query.go`
- Create: `internal/api/finance_query_test.go`

**Interfaces:**
- Produces: `type ledgerFilter struct { Kind string; TenantID int64; From, To time.Time; Limit, Offset int }`
- Produces: `func buildLedgerWhere(f ledgerFilter) (string, []any)` — returns `" WHERE ..."` (or `""`) + positional args for `wallet_ledger` aliased `l`. Conditions: `l.kind::text = $n` (when Kind != ""), `l.tenant_id = $n` (when != 0), `l.created_at >= $n` (when From not zero), `l.created_at < $n` (when To not zero — caller passes the exclusive upper bound).

- [ ] **Step 1: Write the failing test**

Create `internal/api/finance_query_test.go`:

```go
package api

import (
	"testing"
	"time"
)

func TestBuildLedgerWhere(t *testing.T) {
	// empty filter → no WHERE
	if w, args := buildLedgerWhere(ledgerFilter{}); w != "" || len(args) != 0 {
		t.Errorf("empty: where=%q args=%v", w, args)
	}
	// kind + tenant
	w, args := buildLedgerWhere(ledgerFilter{Kind: "topup", TenantID: 7})
	if w != " WHERE l.kind::text = $1 AND l.tenant_id = $2" {
		t.Errorf("where = %q", w)
	}
	if len(args) != 2 || args[0] != "topup" || args[1] != int64(7) {
		t.Errorf("args = %v", args)
	}
	// date range uses >= from and < to
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	w, args = buildLedgerWhere(ledgerFilter{From: from, To: to})
	if w != " WHERE l.created_at >= $1 AND l.created_at < $2" {
		t.Errorf("range where = %q", w)
	}
	if len(args) != 2 {
		t.Errorf("range args = %v", args)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/api/ -run TestBuildLedgerWhere`
Expected: FAIL — `undefined: buildLedgerWhere`.

- [ ] **Step 3: Implement**

Create `internal/api/finance_query.go`:

```go
package api

import (
	"fmt"
	"strings"
	"time"
)

// ledgerFilter is the parsed query for the ledger list/export endpoints.
type ledgerFilter struct {
	Kind     string
	TenantID int64
	From     time.Time // zero = unset (inclusive lower bound)
	To       time.Time // zero = unset (EXCLUSIVE upper bound — caller adds +1 day)
	Limit    int
	Offset   int
}

// buildLedgerWhere returns the " WHERE ..." clause (or "") and positional args
// for wallet_ledger aliased "l".
func buildLedgerWhere(f ledgerFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(tmpl string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(tmpl, len(args)))
	}
	if f.Kind != "" {
		add("l.kind::text = $%d", f.Kind)
	}
	if f.TenantID != 0 {
		add("l.tenant_id = $%d", f.TenantID)
	}
	if !f.From.IsZero() {
		add("l.created_at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("l.created_at < $%d", f.To)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/api/ -run TestBuildLedgerWhere`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa && git add internal/api/finance_query.go internal/api/finance_query_test.go
git commit -m "feat(admin): buildLedgerWhere pure filter (SP4)"
```

---

## Task 3: Bill date-range parser (pure function)

**Files:**
- Modify: `internal/api/finance_query.go`
- Modify: `internal/api/finance_query_test.go`

**Interfaces:**
- Produces: `type billRange struct { From, To time.Time }` where `From` = start of `from` day in Asia/Shanghai, `To` = start of the day AFTER `to` (exclusive upper bound).
- Produces: `func parseBillRange(fromStr, toStr string) (billRange, error)` — parses `YYYY-MM-DD`; empty strings default to last 30 days (`To` = tomorrow 00:00 CN, `From` = 30 days before `to`); errors on unparseable dates, reversed range (`from > to`), or span > 366 days.

- [ ] **Step 1: Write the failing test**

Append to `internal/api/finance_query_test.go`:

```go
func TestParseBillRange(t *testing.T) {
	cn, _ := time.LoadLocation("Asia/Shanghai")

	// explicit range: To is exclusive (start of day after `to`)
	r, err := parseBillRange("2026-07-01", "2026-07-07")
	if err != nil {
		t.Fatalf("valid range err: %v", err)
	}
	wantFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, cn)
	wantTo := time.Date(2026, 7, 8, 0, 0, 0, 0, cn)
	if !r.From.Equal(wantFrom) || !r.To.Equal(wantTo) {
		t.Errorf("range = %v..%v want %v..%v", r.From, r.To, wantFrom, wantTo)
	}
	// reversed → error
	if _, err := parseBillRange("2026-07-08", "2026-07-01"); err == nil {
		t.Error("reversed range should error")
	}
	// bad format → error
	if _, err := parseBillRange("07/01/2026", ""); err == nil {
		t.Error("bad format should error")
	}
	// oversized → error
	if _, err := parseBillRange("2020-01-01", "2026-01-01"); err == nil {
		t.Error("oversized range should error")
	}
	// empty defaults to a 30-day window (no error)
	if _, err := parseBillRange("", ""); err != nil {
		t.Errorf("default range err: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/api/ -run TestParseBillRange`
Expected: FAIL — `undefined: parseBillRange`.

- [ ] **Step 3: Implement**

Append to `internal/api/finance_query.go`:

```go
// billRange is a validated [From, To) window in Asia/Shanghai. To is exclusive.
type billRange struct {
	From time.Time
	To   time.Time
}

var cnLoc = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600) // fallback: fixed +08:00
	}
	return loc
}()

// parseBillRange parses YYYY-MM-DD `from`/`to` as Asia/Shanghai day bounds.
// `to` is inclusive of that whole day, so the returned To is start-of-next-day.
// Empty strings default to the last 30 days. Rejects reversed / >366d ranges.
func parseBillRange(fromStr, toStr string) (billRange, error) {
	const layout = "2006-01-02"
	// Anchor "now" to CN start-of-tomorrow so the default window is stable
	// within a day; we derive it from the parsed `to` when supplied.
	var to time.Time
	if toStr == "" {
		now := time.Now().In(cnLoc)
		to = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, cnLoc).AddDate(0, 0, 1)
	} else {
		d, err := time.ParseInLocation(layout, toStr, cnLoc)
		if err != nil {
			return billRange{}, fmt.Errorf("bad `to` date: %w", err)
		}
		to = d.AddDate(0, 0, 1) // inclusive → exclusive next-day bound
	}

	var from time.Time
	if fromStr == "" {
		from = to.AddDate(0, 0, -30)
	} else {
		d, err := time.ParseInLocation(layout, fromStr, cnLoc)
		if err != nil {
			return billRange{}, fmt.Errorf("bad `from` date: %w", err)
		}
		from = d
	}

	if !from.Before(to) {
		return billRange{}, fmt.Errorf("`from` must be before `to`")
	}
	if to.Sub(from) > 366*24*time.Hour {
		return billRange{}, fmt.Errorf("range too large (max 366 days)")
	}
	return billRange{From: from, To: to}, nil
}
```

Note: `time` is already imported. `fmt` and `strings` are already imported from Task 2.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/api/ -run TestParseBillRange`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa && git add internal/api/finance_query.go internal/api/finance_query_test.go
git commit -m "feat(admin): parseBillRange CN-timezone window parser (SP4)"
```

---

## Task 4: Ledger list pagination + filtering

Upgrade the existing ledger endpoint to the SP3a envelope shape and add filters.

**Files:**
- Modify: `internal/api/admin_api.go` (`handleAdminLedger`, ~lines 444-487)
- Create: `internal/api/finance_db_test.go`

**Interfaces:**
- Consumes: `buildLedgerWhere`, `clampPage` (existing, in `resources_query.go`).
- Produces: `GET /admin/finance/ledger?kind&tenant_id&from&to&limit&offset` → `ok(c, {rows, total, refunds})`. `rows` includes `frozen_after` and joined `tenant_name`. `refunds` unchanged.
- Produces: helper `parseLedgerFilter(c *gin.Context) ledgerFilter` in `admin_api.go` (near existing `parseDeviceFilter`).

- [ ] **Step 1: Write the failing DB test**

Create `internal/api/finance_db_test.go`:

```go
package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/audit"
)

func newFinanceServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	pool := testPool(t)
	return &Server{sysPool: pool, deps: Deps{Audit: audit.NewAuditWriter(pool)}}, context.Background()
}

// seedLedger inserts one wallet_ledger row and returns nothing; idem_key is
// derived from the caller-supplied unique suffix.
func seedLedger(t *testing.T, ctx context.Context, s *Server, tenantID int64, kind string, dbal, dfroz, balAfter, frozAfter int64, at string, idem string) {
	t.Helper()
	_, err := s.systemPool().Exec(ctx, `
INSERT INTO wallet_ledger (tenant_id, kind, delta_balance, delta_frozen, balance_after, frozen_after, idem_key, created_at)
VALUES ($1, $2::ledger_kind_t, $3, $4, $5, $6, $7, $8::timestamptz)`,
		tenantID, kind, dbal, dfroz, balAfter, frozAfter, idem, at)
	if err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
}

func TestHandleAdminLedgerPaginated(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "led@acme.test")
	seedLedger(t, ctx, s, tid, "topup", 1000, 0, 1000, 0, "2026-07-01T10:00:00+08:00", "k1")
	seedLedger(t, ctx, s, tid, "settle", -200, 0, 800, 0, "2026-07-02T10:00:00+08:00", "k2")
	seedLedger(t, ctx, s, tid, "topup", 500, 0, 1300, 0, "2026-07-03T10:00:00+08:00", "k3")

	// filter kind=topup → 2 rows
	w := doGET(t, s, s.handleAdminLedger, "/admin/finance/ledger?kind=topup")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"total":2`) {
		t.Errorf("want total 2, body=%s", body)
	}
}
```

Add these shared test helpers to `finance_db_test.go` (used by later tasks too):

```go
func doGET(t *testing.T, s *Server, h gin.HandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	h(c)
	return w
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
```

Add imports `net/http/httptest` and `strings` to the file's import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/api/ -run TestHandleAdminLedgerPaginated`
Expected: FAIL — response is old `{ledger,refunds}` shape, no `total`.

- [ ] **Step 3: Add `parseLedgerFilter` + rewrite `handleAdminLedger`**

In `internal/api/admin_api.go`, add near `parseDeviceFilter`:

```go
func parseLedgerFilter(c *gin.Context) ledgerFilter {
	f := ledgerFilter{Kind: c.Query("kind")}
	if n, err := strconv.ParseInt(c.Query("tenant_id"), 10, 64); err == nil && n > 0 {
		f.TenantID = n
	}
	if v := c.Query("from"); v != "" {
		if d, err := time.ParseInLocation("2006-01-02", v, cnLoc); err == nil {
			f.From = d
		}
	}
	if v := c.Query("to"); v != "" {
		if d, err := time.ParseInLocation("2006-01-02", v, cnLoc); err == nil {
			f.To = d.AddDate(0, 0, 1) // inclusive day → exclusive bound
		}
	}
	if n, err := strconv.Atoi(c.Query("limit")); err == nil {
		f.Limit = n
	}
	if n, err := strconv.Atoi(c.Query("offset")); err == nil && n > 0 {
		f.Offset = n
	}
	return f
}
```

Ensure `time` is imported in `admin_api.go` (add to import block if absent).

Replace the ledger-query portion of `handleAdminLedger` (the first `pool.Query` block that selects from `wallet_ledger`) with a filtered + paginated version, keeping the refunds block as-is and changing the final `ok(...)`:

```go
func (s *Server) handleAdminLedger(c *gin.Context) {
	ctx := c.Request.Context()
	pool := s.systemPool()
	f := parseLedgerFilter(c)
	where, args := buildLedgerWhere(f)

	listArgs := append(append([]any{}, args...), clampPage(f.Limit, 50, 500), f.Offset)
	list := `
SELECT l.id, l.tenant_id, t.name, l.kind::text, l.delta_balance, l.delta_frozen,
       l.balance_after, l.frozen_after, l.created_at::text
  FROM wallet_ledger l
  LEFT JOIN tenants t ON t.id = l.tenant_id` + where +
		fmt.Sprintf(" ORDER BY l.id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	lrows, err := pool.Query(ctx, list, listArgs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "ledger query")
		return
	}
	defer lrows.Close()
	ledger := make([]ledgerRow, 0)
	for lrows.Next() {
		var l ledgerRow
		if err := lrows.Scan(&l.ID, &l.TenantID, &l.TenantName, &l.Kind, &l.DeltaBalance,
			&l.DeltaFrozen, &l.BalanceAfter, &l.FrozenAfter, &l.CreatedAt); err != nil {
			fail(c, http.StatusInternalServerError, "scan ledger")
			return
		}
		ledger = append(ledger, l)
	}
	if err := lrows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate ledger")
		return
	}

	var total int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger l`+where, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count ledger")
		return
	}

	rrows, err := pool.Query(ctx, `
SELECT id, tenant_id, amount, reason, state::text
  FROM refund_requests ORDER BY id DESC LIMIT 100`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "refunds query")
		return
	}
	defer rrows.Close()
	refunds := make([]refundRow, 0)
	for rrows.Next() {
		var r refundRow
		if err := rrows.Scan(&r.ID, &r.TenantID, &r.Amount, &r.Reason, &r.State); err != nil {
			fail(c, http.StatusInternalServerError, "scan refund")
			return
		}
		refunds = append(refunds, r)
	}

	ok(c, gin.H{"rows": ledger, "total": total, "refunds": refunds})
}
```

Extend the `ledgerRow` struct (find its definition in `admin_api.go`) to add the new fields. It must include:

```go
type ledgerRow struct {
	ID           int64   `json:"id"`
	TenantID     int64   `json:"tenant_id"`
	TenantName   *string `json:"tenant_name"`
	Kind         string  `json:"kind"`
	DeltaBalance int64   `json:"delta_balance"`
	DeltaFrozen  int64   `json:"delta_frozen"`
	BalanceAfter int64   `json:"balance_after"`
	FrozenAfter  int64   `json:"frozen_after"`
	CreatedAt    string  `json:"created_at"`
}
```

(If the existing struct already has some of these with different names, reconcile to these exact JSON tags — the frontend depends on them.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/api/ -run TestHandleAdminLedgerPaginated`
Expected: PASS.

- [ ] **Step 5: Run full api package build to catch struct drift**

Run: `cd /var/klwa && go build ./... && go test ./internal/api/ -run 'TestBuildLedgerWhere|TestWriteCSV'`
Expected: builds; existing tests pass.

- [ ] **Step 6: Commit**

```bash
cd /var/klwa && git add internal/api/admin_api.go internal/api/finance_db_test.go
git commit -m "feat(admin): ledger list pagination + kind/tenant/date filters (SP4)"
```

---

## Task 5: Ledger CSV export endpoint

**Files:**
- Modify: `internal/api/admin_api.go` (add `handleAdminLedgerExport`)
- Modify: `internal/api/router.go` (register route)
- Modify: `internal/api/finance_db_test.go` (add test)

**Interfaces:**
- Consumes: `parseLedgerFilter`, `buildLedgerWhere`, `writeCSV`, `s.recordAudit`.
- Produces: `GET /admin/finance/ledger/export?<same filters>` → CSV attachment `ledger.csv`, full filtered set (no LIMIT), columns: `id,created_at,tenant_id,tenant_name,kind,delta_balance,delta_frozen,balance_after,frozen_after`. Writes audit `finance.ledger_export`.

- [ ] **Step 1: Write the failing test**

Append to `internal/api/finance_db_test.go`:

```go
func TestHandleAdminLedgerExport(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "exp@acme.test")
	seedLedger(t, ctx, s, tid, "topup", 1000, 0, 1000, 0, "2026-07-01T10:00:00+08:00", "e1")
	seedLedger(t, ctx, s, tid, "settle", -200, 0, 800, 0, "2026-07-02T10:00:00+08:00", "e2")

	w := doGET(t, s, s.handleAdminLedgerExport, "/admin/finance/ledger/export")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("content-type %q", ct)
	}
	// header line + 2 data lines + trailing newline = 3 \n
	body := w.Body.String()
	if lines := strings.Count(body, "\n"); lines != 3 {
		t.Errorf("want 3 newlines, got %d body=%q", lines, body)
	}
	if !strings.HasPrefix(body, "id,created_at,tenant_id,tenant_name,kind,") {
		t.Errorf("bad header: %q", body)
	}
	// audit row written
	var n int
	s.systemPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='finance.ledger_export'`).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 export audit, got %d", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/api/ -run TestHandleAdminLedgerExport`
Expected: FAIL — `undefined: (*Server).handleAdminLedgerExport`.

- [ ] **Step 3: Implement handler**

In `internal/api/admin_api.go`:

```go
// handleAdminLedgerExport: GET /admin/finance/ledger/export — full filtered
// ledger as CSV (no pagination). Audited (sensitive financial export).
func (s *Server) handleAdminLedgerExport(c *gin.Context) {
	ctx := c.Request.Context()
	f := parseLedgerFilter(c)
	where, args := buildLedgerWhere(f)
	q := `
SELECT l.id, l.created_at::text, l.tenant_id, COALESCE(t.name,''), l.kind::text,
       l.delta_balance, l.delta_frozen, l.balance_after, l.frozen_after
  FROM wallet_ledger l
  LEFT JOIN tenants t ON t.id = l.tenant_id` + where + " ORDER BY l.id DESC"
	rows, err := s.systemPool().Query(ctx, q, args...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "ledger export query")
		return
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var id, tenantID, dbal, dfroz, bal, froz int64
		var created, name, kind string
		if err := rows.Scan(&id, &created, &tenantID, &name, &kind, &dbal, &dfroz, &bal, &froz); err != nil {
			fail(c, http.StatusInternalServerError, "scan export")
			return
		}
		out = append(out, []string{
			strconv.FormatInt(id, 10), created, strconv.FormatInt(tenantID, 10), name, kind,
			strconv.FormatInt(dbal, 10), strconv.FormatInt(dfroz, 10),
			strconv.FormatInt(bal, 10), strconv.FormatInt(froz, 10),
		})
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate export")
		return
	}
	s.recordAudit(ctx, auditEvent{
		ActorID: actorID(c), Action: "finance.ledger_export", ResourceType: "ledger",
		Details: gin.H{"kind": f.Kind, "tenant_id": f.TenantID, "rows": len(out)},
	})
	writeCSV(c, "ledger.csv",
		[]string{"id", "created_at", "tenant_id", "tenant_name", "kind", "delta_balance", "delta_frozen", "balance_after", "frozen_after"},
		out)
}
```

- [ ] **Step 4: Register route**

In `internal/api/router.go`, after line 142 (`admin.GET("/finance/ledger", ...)`), add:

```go
		admin.GET("/finance/ledger/export", s.handleAdminLedgerExport)
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/api/ -run TestHandleAdminLedgerExport`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /var/klwa && git add internal/api/admin_api.go internal/api/router.go internal/api/finance_db_test.go
git commit -m "feat(admin): ledger CSV export endpoint + audit (SP4)"
```

---

## Task 6: Finance stats endpoint

**Files:**
- Create: `internal/api/finance_stats.go`
- Modify: `internal/api/router.go`
- Modify: `internal/api/finance_db_test.go`

**Interfaces:**
- Consumes: `parseBillRange`, `s.systemPool()`, `cnLoc`.
- Produces: `GET /admin/finance/stats?from&to` → `ok(c, financeStats{...})` with shape:

```go
type financeStats struct {
	Summary struct {
		Topup  int64 `json:"topup"`
		Settle int64 `json:"settle"`
		Refund int64 `json:"refund"`
		Adjust int64 `json:"adjust"`
		Net    int64 `json:"net"`
	} `json:"summary"`
	Daily      []dailyPoint  `json:"daily"`
	TopTenants []tenantSpend `json:"top_tenants"`
}
type dailyPoint struct {
	Day    string `json:"day"` // YYYY-MM-DD (CN)
	Topup  int64  `json:"topup"`
	Settle int64  `json:"settle"`
	Refund int64  `json:"refund"`
}
type tenantSpend struct {
	TenantID int64  `json:"tenant_id"`
	Name     string `json:"name"`
	Settle   int64  `json:"settle"`
	Topup    int64  `json:"topup"`
}
```

- [ ] **Step 1: Write the failing test**

Append to `internal/api/finance_db_test.go`:

```go
func TestHandleAdminFinanceStats(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "stat@acme.test")
	// Two topups (1500 total), one settle (300 spend).
	seedLedger(t, ctx, s, tid, "topup", 1000, 0, 1000, 0, "2026-07-01T10:00:00+08:00", "s1")
	seedLedger(t, ctx, s, tid, "settle", -300, 0, 700, 0, "2026-07-01T11:00:00+08:00", "s2")
	seedLedger(t, ctx, s, tid, "topup", 500, 0, 1200, 0, "2026-07-02T09:00:00+08:00", "s3")
	// hold nets to zero — must NOT affect totals.
	seedLedger(t, ctx, s, tid, "hold", -100, 100, 1200, 100, "2026-07-02T09:30:00+08:00", "s4")

	w := doGET(t, s, s.handleAdminFinanceStats, "/admin/finance/stats?from=2026-07-01&to=2026-07-02")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"topup":1500`) {
		t.Errorf("want topup 1500: %s", body)
	}
	if !contains(body, `"settle":300`) { // stored as -300, reported positive
		t.Errorf("want settle 300: %s", body)
	}
}

func TestFinanceStatsTimezoneBucketing(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "tz@acme.test")
	// 2026-07-01T23:30:00Z == 2026-07-02T07:30 CN → must bucket to 07-02.
	seedLedger(t, ctx, s, tid, "topup", 100, 0, 100, 0, "2026-07-01T23:30:00Z", "tz1")

	w := doGET(t, s, s.handleAdminFinanceStats, "/admin/finance/stats?from=2026-07-02&to=2026-07-02")
	body := w.Body.String()
	if !contains(body, `"day":"2026-07-02"`) {
		t.Errorf("row should bucket to CN 07-02: %s", body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/api/ -run TestHandleAdminFinanceStats`
Expected: FAIL — `undefined: (*Server).handleAdminFinanceStats`.

- [ ] **Step 3: Implement**

Create `internal/api/finance_stats.go`:

```go
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type dailyPoint struct {
	Day    string `json:"day"`
	Topup  int64  `json:"topup"`
	Settle int64  `json:"settle"`
	Refund int64  `json:"refund"`
}
type tenantSpend struct {
	TenantID int64  `json:"tenant_id"`
	Name     string `json:"name"`
	Settle   int64  `json:"settle"`
	Topup    int64  `json:"topup"`
}
type financeStats struct {
	Summary struct {
		Topup  int64 `json:"topup"`
		Settle int64 `json:"settle"`
		Refund int64 `json:"refund"`
		Adjust int64 `json:"adjust"`
		Net    int64 `json:"net"`
	} `json:"summary"`
	Daily      []dailyPoint  `json:"daily"`
	TopTenants []tenantSpend `json:"top_tenants"`
}

// net movement of a ledger row = delta_balance + delta_frozen.
// settle is stored negative; we report 消耗 as a positive magnitude.
const netExpr = "(l.delta_balance + l.delta_frozen)"

// handleAdminFinanceStats: GET /admin/finance/stats?from&to — platform totals,
// daily series, and tenant spend ranking, all from wallet_ledger.
func (s *Server) handleAdminFinanceStats(c *gin.Context) {
	ctx := c.Request.Context()
	r, err := parseBillRange(c.Query("from"), c.Query("to"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	pool := s.systemPool()
	var out financeStats
	out.Daily = []dailyPoint{}
	out.TopTenants = []tenantSpend{}

	// Summary (single scan of the window).
	err = pool.QueryRow(ctx, `
SELECT
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='topup'), 0),
  COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0),
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='refund'), 0),
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='adjust'), 0)
  FROM wallet_ledger l
 WHERE l.created_at >= $1 AND l.created_at < $2`, r.From, r.To).
		Scan(&out.Summary.Topup, &out.Summary.Settle, &out.Summary.Refund, &out.Summary.Adjust)
	if err != nil {
		fail(c, http.StatusInternalServerError, "stats summary")
		return
	}
	out.Summary.Net = out.Summary.Topup - out.Summary.Settle + out.Summary.Refund + out.Summary.Adjust

	// Daily series (CN days).
	drows, err := pool.Query(ctx, `
SELECT to_char(date_trunc('day', l.created_at AT TIME ZONE 'Asia/Shanghai'), 'YYYY-MM-DD') AS day,
       COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='topup'), 0),
       COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0),
       COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='refund'), 0)
  FROM wallet_ledger l
 WHERE l.created_at >= $1 AND l.created_at < $2
 GROUP BY day ORDER BY day`, r.From, r.To)
	if err != nil {
		fail(c, http.StatusInternalServerError, "stats daily")
		return
	}
	defer drows.Close()
	for drows.Next() {
		var d dailyPoint
		if err := drows.Scan(&d.Day, &d.Topup, &d.Settle, &d.Refund); err != nil {
			fail(c, http.StatusInternalServerError, "scan daily")
			return
		}
		out.Daily = append(out.Daily, d)
	}
	if err := drows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate daily")
		return
	}

	// Tenant spend ranking (top 20 by settle).
	trows, err := pool.Query(ctx, `
SELECT l.tenant_id, COALESCE(t.name, ''),
       COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0) AS settle,
       COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='topup'), 0) AS topup
  FROM wallet_ledger l
  LEFT JOIN tenants t ON t.id = l.tenant_id
 WHERE l.created_at >= $1 AND l.created_at < $2
 GROUP BY l.tenant_id, t.name
 ORDER BY settle DESC LIMIT 20`, r.From, r.To)
	if err != nil {
		fail(c, http.StatusInternalServerError, "stats tenants")
		return
	}
	defer trows.Close()
	for trows.Next() {
		var ts tenantSpend
		if err := trows.Scan(&ts.TenantID, &ts.Name, &ts.Settle, &ts.Topup); err != nil {
			fail(c, http.StatusInternalServerError, "scan tenant spend")
			return
		}
		out.TopTenants = append(out.TopTenants, ts)
	}
	if err := trows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate tenant spend")
		return
	}

	ok(c, out)
}
```

- [ ] **Step 4: Register route**

In `internal/api/router.go`, after the ledger export route from Task 5, add:

```go
		admin.GET("/finance/stats", s.handleAdminFinanceStats)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /var/klwa && go test ./internal/api/ -run 'TestHandleAdminFinanceStats|TestFinanceStatsTimezoneBucketing'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /var/klwa && git add internal/api/finance_stats.go internal/api/router.go internal/api/finance_db_test.go
git commit -m "feat(admin): platform finance stats endpoint (summary/daily/top-tenants) (SP4)"
```

---

## Task 7: Per-tenant bill endpoint + export

**Files:**
- Modify: `internal/api/finance_stats.go`
- Modify: `internal/api/router.go`
- Modify: `internal/api/finance_db_test.go`

**Interfaces:**
- Consumes: `parseBillRange`, `netExpr`, `writeCSV`, `s.recordAudit`, `dailyPoint`.
- Produces: `GET /admin/finance/bill?tenant_id&from&to` → `ok(c, tenantBill{...})`:

```go
type tenantBill struct {
	TenantID int64        `json:"tenant_id"`
	Name     string       `json:"name"`
	Opening  int64        `json:"opening"`  // balance+frozen just before window
	Closing  int64        `json:"closing"`  // balance+frozen at window end
	Summary  struct {
		Topup  int64 `json:"topup"`
		Settle int64 `json:"settle"`
		Refund int64 `json:"refund"`
		Adjust int64 `json:"adjust"`
	} `json:"summary"`
	Daily []dailyPoint `json:"daily"`
}
```

- Produces: `GET /admin/finance/bill/export?tenant_id&from&to` → CSV `bill-<tenant_id>.csv`, that tenant's ledger rows in the window (same 9 columns as ledger export). Writes audit `finance.bill_export`.
- Invariant tested: `opening + (topup - settle + refund + adjust) == closing`.

- [ ] **Step 1: Write the failing test**

Append to `internal/api/finance_db_test.go`:

```go
func TestHandleAdminFinanceBill(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "bill@acme.test")
	// Before window: a topup establishing opening balance 1000.
	seedLedger(t, ctx, s, tid, "topup", 1000, 0, 1000, 0, "2026-06-15T10:00:00+08:00", "b0")
	// In window: +500 topup, -200 settle → closing 1300.
	seedLedger(t, ctx, s, tid, "topup", 500, 0, 1500, 0, "2026-07-02T10:00:00+08:00", "b1")
	seedLedger(t, ctx, s, tid, "settle", -200, 0, 1300, 0, "2026-07-03T10:00:00+08:00", "b2")

	w := doGET(t, s, s.handleAdminFinanceBill,
		"/admin/finance/bill?tenant_id="+itoa(tid)+"&from=2026-07-01&to=2026-07-31")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"opening":1000`) {
		t.Errorf("want opening 1000: %s", body)
	}
	if !contains(body, `"closing":1300`) {
		t.Errorf("want closing 1300: %s", body)
	}
	// reconciliation: opening + topup - settle == closing (1000 + 500 - 200)
	if !contains(body, `"topup":500`) || !contains(body, `"settle":200`) {
		t.Errorf("summary wrong: %s", body)
	}
}

func TestHandleAdminFinanceBillMissingTenant(t *testing.T) {
	s, _ := newFinanceServer(t)
	w := doGET(t, s, s.handleAdminFinanceBill, "/admin/finance/bill?from=2026-07-01&to=2026-07-31")
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing tenant_id want 400, got %d", w.Code)
	}
}

func TestHandleAdminFinanceBillExport(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "billexp@acme.test")
	seedLedger(t, ctx, s, tid, "topup", 500, 0, 500, 0, "2026-07-02T10:00:00+08:00", "be1")

	w := doGET(t, s, s.handleAdminFinanceBillExport,
		"/admin/finance/bill/export?tenant_id="+itoa(tid)+"&from=2026-07-01&to=2026-07-31")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("content-type %q", ct)
	}
	var n int
	s.systemPool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='finance.bill_export'`).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 bill_export audit, got %d", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/api/ -run TestHandleAdminFinanceBill`
Expected: FAIL — `undefined: (*Server).handleAdminFinanceBill`.

- [ ] **Step 3: Implement both handlers**

Append to `internal/api/finance_stats.go`:

```go
type tenantBill struct {
	TenantID int64  `json:"tenant_id"`
	Name     string `json:"name"`
	Opening  int64  `json:"opening"`
	Closing  int64  `json:"closing"`
	Summary  struct {
		Topup  int64 `json:"topup"`
		Settle int64 `json:"settle"`
		Refund int64 `json:"refund"`
		Adjust int64 `json:"adjust"`
	} `json:"summary"`
	Daily []dailyPoint `json:"daily"`
}

// balanceAsOf returns balance_after+frozen_after of the last ledger row for the
// tenant strictly before `before` (0 if none).
func (s *Server) balanceAsOf(ctx context.Context, tenantID int64, before time.Time) (int64, error) {
	var v int64
	err := s.systemPool().QueryRow(ctx, `
SELECT COALESCE(balance_after + frozen_after, 0)
  FROM wallet_ledger
 WHERE tenant_id = $1 AND created_at < $2
 ORDER BY id DESC LIMIT 1`, tenantID, before).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

func (s *Server) handleAdminFinanceBill(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, err := strconv.ParseInt(c.Query("tenant_id"), 10, 64)
	if err != nil || tenantID <= 0 {
		fail(c, http.StatusBadRequest, "tenant_id required")
		return
	}
	r, err := parseBillRange(c.Query("from"), c.Query("to"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	pool := s.systemPool()
	var bill tenantBill
	bill.TenantID = tenantID
	bill.Daily = []dailyPoint{}

	_ = pool.QueryRow(ctx, `SELECT COALESCE(name,'') FROM tenants WHERE id=$1`, tenantID).Scan(&bill.Name)

	bill.Opening, err = s.balanceAsOf(ctx, tenantID, r.From)
	if err != nil {
		fail(c, http.StatusInternalServerError, "opening balance")
		return
	}
	closing, err := s.balanceAsOf(ctx, tenantID, r.To)
	if err != nil {
		fail(c, http.StatusInternalServerError, "closing balance")
		return
	}
	bill.Closing = closing

	err = pool.QueryRow(ctx, `
SELECT
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='topup'), 0),
  COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0),
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='refund'), 0),
  COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='adjust'), 0)
  FROM wallet_ledger l
 WHERE l.tenant_id=$1 AND l.created_at >= $2 AND l.created_at < $3`,
		tenantID, r.From, r.To).
		Scan(&bill.Summary.Topup, &bill.Summary.Settle, &bill.Summary.Refund, &bill.Summary.Adjust)
	if err != nil {
		fail(c, http.StatusInternalServerError, "bill summary")
		return
	}

	drows, err := pool.Query(ctx, `
SELECT to_char(date_trunc('day', l.created_at AT TIME ZONE 'Asia/Shanghai'), 'YYYY-MM-DD') AS day,
       COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='topup'), 0),
       COALESCE(-SUM(`+netExpr+`) FILTER (WHERE l.kind='settle'), 0),
       COALESCE(SUM(`+netExpr+`) FILTER (WHERE l.kind='refund'), 0)
  FROM wallet_ledger l
 WHERE l.tenant_id=$1 AND l.created_at >= $2 AND l.created_at < $3
 GROUP BY day ORDER BY day`, tenantID, r.From, r.To)
	if err != nil {
		fail(c, http.StatusInternalServerError, "bill daily")
		return
	}
	defer drows.Close()
	for drows.Next() {
		var d dailyPoint
		if err := drows.Scan(&d.Day, &d.Topup, &d.Settle, &d.Refund); err != nil {
			fail(c, http.StatusInternalServerError, "scan bill daily")
			return
		}
		bill.Daily = append(bill.Daily, d)
	}
	if err := drows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate bill daily")
		return
	}

	ok(c, bill)
}

func (s *Server) handleAdminFinanceBillExport(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, err := strconv.ParseInt(c.Query("tenant_id"), 10, 64)
	if err != nil || tenantID <= 0 {
		fail(c, http.StatusBadRequest, "tenant_id required")
		return
	}
	r, err := parseBillRange(c.Query("from"), c.Query("to"))
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.systemPool().Query(ctx, `
SELECT l.id, l.created_at::text, l.tenant_id, COALESCE(t.name,''), l.kind::text,
       l.delta_balance, l.delta_frozen, l.balance_after, l.frozen_after
  FROM wallet_ledger l
  LEFT JOIN tenants t ON t.id = l.tenant_id
 WHERE l.tenant_id=$1 AND l.created_at >= $2 AND l.created_at < $3
 ORDER BY l.id`, tenantID, r.From, r.To)
	if err != nil {
		fail(c, http.StatusInternalServerError, "bill export query")
		return
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		var id, tid, dbal, dfroz, bal, froz int64
		var created, name, kind string
		if err := rows.Scan(&id, &created, &tid, &name, &kind, &dbal, &dfroz, &bal, &froz); err != nil {
			fail(c, http.StatusInternalServerError, "scan bill export")
			return
		}
		out = append(out, []string{
			strconv.FormatInt(id, 10), created, strconv.FormatInt(tid, 10), name, kind,
			strconv.FormatInt(dbal, 10), strconv.FormatInt(dfroz, 10),
			strconv.FormatInt(bal, 10), strconv.FormatInt(froz, 10),
		})
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate bill export")
		return
	}
	s.recordAudit(ctx, auditEvent{
		TenantID: tenantID, ActorID: actorID(c), Action: "finance.bill_export",
		ResourceType: "tenant", ResourceID: tenantID,
		Details: gin.H{"from": c.Query("from"), "to": c.Query("to"), "rows": len(out)},
	})
	writeCSV(c, "bill-"+strconv.FormatInt(tenantID, 10)+".csv",
		[]string{"id", "created_at", "tenant_id", "tenant_name", "kind", "delta_balance", "delta_frozen", "balance_after", "frozen_after"},
		out)
}
```

Update the `finance_stats.go` import block to:

```go
import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)
```

- [ ] **Step 4: Register routes**

In `internal/api/router.go`, after the stats route, add:

```go
		admin.GET("/finance/bill", s.handleAdminFinanceBill)
		admin.GET("/finance/bill/export", s.handleAdminFinanceBillExport)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /var/klwa && go test ./internal/api/ -run TestHandleAdminFinanceBill`
Expected: PASS (all three bill tests).

- [ ] **Step 6: Commit**

```bash
cd /var/klwa && git add internal/api/finance_stats.go internal/api/router.go internal/api/finance_db_test.go
git commit -m "feat(admin): per-tenant bill endpoint + CSV export with reconciliation (SP4)"
```

---

## Task 8: Campaign filter builder + list upgrade

**Files:**
- Modify: `internal/api/finance_query.go` (add campaign filter — it lives here to keep query builders together)
- Modify: `internal/api/finance_query_test.go`
- Modify: `internal/api/admin_api.go` (`handleAdminListCampaigns`)
- Modify: `internal/api/finance_db_test.go`

**Interfaces:**
- Produces: `type campaignFilter struct { Q string; State string; Limit, Offset int }`
- Produces: `func buildCampaignWhere(f campaignFilter) (string, []any)` — clause for `campaigns c` joined to `tenants t`. `State` → `c.state::text = $n`; `Q` → numeric matches `c.id` or `c.tenant_id`, non-numeric matches `t.name ILIKE`.
- Produces: `GET /admin/campaigns?state&q&limit&offset` → `ok(c, {rows, total, stats})`. `stats` = `{running, paused, tripped, total}` (unfiltered global counts). `rows` gains `tenant_name`.

- [ ] **Step 1: Write the failing filter test**

Append to `internal/api/finance_query_test.go`:

```go
func TestBuildCampaignWhere(t *testing.T) {
	if w, _ := buildCampaignWhere(campaignFilter{}); w != "" {
		t.Errorf("empty where = %q", w)
	}
	w, args := buildCampaignWhere(campaignFilter{State: "running"})
	if w != " WHERE c.state::text = $1" || len(args) != 1 || args[0] != "running" {
		t.Errorf("state where=%q args=%v", w, args)
	}
	// numeric q matches id or tenant_id
	w, args = buildCampaignWhere(campaignFilter{Q: "42"})
	if w != " WHERE (c.id = $1 OR c.tenant_id = $1)" || args[0] != int64(42) {
		t.Errorf("numeric q where=%q args=%v", w, args)
	}
	// text q matches tenant name
	w, args = buildCampaignWhere(campaignFilter{Q: "acme"})
	if w != " WHERE t.name ILIKE $1" || args[0] != "%acme%" {
		t.Errorf("text q where=%q args=%v", w, args)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/api/ -run TestBuildCampaignWhere`
Expected: FAIL — `undefined: buildCampaignWhere`.

- [ ] **Step 3: Implement filter**

Append to `internal/api/finance_query.go`:

```go
import "strconv" // add to existing import block if not present

// campaignFilter is the parsed query for the admin campaign list.
type campaignFilter struct {
	Q      string
	State  string
	Limit  int
	Offset int
}

// buildCampaignWhere returns the clause for campaigns "c" joined to tenants "t".
func buildCampaignWhere(f campaignFilter) (string, []any) {
	var conds []string
	var args []any
	if f.State != "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("c.state::text = $%d", len(args)))
	}
	if f.Q != "" {
		if id, err := strconv.ParseInt(f.Q, 10, 64); err == nil {
			args = append(args, id)
			conds = append(conds, fmt.Sprintf("(c.id = $%d OR c.tenant_id = $%d)", len(args), len(args)))
		} else {
			args = append(args, "%"+f.Q+"%")
			conds = append(conds, fmt.Sprintf("t.name ILIKE $%d", len(args)))
		}
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
```

(Merge the `strconv` import into the file's existing import block rather than a second `import` line.)

- [ ] **Step 4: Run filter test to verify it passes**

Run: `cd /var/klwa && go test ./internal/api/ -run TestBuildCampaignWhere`
Expected: PASS.

- [ ] **Step 5: Write the failing handler DB test**

Append to `internal/api/finance_db_test.go`:

```go
// seedCampaign inserts a template + campaign and returns the campaign id.
func seedCampaign(t *testing.T, ctx context.Context, s *Server, tenantID int64, state string, total, sent, failed int) int64 {
	t.Helper()
	var tplID int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES ($1,'text','hi') RETURNING id`, tenantID).Scan(&tplID); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	var id int64
	if err := s.systemPool().QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state, total, sent, failed)
		 VALUES ($1,$2,$3::campaign_state_t,$4,$5,$6) RETURNING id`,
		tenantID, tplID, state, total, sent, failed).Scan(&id); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	return id
}

func TestHandleAdminListCampaignsFiltered(t *testing.T) {
	s, ctx := newFinanceServer(t)
	tid, _ := seedTenantUser(t, ctx, s, "camp@acme.test")
	seedCampaign(t, ctx, s, tid, "running", 100, 50, 2)
	seedCampaign(t, ctx, s, tid, "paused", 80, 80, 0)
	seedCampaign(t, ctx, s, tid, "completed", 10, 10, 0)

	// filter state=running → 1 row, but stats counts are global
	w := doGET(t, s, s.handleAdminListCampaigns, "/admin/campaigns?state=running")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"total":1`) {
		t.Errorf("want filtered total 1: %s", body)
	}
	if !contains(body, `"running":1`) || !contains(body, `"paused":1`) {
		t.Errorf("want global stats running=1 paused=1: %s", body)
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/api/ -run TestHandleAdminListCampaignsFiltered`
Expected: FAIL — response is a bare array, no `total`/`stats`.

- [ ] **Step 7: Rewrite `handleAdminListCampaigns`**

Replace the handler in `internal/api/admin_api.go`. Add a `parseCampaignFilter` near the other parsers:

```go
func parseCampaignFilter(c *gin.Context) campaignFilter {
	f := campaignFilter{Q: c.Query("q"), State: c.Query("state")}
	if n, err := strconv.Atoi(c.Query("limit")); err == nil {
		f.Limit = n
	}
	if n, err := strconv.Atoi(c.Query("offset")); err == nil && n > 0 {
		f.Offset = n
	}
	return f
}
```

Rewrite the handler body:

```go
func (s *Server) handleAdminListCampaigns(c *gin.Context) {
	ctx := c.Request.Context()
	pool := s.systemPool()
	f := parseCampaignFilter(c)
	where, args := buildCampaignWhere(f)

	listArgs := append(append([]any{}, args...), clampPage(f.Limit, 20, 200), f.Offset)
	list := `
SELECT c.id, c.tenant_id, t.name, c.state::text, c.total, c.sent, c.failed, c.created_at::text,
       (c.state = 'paused' AND
        COALESCE((SELECT max(occurred_at) FROM audit_log a
                   WHERE a.action='campaign.circuit_break' AND a.resource_id=c.id), 'epoch') >
        COALESCE((SELECT max(occurred_at) FROM audit_log a
                   WHERE a.action='campaign.resume' AND a.resource_id=c.id), 'epoch')
       ) AS auto_tripped
  FROM campaigns c
  LEFT JOIN tenants t ON t.id = c.tenant_id` + where +
		fmt.Sprintf(" ORDER BY c.id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := pool.Query(ctx, list, listArgs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list campaigns")
		return
	}
	defer rows.Close()
	out := make([]adminCampaignRow, 0)
	for rows.Next() {
		var cp adminCampaignRow
		if err := rows.Scan(&cp.ID, &cp.TenantID, &cp.TenantName, &cp.State, &cp.Total, &cp.Sent, &cp.Failed, &cp.CreatedAt, &cp.AutoTripped); err != nil {
			fail(c, http.StatusInternalServerError, "scan campaign")
			return
		}
		out = append(out, cp)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate campaigns")
		return
	}

	var total int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM campaigns c LEFT JOIN tenants t ON t.id = c.tenant_id`+where, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count campaigns")
		return
	}

	var st struct {
		Running int64 `json:"running"`
		Paused  int64 `json:"paused"`
		Tripped int64 `json:"tripped"`
		Total   int64 `json:"total"`
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE state='running'),
       count(*) FILTER (WHERE state='paused'),
       count(*) FILTER (WHERE state='paused' AND
         COALESCE((SELECT max(occurred_at) FROM audit_log a WHERE a.action='campaign.circuit_break' AND a.resource_id=campaigns.id),'epoch') >
         COALESCE((SELECT max(occurred_at) FROM audit_log a WHERE a.action='campaign.resume' AND a.resource_id=campaigns.id),'epoch')),
       count(*)
  FROM campaigns`).Scan(&st.Running, &st.Paused, &st.Tripped, &st.Total); err != nil {
		fail(c, http.StatusInternalServerError, "campaign stats")
		return
	}

	ok(c, gin.H{"rows": out, "total": total, "stats": st})
}
```

Add `TenantName *string` to the `adminCampaignRow` struct (find its definition) with JSON tag `json:"tenant_name"`.

- [ ] **Step 8: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/api/ -run TestHandleAdminListCampaignsFiltered`
Expected: PASS.

- [ ] **Step 9: Full package test + build**

Run: `cd /var/klwa && go build ./... && go test ./internal/api/`
Expected: builds; all api tests pass (including pre-existing ones — watch for `adminCampaignRow`/`ledgerRow` scan-arity drift).

- [ ] **Step 10: Commit**

```bash
cd /var/klwa && git add internal/api/finance_query.go internal/api/finance_query_test.go internal/api/admin_api.go internal/api/finance_db_test.go
git commit -m "feat(admin): campaign list pagination + state/q filters + stats (SP4)"
```

---

## Task 9: Nav + audit page cleanup

**Files:**
- Modify: `frontend/components/admin/nav.ts`
- Modify: `frontend/app/admin/audit/page.tsx`
- Create: `frontend/app/admin/campaigns/page.tsx`
- Create: `frontend/app/admin/billing/page.tsx`
- Delete: `frontend/components/admin-audit-tabs.tsx`

**Interfaces:**
- Consumes: `AdminAuditLog`, `AdminCampaigns` (existing), `AdminBilling` (created in Task 11 — page shell references it; build this task AFTER Task 11, or stub then wire).

Note: to keep each task independently buildable, this task creates the campaigns + audit changes now and defers the billing page creation to the end of Task 11. Do the nav entry for billing here, but create `app/admin/billing/page.tsx` in Task 11's final step.

- [ ] **Step 1: Add nav items**

In `frontend/components/admin/nav.ts`, update imports to add `Send` and `PieChart`:

```ts
import {
  LayoutDashboard,
  Building2,
  UserCog,
  Globe,
  Smartphone,
  ScrollText,
  SlidersHorizontal,
  ReceiptText,
  Send,
  PieChart,
  type LucideIcon,
} from "lucide-react";
```

Add 账单统计 to the "IAM 与账单" group (after ledger) and a new "发送中心" group before "策略中心":

```ts
  {
    title: "IAM 与账单",
    items: [
      { href: "/admin/tenants", label: "租户与财务", en: "Tenants", icon: Building2 },
      { href: "/admin/users", label: "用户管理", en: "Users", icon: UserCog },
      { href: "/admin/ledger", label: "财务流水", en: "Ledger", icon: ReceiptText },
      { href: "/admin/billing", label: "账单统计", en: "Billing", icon: PieChart },
    ],
  },
  {
    title: "资源大厅",
    items: [
      { href: "/admin/resources", label: "代理网络池", en: "Proxies", icon: Globe },
      { href: "/admin/devices", label: "节点与设备", en: "Devices", icon: Smartphone },
    ],
  },
  {
    title: "发送中心",
    items: [
      { href: "/admin/campaigns", label: "发送任务", en: "Campaigns", icon: Send },
    ],
  },
```

- [ ] **Step 2: Simplify audit page (drop tabs)**

Replace `frontend/app/admin/audit/page.tsx`:

```tsx
import { PageHeader } from "@/components/admin/page-header";
import { AdminAuditLog } from "@/components/admin-audit-log";

export default function AdminAuditPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="God View · Risk"
        title="风控与审计"
        description="平台操作审计日志。群发任务监控已迁移至「发送任务」。"
      />
      <AdminAuditLog />
    </div>
  );
}
```

- [ ] **Step 3: Create campaigns page shell**

Create `frontend/app/admin/campaigns/page.tsx`:

```tsx
import { PageHeader } from "@/components/admin/page-header";
import { AdminCampaigns } from "@/components/admin-campaigns";

export default function AdminCampaignsPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Send Center"
        title="发送任务"
        description="全租户群发任务监控:筛选、强制暂停/恢复、下钻收件人明细。"
      />
      <AdminCampaigns />
    </div>
  );
}
```

- [ ] **Step 4: Delete the tabs component**

```bash
cd /var/klwa && git rm frontend/components/admin-audit-tabs.tsx
```

- [ ] **Step 5: Verify build**

Run: `cd /var/klwa/frontend && npm run lint && npm run build`
Expected: builds. (Billing nav item points to `/admin/billing`, created in Task 11 — Next.js will 404 that route until then, which is fine for build; do not click it before Task 11.)

- [ ] **Step 6: Commit**

```bash
cd /var/klwa && git add frontend/components/admin/nav.ts frontend/app/admin/audit/page.tsx frontend/app/admin/campaigns/page.tsx
git commit -m "feat(admin): promote campaigns to its own page, add billing nav, drop audit tabs (SP4)"
```

---

## Task 10: Campaigns page — server mode + tabs + stat cards

**Files:**
- Modify: `frontend/components/admin/pro-data-table.tsx` (add `onRowClick`)
- Modify: `frontend/components/admin-campaigns.tsx`

**Interfaces:**
- Consumes: `ProDataTable` (server mode), `StatCard`/`MetricCardGroup`, `CampaignDetailSheet`, `api.get`.
- Produces (pro-data-table): optional prop `onRowClick?: (row: T) => void` — when set, rows get `cursor-pointer` + `onClick`.

- [ ] **Step 1: Add `onRowClick` to ProDataTable**

In `frontend/components/admin/pro-data-table.tsx`, add to `ProDataTableProps<T>`:

```ts
  /** When set, clicking a row calls this (rows become cursor-pointer). */
  onRowClick?: (row: T) => void;
```

Add `onRowClick` to the destructured props, then update the data-row `<TableRow>` (around line 175):

```tsx
            rows.map((row) => (
              <TableRow
                key={getRowKey(row)}
                className={onRowClick ? "cursor-pointer" : undefined}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
              >
```

- [ ] **Step 2: Rewrite `admin-campaigns.tsx` to server mode**

Replace the component (keep the `Campaign`, `stateVariant`, `nf` top-level declarations; add `tenant_name` to the interface). The new body:

```tsx
"use client";

import { useCallback, useEffect, useState } from "react";
import { Ban, Play, ShieldAlert, Radio, Pause, Zap, ListChecks } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { StatCard, MetricCardGroup } from "@/components/admin/stat-card";
import { CampaignDetailSheet } from "@/components/campaign-detail-sheet";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

interface Campaign {
  id: number;
  tenant_id: number;
  tenant_name: string | null;
  state: "draft" | "running" | "paused" | "completed" | "failed";
  total: number;
  sent: number;
  failed: number;
  created_at: string;
  auto_tripped: boolean;
}
interface CampaignStats {
  running: number;
  paused: number;
  tripped: number;
  total: number;
}

const stateVariant: Record<Campaign["state"], "default" | "secondary" | "outline" | "destructive"> = {
  running: "secondary",
  paused: "outline",
  draft: "outline",
  completed: "default",
  failed: "destructive",
};
const nf = new Intl.NumberFormat("en-US");
const PAGE_SIZE = 10;

const STATE_TABS: { key: string; label: string }[] = [
  { key: "", label: "全部" },
  { key: "running", label: "运行中" },
  { key: "paused", label: "已暂停" },
  { key: "completed", label: "已完成" },
  { key: "failed", label: "失败" },
];

export function AdminCampaigns() {
  const [rows, setRows] = useState<Campaign[] | null>(null);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<CampaignStats | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [state, setState] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [killTarget, setKillTarget] = useState<Campaign | null>(null);
  const [busy, setBusy] = useState(false);
  const [resumingId, setResumingId] = useState<number | null>(null);
  const [detailId, setDetailId] = useState<number | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (state) params.set("state", state);
    try {
      const d = await api.get<{ rows: Campaign[]; total: number; stats: CampaignStats }>(
        `/admin/campaigns?${params.toString()}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setStats(d.stats);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, state]);

  useEffect(() => {
    load();
  }, [load]);

  async function confirmKill() {
    if (!killTarget) return;
    setBusy(true);
    try {
      await api.post(`/admin/campaigns/${killTarget.id}/stop`);
      toast.success("已强制暂停", { description: `任务 #${killTarget.id} → paused` });
      setKillTarget(null);
      load();
    } catch (e) {
      toast.error("操作失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setBusy(false);
    }
  }

  async function resume(c: Campaign) {
    setResumingId(c.id);
    try {
      await api.post(`/admin/campaigns/${c.id}/resume`);
      toast.success("已恢复任务", { description: `任务 #${c.id} → running` });
      load();
    } catch (e) {
      toast.error("恢复失败", { description: e instanceof ApiError ? e.message : "请重试" });
    } finally {
      setResumingId(null);
    }
  }

  const tenantLabel = (c: Campaign) => c.tenant_name ?? `#${c.tenant_id}`;

  const columns: Column<Campaign>[] = [
    { key: "id", header: "任务", cell: (c) => <span className="font-mono text-sm">#{c.id}</span> },
    { key: "tenant", header: "租户", cell: (c) => <span className="text-sm text-muted-foreground">{tenantLabel(c)}</span> },
    {
      key: "state",
      header: "状态",
      cell: (c) => (
        <div className="flex items-center gap-1.5">
          <Badge variant={stateVariant[c.state]}>{c.state}</Badge>
          {c.auto_tripped && (
            <Badge variant="destructive" className="gap-1" title="风控熔断器因封号率超阈值自动挂起了此任务">
              <ShieldAlert className="size-3" />
              自动熔断
            </Badge>
          )}
        </div>
      ),
    },
    { key: "total", header: "总数", align: "right", cell: (c) => <span className="font-mono tabular-nums text-sm">{nf.format(c.total)}</span> },
    { key: "sent", header: "已发", align: "right", cell: (c) => <span className="font-mono tabular-nums text-sm">{nf.format(c.sent)}</span> },
    { key: "failed", header: "失败", align: "right", cell: (c) => <span className="font-mono tabular-nums text-sm text-muted-foreground">{nf.format(c.failed)}</span> },
  ];

  return (
    <div className="space-y-6">
      {stats && (
        <MetricCardGroup>
          <StatCard accent="brand" label="任务总数" value={String(stats.total)} sub="全平台" icon={ListChecks} />
          <StatCard accent="emerald" label="运行中" value={String(stats.running)} sub="正在发送" icon={Radio} />
          <StatCard accent="amber" label="已暂停" value={String(stats.paused)} sub="含手动/熔断" icon={Pause} />
          <StatCard accent="rose" label="熔断挂起" value={String(stats.tripped)} sub="风控自动触发" icon={Zap} />
        </MetricCardGroup>
      )}

      <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
        {STATE_TABS.map((t) => (
          <button
            key={t.key || "all"}
            onClick={() => {
              setState(t.key);
              setPage(0);
            }}
            className={
              "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
              (state === t.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
            }
          >
            {t.label}
          </button>
        ))}
      </div>

      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(c) => c.id}
        onRowClick={(c) => setDetailId(c.id)}
        emptyState="当前没有任务。"
        server={{
          total,
          page,
          pageSize: PAGE_SIZE,
          onPageChange: setPage,
          query,
          onQueryChange: (q) => {
            setQuery(q);
            setPage(0);
          },
          loading,
        }}
        rowActions={(c) =>
          c.state === "paused" ? (
            <Button variant="ghost" size="sm" className="gap-1.5" disabled={resumingId === c.id} onClick={() => resume(c)}>
              <Play className="size-3.5" />
              {resumingId === c.id ? "恢复中…" : "恢复"}
            </Button>
          ) : (
            <Button
              variant="ghost"
              size="sm"
              className="gap-1.5 text-destructive hover:text-destructive"
              disabled={c.state !== "running"}
              onClick={() => setKillTarget(c)}
            >
              <Ban className="size-3.5" />
              强制终止
            </Button>
          )
        }
      />

      <Dialog open={killTarget != null} onOpenChange={(o) => !o && setKillTarget(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>强制终止任务</DialogTitle>
            <DialogDescription>
              将任务 <span className="font-mono">#{killTarget?.id}</span>(租户 {killTarget ? tenantLabel(killTarget) : ""})置为 paused,
              调度器会立即停止继续发送。此操作不可在界面撤销。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose render={<Button variant="ghost" />}>取消</DialogClose>
            <Button variant="destructive" onClick={confirmKill} disabled={busy}>
              {busy ? "处理中…" : "确认终止"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <CampaignDetailSheet campaignId={detailId} apiBase="/admin/campaigns" onClose={() => setDetailId(null)} />
    </div>
  );
}
```

Note: `rowActions` cells must not trigger the row click. `ProDataTable` renders `rowActions` in a separate `<TableCell>`; add `onClick={(e) => e.stopPropagation()}` guard. Update the `rowActions` cell wrapper in `pro-data-table.tsx`:

```tsx
                {rowActions && (
                  <TableCell className="py-2.5 text-right" onClick={(e) => e.stopPropagation()}>
                    {rowActions(row)}
                  </TableCell>
                )}
```

- [ ] **Step 3: Verify build**

Run: `cd /var/klwa/frontend && npm run lint && npm run build`
Expected: builds.

- [ ] **Step 4: Commit**

```bash
cd /var/klwa && git add frontend/components/admin/pro-data-table.tsx frontend/components/admin-campaigns.tsx
git commit -m "feat(admin): campaigns page server-mode table + state tabs + stat cards (SP4)"
```

---

## Task 11: Billing page (dashboard + per-tenant bill)

**Files:**
- Create: `frontend/components/billing-trend-chart.tsx`
- Create: `frontend/components/admin-billing.tsx`
- Create: `frontend/app/admin/billing/page.tsx`

**Interfaces:**
- Consumes: `api.get`, `api.download`, `StatCard`/`MetricCardGroup`, `ProDataTable`, endpoints `/admin/finance/stats`, `/admin/finance/bill`, `/admin/finance/bill/export`.
- Produces: `AdminBilling` default-exported component; `BillingTrendChart` named export.

- [ ] **Step 1: Create the trend chart**

Create `frontend/components/billing-trend-chart.tsx`:

```tsx
"use client";

import { useEffect, useState } from "react";
import { AreaChart } from "@tremor/react";

export interface DailyPoint {
  day: string;
  topup: number;
  settle: number;
  refund: number;
}

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

export function BillingTrendChart({ daily }: { daily: DailyPoint[] }) {
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  if (!mounted) {
    return <div className="h-72 w-full animate-pulse rounded-md bg-muted motion-reduce:animate-none" />;
  }

  const data = daily.map((d) => ({ date: d.day.slice(5), 充值: d.topup, 消耗: d.settle }));
  return (
    <AreaChart
      className="h-72"
      data={data}
      index="date"
      categories={["充值", "消耗"]}
      colors={["emerald", "rose"]}
      valueFormatter={usd}
      showLegend
      showGradient
      curveType="monotone"
      yAxisWidth={64}
      showAnimation
    />
  );
}
```

- [ ] **Step 2: Create the billing component**

Create `frontend/components/admin-billing.tsx`:

```tsx
"use client";

import { useCallback, useEffect, useState } from "react";
import { ArrowLeft, Download, Wallet, TrendingDown, RotateCcw, Scale } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { StatCard, MetricCardGroup } from "@/components/admin/stat-card";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";
import { BillingTrendChart, type DailyPoint } from "@/components/billing-trend-chart";

interface Summary {
  topup: number;
  settle: number;
  refund: number;
  adjust: number;
  net: number;
}
interface TenantSpend {
  tenant_id: number;
  name: string;
  settle: number;
  topup: number;
}
interface Stats {
  summary: Summary;
  daily: DailyPoint[];
  top_tenants: TenantSpend[];
}
interface Bill {
  tenant_id: number;
  name: string;
  opening: number;
  closing: number;
  summary: { topup: number; settle: number; refund: number; adjust: number };
  daily: DailyPoint[];
}

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

const RANGES: { key: string; label: string; days: number }[] = [
  { key: "7", label: "近 7 天", days: 7 },
  { key: "30", label: "近 30 天", days: 30 },
  { key: "90", label: "近 90 天", days: 90 },
];

// Compute a YYYY-MM-DD pair for the last N days ending today (client-local).
function rangeParams(days: number): { from: string; to: string } {
  const now = new Date();
  const to = now.toISOString().slice(0, 10);
  const fromDate = new Date(now.getTime() - (days - 1) * 86400000);
  const from = fromDate.toISOString().slice(0, 10);
  return { from, to };
}

export function AdminBilling() {
  const [rangeKey, setRangeKey] = useState("30");
  const [stats, setStats] = useState<Stats | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<TenantSpend | null>(null);
  const [bill, setBill] = useState<Bill | null>(null);

  const days = RANGES.find((r) => r.key === rangeKey)?.days ?? 30;

  const loadStats = useCallback(async () => {
    const { from, to } = rangeParams(days);
    try {
      setStats(await api.get<Stats>(`/admin/finance/stats?from=${from}&to=${to}`));
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    }
  }, [days]);

  useEffect(() => {
    loadStats();
  }, [loadStats]);

  const loadBill = useCallback(
    async (t: TenantSpend) => {
      const { from, to } = rangeParams(days);
      setSelected(t);
      setBill(null);
      try {
        setBill(await api.get<Bill>(`/admin/finance/bill?tenant_id=${t.tenant_id}&from=${from}&to=${to}`));
      } catch (e) {
        toast.error("加载账单失败", { description: e instanceof ApiError ? e.message : "请重试" });
      }
    },
    [days],
  );

  async function exportBill() {
    if (!selected) return;
    const { from, to } = rangeParams(days);
    try {
      await api.download(
        `/admin/finance/bill/export?tenant_id=${selected.tenant_id}&from=${from}&to=${to}`,
        `bill-${selected.tenant_id}.csv`,
      );
    } catch (e) {
      toast.error("导出失败", { description: e instanceof ApiError ? e.message : "请重试" });
    }
  }

  const rangePicker = (
    <div className="inline-flex rounded-lg border bg-muted/40 p-0.5">
      {RANGES.map((r) => (
        <button
          key={r.key}
          onClick={() => setRangeKey(r.key)}
          className={
            "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
            (rangeKey === r.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
          }
        >
          {r.label}
        </button>
      ))}
    </div>
  );

  // --- per-tenant bill view ---
  if (selected) {
    return (
      <div className="space-y-6">
        <div className="flex items-center justify-between gap-3">
          <Button variant="ghost" size="sm" className="gap-1.5" onClick={() => { setSelected(null); setBill(null); }}>
            <ArrowLeft className="size-4" />
            返回大盘
          </Button>
          <Button variant="outline" size="sm" className="gap-1.5" onClick={exportBill} disabled={!bill}>
            <Download className="size-4" />
            导出 CSV
          </Button>
        </div>

        <div>
          <h2 className="text-lg font-semibold">{selected.name || `租户 #${selected.tenant_id}`}</h2>
          <p className="font-mono text-xs text-muted-foreground">账期口径:Asia/Shanghai · {RANGES.find((r) => r.key === rangeKey)?.label}</p>
        </div>

        {!bill ? (
          <div className="h-40 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />
        ) : (
          <>
            <MetricCardGroup>
              <StatCard accent="neutral" label="期初余额" value={usd(bill.opening)} icon={Wallet} />
              <StatCard accent="emerald" label="充值" value={usd(bill.summary.topup)} icon={Wallet} />
              <StatCard accent="rose" label="消耗" value={usd(bill.summary.settle)} icon={TrendingDown} />
              <StatCard accent="brand" label="期末余额" value={usd(bill.closing)} icon={Scale} />
            </MetricCardGroup>
            <Card className="p-5">
              <p className="mb-3 font-mono text-xs uppercase tracking-wider text-muted-foreground">按天充值 / 消耗</p>
              <BillingTrendChart daily={bill.daily} />
            </Card>
          </>
        )}
      </div>
    );
  }

  // --- platform dashboard ---
  if (error) return <Card className="p-5 text-sm text-muted-foreground">加载失败:{error}</Card>;

  const tenantCols: Column<TenantSpend>[] = [
    { key: "name", header: "租户", cell: (t) => <span className="text-sm">{t.name || `#${t.tenant_id}`}</span> },
    { key: "settle", header: "消耗", align: "right", cell: (t) => <span className="font-mono tabular-nums text-rose-600 dark:text-rose-400">{usd(t.settle)}</span> },
    { key: "topup", header: "充值", align: "right", cell: (t) => <span className="font-mono tabular-nums text-emerald-600 dark:text-emerald-400">{usd(t.topup)}</span> },
  ];

  return (
    <div className="space-y-6">
      <div className="flex justify-end">{rangePicker}</div>

      {!stats ? (
        <div className="h-40 animate-pulse rounded-xl bg-muted motion-reduce:animate-none" />
      ) : (
        <>
          <MetricCardGroup>
            <StatCard accent="emerald" label="总充值" value={usd(stats.summary.topup)} icon={Wallet} />
            <StatCard accent="rose" label="总消耗" value={usd(stats.summary.settle)} icon={TrendingDown} />
            <StatCard accent="amber" label="总退款" value={usd(stats.summary.refund)} icon={RotateCcw} />
            <StatCard accent="brand" label="净额" value={usd(stats.summary.net)} sub="充值−消耗+退款+调整" icon={Scale} />
          </MetricCardGroup>

          <Card className="p-5">
            <p className="mb-3 font-mono text-xs uppercase tracking-wider text-muted-foreground">按天充值 / 消耗</p>
            <BillingTrendChart daily={stats.daily} />
          </Card>

          <section className="space-y-2">
            <p className="font-mono text-xs uppercase tracking-wider text-muted-foreground">租户消耗排行 · Top 20 · 点击查看账单</p>
            <ProDataTable
              data={stats.top_tenants}
              columns={tenantCols}
              getRowKey={(t) => t.tenant_id}
              onRowClick={(t) => loadBill(t)}
              emptyState="该账期暂无消耗记录"
            />
          </section>

          <p className="font-mono text-xs text-muted-foreground">金额单位 USD · 数据源 wallet_ledger · 时区 Asia/Shanghai</p>
        </>
      )}
    </div>
  );
}
```

- [ ] **Step 3: Create the billing page shell**

Create `frontend/app/admin/billing/page.tsx`:

```tsx
import { PageHeader } from "@/components/admin/page-header";
import { AdminBilling } from "@/components/admin-billing";

export default function AdminBillingPage() {
  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        eyebrow="Billing & IAM"
        title="账单统计"
        description="平台资金大盘与租户账单。数据源为钱包流水,可导出对账。"
      />
      <AdminBilling />
    </div>
  );
}
```

- [ ] **Step 4: Verify build**

Run: `cd /var/klwa/frontend && npm run lint && npm run build`
Expected: builds; `/admin/billing` route now resolves.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa && git add frontend/components/billing-trend-chart.tsx frontend/components/admin-billing.tsx frontend/app/admin/billing/page.tsx
git commit -m "feat(admin): billing statistics page — dashboard + per-tenant bill + export (SP4)"
```

---

## Task 12: Ledger page — kind tabs + server pagination + export

**Files:**
- Modify: `frontend/components/admin-ledger.tsx`

**Interfaces:**
- Consumes: `ProDataTable` server mode, `api.get`, `api.download`, new ledger envelope `{rows, total, refunds}` with `tenant_name`/`frozen_after`.

- [ ] **Step 1: Rewrite `admin-ledger.tsx`**

Replace with server-mode ledger + kind tabs + export button (refunds section stays client-mode):

```tsx
"use client";

import { useCallback, useEffect, useState } from "react";
import { Download } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ProDataTable, type Column } from "@/components/admin/pro-data-table";

interface LedgerRow {
  id: number;
  tenant_id: number;
  tenant_name: string | null;
  kind: string;
  delta_balance: number;
  delta_frozen: number;
  balance_after: number;
  frozen_after: number;
  created_at: string;
}
interface RefundRow {
  id: number;
  tenant_id: number;
  amount: number;
  reason: string;
  state: string;
}

const usd = (cents: number) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(cents / 100);

function Money({ cents }: { cents: number }) {
  const cls = cents < 0 ? "text-rose-600 dark:text-rose-400" : "text-emerald-600 dark:text-emerald-400";
  return <span className={`font-mono tabular-nums ${cls}`}>{usd(cents)}</span>;
}

const refundStateVariant: Record<string, "default" | "secondary" | "outline"> = {
  approved: "default",
  pending: "secondary",
  rejected: "outline",
};

const KIND_TABS: { key: string; label: string }[] = [
  { key: "", label: "全部" },
  { key: "topup", label: "充值" },
  { key: "settle", label: "结算" },
  { key: "refund", label: "退款" },
  { key: "adjust", label: "调整" },
];
const PAGE_SIZE = 15;

export function AdminLedger() {
  const [rows, setRows] = useState<LedgerRow[] | null>(null);
  const [total, setTotal] = useState(0);
  const [refunds, setRefunds] = useState<RefundRow[] | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [kind, setKind] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (kind) params.set("kind", kind);
    if (query.trim()) params.set("tenant_id", query.trim());
    try {
      const d = await api.get<{ rows: LedgerRow[]; total: number; refunds: RefundRow[] }>(
        `/admin/finance/ledger?${params.toString()}`,
      );
      setRows(d.rows);
      setTotal(d.total);
      setRefunds(d.refunds);
      setError(null);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) {
        setError(e instanceof ApiError ? e.message : "加载失败");
      }
    } finally {
      setLoading(false);
    }
  }, [page, query, kind]);

  useEffect(() => {
    load();
  }, [load]);

  async function exportCsv() {
    const params = new URLSearchParams();
    if (kind) params.set("kind", kind);
    if (query.trim()) params.set("tenant_id", query.trim());
    try {
      await api.download(`/admin/finance/ledger/export?${params.toString()}`, "ledger.csv");
    } catch (e) {
      toast.error("导出失败", { description: e instanceof ApiError ? e.message : "请重试" });
    }
  }

  const tenantLabel = (r: LedgerRow) => r.tenant_name ?? `#${r.tenant_id}`;

  const ledgerCols: Column<LedgerRow>[] = [
    { key: "kind", header: "类型", cell: (r) => <Badge variant="outline">{r.kind}</Badge> },
    { key: "tenant", header: "租户", cell: (r) => <span className="text-sm text-muted-foreground">{tenantLabel(r)}</span> },
    { key: "delta_balance", header: "余额变动", align: "right", cell: (r) => <Money cents={r.delta_balance} /> },
    { key: "delta_frozen", header: "冻结变动", align: "right", cell: (r) => <span className="font-mono tabular-nums text-muted-foreground">{usd(r.delta_frozen)}</span> },
    { key: "balance_after", header: "变动后余额", align: "right", cell: (r) => <span className="font-mono tabular-nums">{usd(r.balance_after)}</span> },
    { key: "created_at", header: "时间", cell: (r) => <span className="font-mono text-xs text-muted-foreground">{r.created_at}</span> },
  ];

  const refundCols: Column<RefundRow>[] = [
    { key: "tenant", header: "租户", cell: (r) => <span className="text-sm text-muted-foreground">#{r.tenant_id}</span> },
    { key: "amount", header: "金额", align: "right", cell: (r) => <span className="font-mono tabular-nums">{usd(r.amount)}</span> },
    { key: "reason", header: "原因", cell: (r) => <span className="text-sm">{r.reason}</span> },
    { key: "state", header: "状态", cell: (r) => <Badge variant={refundStateVariant[r.state] ?? "outline"}>{r.state}</Badge> },
  ];

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
            {KIND_TABS.map((t) => (
              <button
                key={t.key || "all"}
                onClick={() => {
                  setKind(t.key);
                  setPage(0);
                }}
                className={
                  "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
                  (kind === t.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
                }
              >
                {t.label}
              </button>
            ))}
          </div>
          <Button variant="outline" size="sm" className="gap-1.5" onClick={exportCsv}>
            <Download className="size-4" />
            导出 CSV
          </Button>
        </div>

        <ProDataTable
          data={rows}
          error={error}
          columns={ledgerCols}
          getRowKey={(r) => `l${r.id}`}
          emptyState="暂无流水"
          server={{
            total,
            page,
            pageSize: PAGE_SIZE,
            onPageChange: setPage,
            query,
            onQueryChange: (q) => {
              setQuery(q);
              setPage(0);
            },
            loading,
          }}
        />
        <p className="font-mono text-xs text-muted-foreground">搜索框按租户 ID 精确筛选 · 导出为当前筛选的全量结果</p>
      </section>

      <section className="space-y-2">
        <p className="font-mono text-xs uppercase tracking-wider text-muted-foreground">退款申请 · 仅显示最近 100 条</p>
        <ProDataTable
          data={refunds}
          error={error}
          columns={refundCols}
          getRowKey={(r) => `r${r.id}`}
          search={{ placeholder: "搜索原因…", accessor: (r) => `${r.tenant_id} ${r.reason}` }}
          emptyState="暂无退款申请"
        />
      </section>
    </div>
  );
}
```

Note: the server-mode search box maps to `tenant_id` (the backend ledger filter has no free-text field — searching by tenant ID is the meaningful axis). The placeholder is set via the table's server-mode search input, which reuses the default "搜索…" text; to customize it, the search box in server mode shows the default placeholder. This is acceptable; the helper line explains it.

- [ ] **Step 2: Verify build**

Run: `cd /var/klwa/frontend && npm run lint && npm run build`
Expected: builds.

- [ ] **Step 3: Commit**

```bash
cd /var/klwa && git add frontend/components/admin-ledger.tsx
git commit -m "feat(admin): ledger page kind tabs + server pagination + CSV export (SP4)"
```

---

## Task 13: Users page — role tabs + sales customer count

**Files:**
- Modify: `frontend/components/admin-users-table.tsx`

**Interfaces:**
- Consumes: existing `/admin/users` + `/admin/tenants`. Adds `sales_owner_id` to the `Tenant` interface for the client-side join.

- [ ] **Step 1: Add role tabs + owned-tenant count**

In `frontend/components/admin-users-table.tsx`:

Extend the `Tenant` interface:

```ts
interface Tenant {
  id: number;
  name: string;
  sales_owner_id: number | null;
}
```

Add role-tab state + a computed count map inside `AdminUsersTable` (after the `load`/`useEffect`, before the early returns):

```ts
  const [roleTab, setRoleTab] = useState<"" | Role>("");

  // tenants owned per sales user id (client-side join for the 名下客户 column).
  const ownedCount = new Map<number, number>();
  for (const t of tenants) {
    if (t.sales_owner_id != null) {
      ownedCount.set(t.sales_owner_id, (ownedCount.get(t.sales_owner_id) ?? 0) + 1);
    }
  }

  const ROLE_TABS: { key: "" | Role; label: string }[] = [
    { key: "", label: "全部" },
    { key: "admin", label: "管理员" },
    { key: "sales", label: "销售" },
    { key: "customer", label: "客户" },
  ];
```

Then filter the rendered users. Replace the `if (!users) return ...` guard's downstream `users.map(...)` source with a filtered list. Right before `return (`, add:

```ts
  const shown = roleTab ? users.filter((u) => u.role === roleTab) : users;
```

Insert the tab bar just inside the top of the returned `<div className="space-y-4">`, before the `<div className="flex justify-end">`:

```tsx
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="inline-flex flex-wrap rounded-lg border bg-muted/40 p-0.5">
          {ROLE_TABS.map((t) => (
            <button
              key={t.key || "all"}
              onClick={() => setRoleTab(t.key)}
              className={
                "rounded-md px-3 py-1.5 text-sm font-medium transition-colors " +
                (roleTab === t.key ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")
              }
            >
              {t.label}
            </button>
          ))}
        </div>
        <CreateUserDialog tenants={tenants} onDone={load} />
      </div>
```

Remove the now-duplicate standalone `<div className="flex justify-end"><CreateUserDialog .../></div>`.

Change the table body to iterate `shown` instead of `users`, and add a "名下客户" column. Update the header row:

```tsx
            <TableRow className="bg-muted/40">
              <TableHead>账号</TableHead>
              <TableHead>角色</TableHead>
              <TableHead>租户 / 名下客户</TableHead>
              <TableHead>状态</TableHead>
              <TableHead className="w-12" />
            </TableRow>
```

And the tenant cell (replace the existing `<TableCell>{tenantName(u.tenant_id)}</TableCell>`):

```tsx
                <TableCell className="text-sm text-muted-foreground">
                  {u.role === "sales"
                    ? `名下 ${ownedCount.get(u.id) ?? 0} 个租户`
                    : tenantName(u.tenant_id)}
                </TableCell>
```

- [ ] **Step 2: Verify build**

Run: `cd /var/klwa/frontend && npm run lint && npm run build`
Expected: builds.

- [ ] **Step 3: Commit**

```bash
cd /var/klwa && git add frontend/components/admin-users-table.tsx
git commit -m "feat(admin): user role tabs + sales owned-tenant count (SP4)"
```

---

## Task 14: Full verification + live smoke

**Files:** none (verification only).

- [ ] **Step 1: Backend full test**

Run: `cd /var/klwa && go build ./... && go test ./internal/api/`
Expected: all pass. If a pre-existing test broke on the `handleAdminLedger`/`handleAdminListCampaigns` envelope change, update that test's assertions (the response shape intentionally changed).

- [ ] **Step 2: Frontend full build**

Run: `cd /var/klwa/frontend && npm run lint && npm run build`
Expected: clean.

- [ ] **Step 3: Live-API smoke (invoke the `verify` skill or drive manually)**

With the console server + a seeded DB running, verify each surface and confirm audit rows:
- `/admin/campaigns`: state tabs filter; stat cards populate; row click opens recipients sheet; stop/resume work.
- `/admin/ledger`: kind tabs filter; pagination pages; 导出 CSV downloads a file; `finance.ledger_export` appears in `/admin/audit`.
- `/admin/billing`: range presets reload; tenant row → bill view; 导出 CSV downloads; `finance.bill_export` in audit.
- `/admin/users`: role tabs filter; a sales user shows "名下 N 个租户".

- [ ] **Step 4: Final commit (if smoke required fixes)**

```bash
cd /var/klwa && git add -A && git commit -m "fix(admin): SP4 smoke-test corrections"
```

---

## Self-Review Notes

**Spec coverage:** ① campaigns (Tasks 8,9,10) ✓; ② ledger filter/export (Tasks 1,2,4,5,12) ✓; ③ user role view (Task 13) ✓; ④ billing stats+bill+export (Tasks 1,3,6,7,11) ✓; nav reorg + audit cleanup (Task 9) ✓; money semantics encoded in `netExpr` + summary formulas (Tasks 6,7) ✓; CN timezone (Tasks 3,6,7) ✓; export audit (Tasks 5,7) ✓; `api.download` blob auth (Task 1) ✓.

**Deferred (per spec non-goals):** `billing_charges` message-level detail, users/tenants server pagination, suspended enforcement — none scheduled, correct.

**Known follow-ups for the implementer:**
- The ledger server-mode search box uses `ProDataTable`'s default placeholder; a custom server-mode placeholder would need a small `ServerMode.placeholder` addition — out of scope, documented inline.
- `parseLedgerFilter` and `parseBillRange` both parse `YYYY-MM-DD`; the former silently ignores bad dates (list still returns), the latter 400s (bill needs a valid window). This asymmetry is intentional.
