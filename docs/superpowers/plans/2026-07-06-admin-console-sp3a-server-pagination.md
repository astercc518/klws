# Admin Console SP3a — Server-side Pagination Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the devices and proxies admin lists from `LIMIT 500` + client-side filtering to real server-side pagination + filtering, and teach `ProDataTable` a server-driven mode.

**Architecture:** Two backend list handlers gain `limit/offset/q/filters` and return `{rows,total,stats}`, built with pure per-entity WHERE-builders (unit-tested) reusing SP2's technique and testcontainers harness. The shared `ProDataTable` gains an optional `server` prop (controlled paging + debounced search); the two components switch to it. Backend confined to `internal/api`; no engine packages.

**Tech Stack:** Go 1.26 (gin, pgx/v5, testcontainers postgres:16); Next.js 16.2.9 frontend, TypeScript, Tailwind.

## Global Constraints

- **No engine-package edits.** Only `internal/api/*` and `frontend/*` change.
- **Handlers must use `s.systemPool()`** (the SP2 seam), NOT `s.deps.Mgr.SystemPool()`, so DB tests can inject a pool via `&Server{sysPool: pool}`.
- **Response shape for the two endpoints becomes `{rows, total, stats}`** (was a bare array). `total` = filtered count (for the pager); `stats` = UNFILTERED breakdown (for summary cards).
- **Paging:** `limit`/`offset` (not cursors). `clampPage(limit, 20, 200)`; `offset` negative → 0.
- **Backend tests need Docker** (testcontainers). `go test ./internal/api/...`. Pure builder tests need no Docker.
- **Frontend:** no JS runner — verify via `npm run build` + `eslint` from `/var/klwa/frontend`. `npm run lint` has a repo-wide ACCEPTED `react-hooks/set-state-in-effect` error in every admin-* component; bar = no NEW error type / no unused-var in touched files (confirm via `npx eslint <file>`).
- **Customized Next.js 16.2.9** — consult `frontend/node_modules/next/dist/docs/` before Next-specific code; don't assume training-data Next.
- Work on branch `feat/admin-sp3a-server-pagination`. Backend commits: run `go build ./... && go vet ./internal/api/...` first. Commit after each task with the exact message shown.

---

### Task 1: ProDataTable server mode

Add an optional server-driven mode to the shared table. Backward compatible: no current caller passes `server`, so ledger/audit/tenants/users tables are unchanged.

**Files:**
- Modify: `frontend/components/admin/pro-data-table.tsx`

**Interfaces:**
- Produces (used by Tasks 5–6): a `server?` prop on `ProDataTable`:
  `server?: { total: number; page: number; pageSize: number; onPageChange: (page: number) => void; query: string; onQueryChange: (q: string) => void; loading?: boolean }`.

- [ ] **Step 1: Replace the component with a dual-mode version**

Replace the entire body of `frontend/components/admin/pro-data-table.tsx` with:

```tsx
"use client";

import { useEffect, useMemo, useState } from "react";
import { Search, ChevronLeft, ChevronRight } from "lucide-react";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";

export interface Column<T> {
  key: string;
  header: React.ReactNode;
  cell: (row: T) => React.ReactNode;
  align?: "left" | "right";
  headClassName?: string;
  cellClassName?: string;
}

/** Server-driven mode: the parent owns paging/search and fetches each page. */
export interface ServerMode {
  total: number;
  page: number; // 0-based
  pageSize: number;
  onPageChange: (page: number) => void;
  query: string;
  onQueryChange: (q: string) => void;
  loading?: boolean;
}

interface ProDataTableProps<T> {
  data: T[] | null;
  columns: Column<T>[];
  getRowKey: (row: T) => string | number;
  /** Client-mode search box; ignored when `server` is set. */
  search?: { placeholder?: string; accessor: (row: T) => string };
  toolbar?: React.ReactNode;
  pageSize?: number;
  emptyState?: React.ReactNode;
  error?: string | null;
  rowActions?: (row: T) => React.ReactNode;
  /** When set, the table is server-driven: no local filter/slice. */
  server?: ServerMode;
}

const alignClass = (a?: "left" | "right") => (a === "right" ? "text-right" : "text-left");

export function ProDataTable<T>({
  data,
  columns,
  getRowKey,
  search,
  toolbar,
  pageSize = 10,
  emptyState,
  error,
  rowActions,
  server,
}: ProDataTableProps<T>) {
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);

  // Server mode: debounce the search input, then notify the parent.
  const [serverInput, setServerInput] = useState(server?.query ?? "");
  useEffect(() => {
    if (!server) return;
    const id = setTimeout(() => {
      if (serverInput !== server.query) server.onQueryChange(serverInput);
    }, 300);
    return () => clearTimeout(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [serverInput]);

  const filtered = useMemo(() => {
    if (!data) return [];
    if (server) return data; // server already returns the current page
    const q = query.trim().toLowerCase();
    if (!q || !search) return data;
    return data.filter((row) => search.accessor(row).toLowerCase().includes(q));
  }, [data, query, search, server]);

  const effPageSize = server ? server.pageSize : pageSize;
  const total = server ? server.total : filtered.length;
  const pageCount = Math.max(1, Math.ceil(total / effPageSize));
  const current = server ? server.page : Math.min(page, pageCount - 1);
  const start = current * effPageSize;
  const rows = server ? filtered : filtered.slice(start, start + effPageSize);
  const colSpan = columns.length + (rowActions ? 1 : 0);
  const showSkeleton = data === null || (server?.loading ?? false);

  function changeQuery(v: string) {
    if (server) {
      setServerInput(v);
    } else {
      setQuery(v);
      setPage(0);
    }
  }
  const queryValue = server ? serverInput : query;

  function goTo(p: number) {
    if (server) server.onPageChange(p);
    else setPage(p);
  }

  return (
    <Card className="gap-0 p-0">
      {(search || toolbar) && (
        <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2.5">
          {search && (
            <div className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-lg border bg-muted/40 px-2.5 text-muted-foreground sm:max-w-xs">
              <Search className="size-3.5 shrink-0" />
              <input
                type="search"
                value={queryValue}
                onChange={(e) => changeQuery(e.target.value)}
                placeholder={search.placeholder ?? "搜索…"}
                className="min-w-0 flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground/70"
              />
            </div>
          )}
          {toolbar && <div className="ml-auto flex items-center gap-2">{toolbar}</div>}
        </div>
      )}

      <Table>
        <TableHeader>
          <TableRow className="bg-muted/30 hover:bg-muted/30">
            {columns.map((c) => (
              <TableHead
                key={c.key}
                className={cn(
                  "h-9 font-mono text-[11px] uppercase tracking-wider text-muted-foreground",
                  alignClass(c.align),
                  c.headClassName,
                )}
              >
                {c.header}
              </TableHead>
            ))}
            {rowActions && <TableHead className="w-12" />}
          </TableRow>
        </TableHeader>
        <TableBody>
          {error ? (
            <TableRow className="hover:bg-transparent">
              <TableCell colSpan={colSpan} className="py-12 text-center text-sm text-muted-foreground">
                加载失败:{error}
              </TableCell>
            </TableRow>
          ) : showSkeleton ? (
            Array.from({ length: 5 }).map((_, i) => (
              <TableRow key={i} className="hover:bg-transparent">
                <TableCell colSpan={colSpan} className="py-2">
                  <div className="h-6 animate-pulse rounded bg-muted motion-reduce:animate-none" />
                </TableCell>
              </TableRow>
            ))
          ) : rows.length === 0 ? (
            <TableRow className="hover:bg-transparent">
              <TableCell colSpan={colSpan} className="py-12 text-center text-sm text-muted-foreground">
                {queryValue.trim()
                  ? `没有匹配「${queryValue.trim()}」的结果`
                  : (emptyState ?? "暂无数据")}
              </TableCell>
            </TableRow>
          ) : (
            rows.map((row) => (
              <TableRow key={getRowKey(row)}>
                {columns.map((c) => (
                  <TableCell
                    key={c.key}
                    className={cn("py-2.5", alignClass(c.align), c.cellClassName)}
                  >
                    {c.cell(row)}
                  </TableCell>
                ))}
                {rowActions && (
                  <TableCell className="py-2.5 text-right">{rowActions(row)}</TableCell>
                )}
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>

      {!showSkeleton && !error && total > 0 && (
        <div className="flex items-center justify-between gap-3 border-t px-3 py-2.5">
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {start + 1}–{Math.min(start + effPageSize, total)} / {total}
          </span>
          {pageCount > 1 && (
            <div className="flex items-center gap-1">
              <Button
                variant="outline"
                size="icon-sm"
                aria-label="上一页"
                disabled={current === 0}
                onClick={() => goTo(current - 1)}
              >
                <ChevronLeft className="size-4" />
              </Button>
              <span className="px-1 font-mono text-xs tabular-nums text-muted-foreground">
                {current + 1} / {pageCount}
              </span>
              <Button
                variant="outline"
                size="icon-sm"
                aria-label="下一页"
                disabled={current >= pageCount - 1}
                onClick={() => goTo(current + 1)}
              >
                <ChevronRight className="size-4" />
              </Button>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}
```

- [ ] **Step 2: Build + lint**

Run (from `frontend/`): `npm run build && npx eslint components/admin/pro-data-table.tsx`
Expected: build clean; eslint clean on this file (the debounce effect only calls a prop callback, not setState-of-this-component, so `react-hooks/set-state-in-effect` should not fire; the `exhaustive-deps` disable is intentional). Existing client-mode callers (ledger, audit, tenants, users, campaigns) still compile — the build covers them.

- [ ] **Step 3: Commit**

```bash
git add frontend/components/admin/pro-data-table.tsx
git commit -m "feat(admin): ProDataTable optional server-driven mode (controlled paging + debounced search)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Backend paging helper + pure WHERE builders + tests

Pure, DB-free filter→SQL logic, unit-tested like SP2's `buildAuditWhere`.

**Files:**
- Create: `internal/api/resources_query.go`
- Test: `internal/api/resources_query_test.go`

**Interfaces:**
- Produces (used by Tasks 3–4): `func clampPage(n, def, max int) int`; `type deviceFilter struct{ Q, BanStatus string; Online *bool; Limit, Offset int }`; `func buildDeviceWhere(f deviceFilter) (string, []any)`; `type proxyFilter struct{ Q, ProxyType string; Alive *bool; Limit, Offset int }`; `func buildProxyWhere(f proxyFilter) (string, []any)`.

- [ ] **Step 1: Write failing tests**

Create `internal/api/resources_query_test.go`:

```go
package api

import (
	"strings"
	"testing"
)

func TestClampPage(t *testing.T) {
	cases := []struct{ n, def, max, want int }{
		{0, 20, 200, 20}, {-5, 20, 200, 20}, {50, 20, 200, 50}, {999, 20, 200, 200}, {200, 20, 200, 200},
	}
	for _, c := range cases {
		if got := clampPage(c.n, c.def, c.max); got != c.want {
			t.Errorf("clampPage(%d,%d,%d)=%d want %d", c.n, c.def, c.max, got, c.want)
		}
	}
}

func TestBuildDeviceWhere(t *testing.T) {
	// empty
	if w, a := buildDeviceWhere(deviceFilter{}); w != "" || len(a) != 0 {
		t.Errorf("empty: w=%q a=%v", w, a)
	}
	// q hits three columns with one param
	w, a := buildDeviceWhere(deviceFilter{Q: "abc"})
	if !strings.Contains(w, "account_jid ILIKE $1") || !strings.Contains(w, "phone_number ILIKE $1") || !strings.Contains(w, "array_to_string(tags,' ') ILIKE $1") {
		t.Errorf("q where = %q", w)
	}
	if len(a) != 1 || a[0] != "%abc%" {
		t.Errorf("q args = %v", a)
	}
	// combined: q + ban_status + online=true; online adds no arg
	on := true
	w, a = buildDeviceWhere(deviceFilter{Q: "x", BanStatus: "banned", Online: &on})
	if !strings.Contains(w, "ban_status::text = $2") || !strings.Contains(w, "owner_node IS NOT NULL") {
		t.Errorf("combined where = %q", w)
	}
	if len(a) != 2 || a[1] != "banned" {
		t.Errorf("combined args = %v", a)
	}
	// online=false → IS NULL
	off := false
	w, _ = buildDeviceWhere(deviceFilter{Online: &off})
	if !strings.Contains(w, "owner_node IS NULL") {
		t.Errorf("offline where = %q", w)
	}
}

func TestBuildProxyWhere(t *testing.T) {
	if w, a := buildProxyWhere(proxyFilter{}); w != "" || len(a) != 0 {
		t.Errorf("empty: w=%q a=%v", w, a)
	}
	alive := true
	w, a := buildProxyWhere(proxyFilter{Q: "us", Alive: &alive, ProxyType: "socks5"})
	if !strings.Contains(w, "p.proxy_url ILIKE $1") || !strings.Contains(w, "p.is_alive = $2") || !strings.Contains(w, "p.proxy_type::text = $3") {
		t.Errorf("where = %q", w)
	}
	if len(a) != 3 || a[0] != "%us%" || a[1] != true || a[2] != "socks5" {
		t.Errorf("args = %v", a)
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/api/ -run 'TestClampPage|TestBuildDeviceWhere|TestBuildProxyWhere'`
Expected: FAIL — undefined `clampPage`/`deviceFilter`/`buildDeviceWhere`/etc.

- [ ] **Step 3: Implement `internal/api/resources_query.go`**

```go
package api

import (
	"fmt"
	"strings"
)

// clampPage returns a sane page size: def when n<=0, else n capped at max.
func clampPage(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// --- devices ---

type deviceFilter struct {
	Q         string
	BanStatus string
	Online    *bool
	Limit     int
	Offset    int
}

// buildDeviceWhere returns the " WHERE ..." clause (or "") and positional args
// for account_devices (unqualified columns).
func buildDeviceWhere(f deviceFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Q != "" {
		args = append(args, "%"+f.Q+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf("(account_jid ILIKE $%d OR phone_number ILIKE $%d OR array_to_string(tags,' ') ILIKE $%d)", n, n, n))
	}
	if f.BanStatus != "" {
		args = append(args, f.BanStatus)
		conds = append(conds, fmt.Sprintf("ban_status::text = $%d", len(args)))
	}
	if f.Online != nil {
		if *f.Online {
			conds = append(conds, "owner_node IS NOT NULL")
		} else {
			conds = append(conds, "owner_node IS NULL")
		}
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// --- proxies ---

type proxyFilter struct {
	Q         string
	ProxyType string
	Alive     *bool
	Limit     int
	Offset    int
}

// buildProxyWhere returns the " WHERE ..." clause (or "") and args for
// proxy_pool (aliased "p").
func buildProxyWhere(f proxyFilter) (string, []any) {
	var conds []string
	var args []any
	if f.Q != "" {
		args = append(args, "%"+f.Q+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf("(p.proxy_url ILIKE $%d OR p.country_code ILIKE $%d)", n, n))
	}
	if f.Alive != nil {
		args = append(args, *f.Alive)
		conds = append(conds, fmt.Sprintf("p.is_alive = $%d", len(args)))
	}
	if f.ProxyType != "" {
		args = append(args, f.ProxyType)
		conds = append(conds, fmt.Sprintf("p.proxy_type::text = $%d", len(args)))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
```

- [ ] **Step 4: Run to verify pass + build**

Run: `go test ./internal/api/ -run 'TestClampPage|TestBuildDeviceWhere|TestBuildProxyWhere' && go build ./...`
Expected: PASS, clean.

- [ ] **Step 5: Commit**

```bash
git add internal/api/resources_query.go internal/api/resources_query_test.go
git commit -m "feat(api): pure device/proxy filter builders + clampPage

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Devices endpoint → paginated {rows,total,stats} + DB test

**Files:**
- Modify: `internal/api/admin_api.go` (`handleAdminListDevices`, add `deviceStats`, `parseDeviceFilter`)
- Test: `internal/api/resources_db_test.go` (new)

**Interfaces:**
- Consumes: `buildDeviceWhere`/`clampPage` (Task 2), `deviceRow` (existing), `testPool`/`seedTenantUser` (SP2 harness), `s.systemPool()`.
- Produces (used by Task 6): `GET /admin/resources/devices` → `{ rows: deviceRow[], total: number, stats: {total,online,banned,flagged,logged_out} }`.

- [ ] **Step 1: Write the failing DB test**

Create `internal/api/resources_db_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// seedDevice inserts one account_devices row.
func seedDevice(t *testing.T, ctx context.Context, s *Server, tenantID int64, jid, phone, banStatus, ownerNode string) {
	t.Helper()
	var on any
	if ownerNode != "" {
		on = ownerNode
	}
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status, owner_node) VALUES ($1,$2,$3,$4::ban_status_t,$5)`,
		tenantID, jid, phone, banStatus, on); err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

func TestHandleAdminListDevices_pagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	s := &Server{sysPool: testPool(t)}
	tid, _ := seedTenantUser(t, ctx, s, "dev-admin@acme.test")

	// 3 active(online), 2 banned(offline), 1 logged_out(offline)
	for i := 0; i < 3; i++ {
		seedDevice(t, ctx, s, tid, fmt.Sprintf("a%d@wa", i), fmt.Sprintf("100%d", i), "active", "node-1")
	}
	for i := 0; i < 2; i++ {
		seedDevice(t, ctx, s, tid, fmt.Sprintf("b%d@wa", i), fmt.Sprintf("200%d", i), "banned", "")
	}
	seedDevice(t, ctx, s, tid, "c0@wa", "3000", "logged_out", "")

	call := func(q string) map[string]any {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/resources/devices"+q, nil)
		s.handleAdminListDevices(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d body %s", w.Code, w.Body.String())
		}
		var env struct{ Data map[string]any `json:"data"` }
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return env.Data
	}

	// no filter: total 6; stats reflect full breakdown
	all := call("")
	if all["total"].(float64) != 6 {
		t.Errorf("total = %v want 6", all["total"])
	}
	stats := all["stats"].(map[string]any)
	if stats["total"].(float64) != 6 || stats["online"].(float64) != 3 || stats["banned"].(float64) != 2 || stats["logged_out"].(float64) != 1 {
		t.Errorf("stats = %v", stats)
	}
	// ban_status filter
	if call("?ban_status=banned")["total"].(float64) != 2 {
		t.Errorf("banned filter wrong")
	}
	// online filter
	if call("?online=true")["total"].(float64) != 3 {
		t.Errorf("online filter wrong")
	}
	if call("?online=false")["total"].(float64) != 3 {
		t.Errorf("offline filter wrong")
	}
	// q filter (jid prefix a)
	if call("?q=a0@wa")["total"].(float64) != 1 {
		t.Errorf("q filter wrong")
	}
	// pagination: limit 2 → 2 rows, total still 6
	pg := call("?limit=2")
	if len(pg["rows"].([]any)) != 2 || pg["total"].(float64) != 6 {
		t.Errorf("limit paging: rows=%d total=%v", len(pg["rows"].([]any)), pg["total"])
	}
	// stats are UNFILTERED even under a filter
	filteredStats := call("?ban_status=banned")["stats"].(map[string]any)
	if filteredStats["total"].(float64) != 6 {
		t.Errorf("stats must be unfiltered, got %v", filteredStats["total"])
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/api/ -run TestHandleAdminListDevices_pagination`
Expected: FAIL (handler still returns a bare array; `all["total"]` nil-asserts / panics).

- [ ] **Step 3: Rewrite `handleAdminListDevices` + add `deviceStats`/`parseDeviceFilter`**

Add near the `deviceRow` type in `admin_api.go`:

```go
type deviceStats struct {
	Total     int64 `json:"total"`
	Online    int64 `json:"online"`
	Banned    int64 `json:"banned"`
	Flagged   int64 `json:"flagged"`
	LoggedOut int64 `json:"logged_out"`
}

func parseDeviceFilter(c *gin.Context) deviceFilter {
	f := deviceFilter{Q: c.Query("q"), BanStatus: c.Query("ban_status")}
	if v := c.Query("online"); v == "true" {
		b := true
		f.Online = &b
	} else if v == "false" {
		b := false
		f.Online = &b
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

Replace the whole `handleAdminListDevices` body with:

```go
func (s *Server) handleAdminListDevices(c *gin.Context) {
	ctx := c.Request.Context()
	f := parseDeviceFilter(c)
	where, args := buildDeviceWhere(f)

	listArgs := append(append([]any{}, args...), clampPage(f.Limit, 20, 200), f.Offset)
	list := `
SELECT id, tenant_id, account_jid, phone_number, ban_status::text, owner_node, last_connected_at::text,
       tags, proxy_id, proxy_url_cache
  FROM account_devices` + where +
		fmt.Sprintf(" ORDER BY id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := s.systemPool().Query(ctx, list, listArgs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list devices")
		return
	}
	defer rows.Close()
	out := make([]deviceRow, 0)
	for rows.Next() {
		var d deviceRow
		if err := rows.Scan(&d.ID, &d.TenantID, &d.AccountJID, &d.Phone, &d.BanStatus, &d.OwnerNode, &d.LastConnectedAt,
			&d.Tags, &d.ProxyID, &d.ProxyURL); err != nil {
			fail(c, http.StatusInternalServerError, "scan device")
			return
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate devices")
		return
	}

	var total int64
	if err := s.systemPool().QueryRow(ctx, `SELECT count(*) FROM account_devices`+where, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count devices")
		return
	}

	var st deviceStats
	if err := s.systemPool().QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE owner_node IS NOT NULL),
       count(*) FILTER (WHERE ban_status = 'banned'),
       count(*) FILTER (WHERE ban_status = 'flagged'),
       count(*) FILTER (WHERE ban_status = 'logged_out')
  FROM account_devices`).Scan(&st.Total, &st.Online, &st.Banned, &st.Flagged, &st.LoggedOut); err != nil {
		fail(c, http.StatusInternalServerError, "device stats")
		return
	}

	ok(c, gin.H{"rows": out, "total": total, "stats": st})
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./internal/api/ -run TestHandleAdminListDevices_pagination -v`
Expected: PASS.

- [ ] **Step 5: Build/vet + commit**

Run: `go build ./... && go vet ./internal/api/...`

```bash
git add internal/api/admin_api.go internal/api/resources_db_test.go
git commit -m "feat(api): devices list server-side paginated {rows,total,stats} + filters

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Proxies endpoint → paginated {rows,total,stats} + DB test

**Files:**
- Modify: `internal/api/admin_api.go` (`handleAdminListProxies`, add `proxyStats`, `parseProxyFilter`)
- Test: `internal/api/resources_db_test.go` (append)

**Interfaces:**
- Consumes: `buildProxyWhere`/`clampPage` (Task 2), `proxyRow` (existing), `testPool` (SP2), `s.systemPool()`.
- Produces (used by Task 5 + the device proxy-picker): `GET /admin/resources/proxies` → `{ rows: proxyRow[], total, stats: {total,alive,dead} }`.

- [ ] **Step 1: Write the failing DB test (append to `resources_db_test.go`)**

```go
func seedProxy(t *testing.T, ctx context.Context, s *Server, url, country, ptype string, alive bool) {
	t.Helper()
	if _, err := s.systemPool().Exec(ctx,
		`INSERT INTO proxy_pool (proxy_url, country_code, proxy_type, is_alive) VALUES ($1,$2,$3::proxy_type_t,$4)`,
		url, country, ptype, alive); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
}

func TestHandleAdminListProxies_pagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	s := &Server{sysPool: testPool(t)}

	seedProxy(t, ctx, s, "socks5://1.1.1.1:1080", "US", "socks5", true)
	seedProxy(t, ctx, s, "socks5://2.2.2.2:1080", "US", "socks5", true)
	seedProxy(t, ctx, s, "http://3.3.3.3:8080", "DE", "http", false)

	call := func(q string) map[string]any {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/resources/proxies"+q, nil)
		s.handleAdminListProxies(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d body %s", w.Code, w.Body.String())
		}
		var env struct{ Data map[string]any `json:"data"` }
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return env.Data
	}

	all := call("")
	if all["total"].(float64) != 3 {
		t.Errorf("total = %v want 3", all["total"])
	}
	stats := all["stats"].(map[string]any)
	if stats["total"].(float64) != 3 || stats["alive"].(float64) != 2 || stats["dead"].(float64) != 1 {
		t.Errorf("stats = %v", stats)
	}
	if call("?alive=true")["total"].(float64) != 2 {
		t.Errorf("alive filter wrong")
	}
	if call("?proxy_type=http")["total"].(float64) != 1 {
		t.Errorf("type filter wrong")
	}
	if call("?q=DE")["total"].(float64) != 1 {
		t.Errorf("q filter wrong")
	}
	pg := call("?limit=1")
	if len(pg["rows"].([]any)) != 1 || pg["total"].(float64) != 3 {
		t.Errorf("limit paging wrong")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./internal/api/ -run TestHandleAdminListProxies_pagination`
Expected: FAIL (handler returns a bare array).

- [ ] **Step 3: Rewrite `handleAdminListProxies` + add `proxyStats`/`parseProxyFilter`**

Add near `proxyRow`:

```go
type proxyStats struct {
	Total int64 `json:"total"`
	Alive int64 `json:"alive"`
	Dead  int64 `json:"dead"`
}

func parseProxyFilter(c *gin.Context) proxyFilter {
	f := proxyFilter{Q: c.Query("q"), ProxyType: c.Query("proxy_type")}
	if v := c.Query("alive"); v == "true" {
		b := true
		f.Alive = &b
	} else if v == "false" {
		b := false
		f.Alive = &b
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

Replace the whole `handleAdminListProxies` body with:

```go
func (s *Server) handleAdminListProxies(c *gin.Context) {
	ctx := c.Request.Context()
	f := parseProxyFilter(c)
	where, args := buildProxyWhere(f)

	listArgs := append(append([]any{}, args...), clampPage(f.Limit, 20, 200), f.Offset)
	list := `
SELECT p.id, p.proxy_url, p.proxy_type::text, p.country_code, p.is_alive,
       p.current_bindings, p.max_bindings, p.failure_count,
       (SELECT count(*)::int FROM account_devices a WHERE a.proxy_id = p.id) AS bound_devices
  FROM proxy_pool p` + where +
		fmt.Sprintf(" ORDER BY p.id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := s.systemPool().Query(ctx, list, listArgs...)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list proxies")
		return
	}
	defer rows.Close()
	out := make([]proxyRow, 0)
	for rows.Next() {
		var p proxyRow
		if err := rows.Scan(&p.ID, &p.URL, &p.Type, &p.Country, &p.IsAlive,
			&p.CurrentBindings, &p.MaxBindings, &p.FailureCount, &p.BoundDevices); err != nil {
			fail(c, http.StatusInternalServerError, "scan proxy")
			return
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate proxies")
		return
	}

	var total int64
	if err := s.systemPool().QueryRow(ctx, `SELECT count(*) FROM proxy_pool p`+where, args...).Scan(&total); err != nil {
		fail(c, http.StatusInternalServerError, "count proxies")
		return
	}

	var st proxyStats
	if err := s.systemPool().QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE is_alive), count(*) FILTER (WHERE NOT is_alive)
  FROM proxy_pool`).Scan(&st.Total, &st.Alive, &st.Dead); err != nil {
		fail(c, http.StatusInternalServerError, "proxy stats")
		return
	}

	ok(c, gin.H{"rows": out, "total": total, "stats": st})
}
```

Note: the existing `handleAdminListProxies` scans into `proxyRow` including `BoundDevices`; keep that field. Confirm the current struct field order matches the Scan (it does in the existing code).

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./internal/api/ -run TestHandleAdminListProxies_pagination -v`
Expected: PASS.

- [ ] **Step 5: Build/vet + full api test + commit**

Run: `go build ./... && go vet ./internal/api/... && go test ./internal/api/ -run 'Proxies|Devices|BuildDevice|BuildProxy|ClampPage'`

```bash
git add internal/api/admin_api.go internal/api/resources_db_test.go
git commit -m "feat(api): proxies list server-side paginated {rows,total,stats} + filters

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: admin-proxies.tsx → server mode

**Files:**
- Modify: `frontend/components/admin-proxies.tsx`

**Interfaces:**
- Consumes: `ProDataTable` `server` prop (Task 1); `GET /admin/resources/proxies` → `{rows,total,stats}` (Task 4).

- [ ] **Step 1: Convert `AdminProxies` to controlled server state**

Replace the `AdminProxies` function (the exported component, NOT the `ImportProxiesDialog` below it) with this. Keep `parseProxies`, `ImportProxiesDialog`, and all imports; add `useCallback` is already imported; the columns array is unchanged so keep it.

```tsx
const PAGE_SIZE = 20;

interface ProxyStats { total: number; alive: number; dead: number }

export function AdminProxies() {
  const [rows, setRows] = useState<Proxy[] | null>(null);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<ProxyStats | null>(null);
  const [page, setPage] = useState(0);
  const [query, setQuery] = useState("");
  const [alive, setAlive] = useState<"all" | "true" | "false">("all");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    if (query.trim()) params.set("q", query.trim());
    if (alive !== "all") params.set("alive", alive);
    try {
      const d = await api.get<{ rows: Proxy[]; total: number; stats: ProxyStats }>(
        `/admin/resources/proxies?${params.toString()}`,
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
  }, [page, query, alive]);

  useEffect(() => {
    load();
  }, [load]);

  // columns: keep the existing `columns` array unchanged (copy it here as-is).

  return (
    <>
      {stats && (
        <MetricCardGroup className="mb-6">
          <StatCard accent="brand" label="代理总数" value={String(stats.total)} sub="全平台出口" icon={Globe} />
          <StatCard accent="emerald" label="在线" value={String(stats.alive)} sub="健康可调度" icon={Wifi} />
          <StatCard accent="rose" label="离线 / 失效" value={String(stats.dead)} sub="需排查或剔除" icon={WifiOff} />
          <StatCard accent="amber" label="当前页" value={String(total)} sub="匹配筛选的总数" icon={Activity} />
        </MetricCardGroup>
      )}
      <ProDataTable
        data={rows}
        error={error}
        columns={columns}
        getRowKey={(p) => p.id}
        emptyState="代理池为空,点击右上角批量导入。"
        server={{
          total,
          page,
          pageSize: PAGE_SIZE,
          onPageChange: setPage,
          query,
          onQueryChange: (q) => { setQuery(q); setPage(0); },
          loading,
        }}
        search={{ placeholder: "搜索地址或国家…", accessor: () => "" }}
        toolbar={
          <div className="flex items-center gap-2">
            <select
              value={alive}
              onChange={(e) => { setAlive(e.target.value as "all" | "true" | "false"); setPage(0); }}
              className="h-8 rounded-md border bg-transparent px-2 text-sm"
              aria-label="按状态筛选"
            >
              <option value="all">全部状态</option>
              <option value="true">在线</option>
              <option value="false">失效</option>
            </select>
            <ImportProxiesDialog onDone={load} />
          </div>
        }
      />
    </>
  );
}
```

Notes for the implementer: (a) move the existing `columns` array definition inside the new `AdminProxies` (unchanged), replacing the `// columns: keep...` comment; (b) `search.accessor` is unused in server mode (the table doesn't filter locally) but the `search` prop is what renders the search box — passing a no-op accessor is fine; (c) `useMemo` import may become unused after removing the client `summary`/columns memo — if so, drop it from the import to avoid a lint error.

- [ ] **Step 2: Build + lint**

Run (from `frontend/`): `npm run build && npx eslint components/admin-proxies.tsx`
Expected: build clean; eslint shows at most the accepted `react-hooks/set-state-in-effect` on the load effect, no unused-var (remove `useMemo` if it went unused).

- [ ] **Step 3: Commit**

```bash
git add frontend/components/admin-proxies.tsx
git commit -m "feat(admin): proxies page server-side paging + filters + stats cards

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: admin-devices.tsx → server mode + proxy picker reads .rows

**Files:**
- Modify: `frontend/components/admin-devices.tsx`

**Interfaces:**
- Consumes: `ProDataTable` `server` prop (Task 1); `GET /admin/resources/devices` → `{rows,total,stats}` (Task 3); `GET /admin/resources/proxies` → `{rows,...}` (Task 4).

- [ ] **Step 1: Read the file first**

Read `frontend/components/admin-devices.tsx` in full (≈540 lines). Identify: the top-level `AdminDevices` component's data-load (`load`/`refresh` calling `GET /admin/resources/devices`), the client-side `summary` memo, the client-side search on `ProDataTable`, and the proxy-picker fetch (`GET /admin/resources/proxies`, in the bind dialog / `loadProxies`).

- [ ] **Step 2: Convert `AdminDevices` list to server mode**

Apply the same controlled-state pattern as Task 5's `admin-proxies.tsx`:
- Add state: `total`, `stats`, `page`, `query`, `banStatus` (`"all" | "active" | "banned" | "flagged" | "logged_out"`), `online` (`"all" | "true" | "false"`), `loading`.
- Rewrite `load` to fetch `GET /admin/resources/devices?` with `URLSearchParams` (`limit=20`, `offset=page*20`, and `q`/`ban_status`/`online` when set), and store `d.rows`/`d.total`/`d.stats`.
- Its `useEffect(() => { load() }, [load])` re-runs on page/query/filter change; bind/unbind/import still call `load()` afterward to refresh the current page.
- Replace the client `summary` (computed from all rows) with `stats` from the response. Device stats shape: `{ total, online, banned, flagged, logged_out }` — wire the four summary cards to these (total / online / banned+flagged as "风控标记" / logged_out) matching the current card set as closely as possible.
- Replace the `ProDataTable` `search={...}` client prop with the `server={{ total, page, pageSize: 20, onPageChange: setPage, query, onQueryChange: q => { setQuery(q); setPage(0) }, loading }}` prop plus a no-op `search` (to keep the search box) and toolbar `<select>`s for ban_status + online (each `setPage(0)` on change), keeping the existing import/connect toolbar buttons.

- [ ] **Step 3: Fix the proxy bind-picker to read `.rows`**

The bind dialog fetches `GET /admin/resources/proxies` and expects an array. Change that fetch to request candidates and read `.rows`:

```tsx
const d = await api.get<{ rows: Proxy[] }>("/admin/resources/proxies?alive=true&limit=200");
// use d.rows where the old code used the array
```

(Adjust the local `Proxy`/proxy type name to whatever the file already uses. The picker's downstream filtering — alive + under-capacity + keep-current — stays; it now operates on `d.rows`.)

- [ ] **Step 4: Build + lint**

Run (from `frontend/`): `npm run build && npx eslint components/admin-devices.tsx`
Expected: build clean; eslint shows at most the accepted `react-hooks/set-state-in-effect`, no new error type, no unused vars (drop any import that became unused, e.g. `useMemo` if the client summary was removed).

- [ ] **Step 5: Manual smoke (if a live API is available; else note it)**

`npm run dev`, `/admin/devices` and `/admin/resources`: paging changes the page and refetches; the search box (debounced) filters server-side; the ban_status/online (devices) and alive/type (proxies) selects filter; summary cards read stats; binding a proxy still works (picker populated from `.rows`). If no live API, state the gap in the report.

- [ ] **Step 6: Commit**

```bash
git add frontend/components/admin-devices.tsx
git commit -m "feat(admin): devices page server-side paging + filters + stats; proxy picker reads .rows

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review (completed by author)

**Spec coverage:**
- ProDataTable server mode → Task 1. ✓
- `clampPage` + pure `buildDeviceWhere`/`buildProxyWhere` → Task 2. ✓
- Devices endpoint `{rows,total,stats}` + filters, `LIMIT 500` removed → Task 3. ✓
- Proxies endpoint `{rows,total,stats}` + filters, `LIMIT 500` removed → Task 4. ✓
- admin-proxies server mode + stats cards → Task 5. ✓
- admin-devices server mode + stats + proxy-picker `.rows` → Task 6. ✓
- Handlers use `s.systemPool()` (test seam) → Tasks 3/4. ✓
- Testing hybrid (pure builder unit + DB round-trip) → Tasks 2/3/4; frontend build/lint → 1/5/6. ✓
- Non-goals (users/tenants paging, CRUD, cursors) absent. ✓

**Placeholder scan:** none — full code in every backend step. Task 6 gives targeted edits + directs reading the 540-line file (reproducing it verbatim would be error-prone); each change is fully specified with code. Task 5 keeps the existing `columns` array (explicitly "copy as-is") rather than reprinting it.

**Type consistency:** `deviceFilter`/`proxyFilter` fields, `buildDeviceWhere`/`buildProxyWhere` signatures, `clampPage(limit,20,200)`, `deviceStats`/`proxyStats` JSON shapes, and the `{rows,total,stats}` response are consistent across Tasks 2→6. `ProDataTable` `server` prop shape (Task 1) matches the object passed in Tasks 5/6. Handlers use `s.systemPool()` so the `&Server{sysPool: pool}` test harness (SP2) drives them.
