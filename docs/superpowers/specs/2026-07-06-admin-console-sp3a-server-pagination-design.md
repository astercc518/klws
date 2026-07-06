# SP3a — 管理后台:服务端分页/筛选(devices + proxies)

**Date:** 2026-07-06
**Status:** Approved (design)
**Scope:** First half of SP3. Moves the two high-cardinality admin lists —
节点与设备 (devices) and 代理网络池 (proxies) — from a hard `LIMIT 500` +
client-side filtering to real server-side pagination + filtering, and teaches
the shared `ProDataTable` a server-driven mode. Confined to `internal/api` +
frontend. No engine packages touched. (Users/tenants stay on client mode —
low cardinality and tangled with SP1's client-side joins; out of scope. CRUD
edit/delete is SP3b.)

## Background

`handleAdminListDevices` and `handleAdminListProxies` each `SELECT … LIMIT 500`
and return a flat array; the frontend loads everything once and filters/paginates
in memory. At the platform's scale target (~100k accounts) a 500-row device list
is effectively broken — rows beyond 500 are invisible with no indication.
`ProDataTable` only supports client-side search + pagination.

## Goals

1. `ProDataTable` gains an optional **server mode** (parent drives
   page/query/total; table renders rows as-is). Backward compatible — every
   current caller keeps client mode.
2. `GET /admin/resources/devices` and `GET /admin/resources/proxies` gain
   `limit`/`offset`/`q`/entity-filters and return `{ rows, total, stats }`.
3. `admin-devices.tsx` and `admin-proxies.tsx` switch to server mode.

## Non-goals (deferred)

- Users/tenants server-side pagination (SP-later, if ever — low cardinality).
- Edit/delete CRUD (SP3b).
- Cursor pagination — use `limit`/`offset`, consistent with `/admin/audit`.
- Proxy health-check / device force-logout (touch cluster engine; separate).

## Existing infrastructure reused

- SP2's testcontainers DB harness in `internal/api` (`testPool`,
  `applyAllMigrations`, `seedTenantUser`) — the new endpoint tests reuse it.
- The dynamic-`WHERE`-builder technique from SP2 (`buildAuditWhere`) — mirrored
  as `buildDeviceWhere`/`buildProxyWhere`. (SP2's `clampLimit` has the wrong
  default/cap shape for these lists, so SP3a adds a small `clampPage(n,def,max)`
  helper instead.)

## Component 1 — `ProDataTable` server mode

`frontend/components/admin/pro-data-table.tsx`. Add an optional prop:

```ts
server?: {
  total: number;          // filtered total, for the page count
  page: number;           // 0-based current page (controlled)
  pageSize: number;
  onPageChange: (page: number) => void;
  query: string;          // current search text (controlled)
  onQueryChange: (q: string) => void;
  loading?: boolean;      // show skeleton rows while a fetch is in flight
};
```

Behavior when `server` is provided:
- The table does **not** filter or slice locally. `data` is the current page's
  rows, rendered as-is.
- The search box is controlled by `server.query`; keystrokes are **debounced
  ~300ms** inside the table, then call `server.onQueryChange`.
- The footer uses `server.total` / `server.page` / `server.pageSize` for the
  `start–end / total` label and page count; prev/next call `server.onPageChange`.
- `server.loading` renders the existing skeleton rows over the current body.

When `server` is absent, behavior is exactly today's (client filter + slice +
`pageSize` prop). No current caller passes `server`, so ledger, audit-log,
campaigns, tenants, and users tables are unchanged.

## Component 2 — Backend paginated list endpoints

`internal/api/admin_api.go`, reusing `clampLimit`. Extract per-entity **pure**
`WHERE` builders (unit-testable like SP2's `buildAuditWhere`) into a new file
`internal/api/resources_query.go`, and change the two handlers to
`{ rows, total, stats }`.

### Devices — `GET /admin/resources/devices`
Params (all optional): `limit`, `offset`, `q` (ILIKE over `account_jid`,
`phone_number`, and `array_to_string(tags,' ')`), `ban_status` (exact:
active/banned/flagged/logged_out), `online` (`"true"` → `owner_node IS NOT
NULL`, `"false"` → `owner_node IS NULL`).

Paging is clamped by a new shared helper (SP2's `clampLimit` defaults to 100 /
caps 500, which is the wrong shape here):

```go
// clampPage returns a sane page size: def when n<=0, else n capped at max.
func clampPage(n, def, max int) int { if n <= 0 { return def }; if n > max { return max }; return n }
```

Devices use `clampPage(limit, 20, 200)`; `offset` negative → 0.

```go
type deviceFilter struct {
    Q         string
    BanStatus string
    Online    *bool
    Limit     int
    Offset    int
}
// buildDeviceWhere(f) (whereSQL string, args []any)  — pure
```

Response:
```go
gin.H{"rows": []deviceRow, "total": <filtered count>, "stats": deviceStats}
// deviceStats = { total, online, banned, flagged, logged_out } — UNFILTERED,
// for the summary cards (single grouped query over the whole table).
```
List query = existing SELECT + the WHERE + `ORDER BY id DESC LIMIT $n OFFSET $m`.
Count query = `SELECT count(*) FROM account_devices` + same WHERE. Stats query =
one `SELECT count(*) FILTER(...)`-style row over the unfiltered table.

### Proxies — `GET /admin/resources/proxies`
Params: `limit`, `offset`, `q` (ILIKE `proxy_url`, `country_code`), `alive`
(`"true"`/`"false"` → `is_alive`), `proxy_type` (exact). Same `clampPage(limit,
20, 200)`; `offset` negative → 0.

```go
type proxyFilter struct {
    Q         string
    Alive     *bool
    ProxyType string
    Limit     int
    Offset    int
}
// buildProxyWhere(f) (whereSQL string, args []any)  — pure
```

Response:
```go
gin.H{"rows": []proxyRow, "total": <filtered>, "stats": proxyStats}
// proxyStats = { total, alive, dead } — UNFILTERED.
```
`proxyRow` keeps its existing fields incl. the `bound_devices` subselect. The
existing `LIMIT 500` is removed (replaced by the paginated LIMIT/OFFSET).

## Component 3 — Frontend components switch to server mode

### `admin-proxies.tsx`
- Controlled state: `page`, `query`, `alive` filter, `proxyType` filter.
- On any of them changing (and on mount, and after an import), `api.get` with a
  querystring built from the state; store `{rows,total,stats}`.
- Feed `server={ total, page, pageSize, onPageChange, query, onQueryChange, loading }`
  to `ProDataTable`; drop the client `search` prop.
- Summary cards read `stats` (total/alive/dead) instead of computing from rows.
- Filter dropdowns (alive / type) in the toolbar drive the filter state.

### `admin-devices.tsx`
- Same controlled pattern: `page`, `query`, `banStatus`, `online` state → refetch.
- Feed `server=` to `ProDataTable`; summary cards read `stats`
  (total/online/banned/logged_out).
- The bind/unbind/import actions refetch the current page afterward.
- **Proxy bind-picker consumer:** `admin-devices.tsx` also calls
  `GET /admin/resources/proxies` to populate the bind dialog. That response is
  now `{rows,total,stats}`, so the picker must read `.rows`, and should request
  a generous page for candidate selection:
  `GET /admin/resources/proxies?alive=true&limit=200`. (200 alive candidates is
  ample for a manual bind; a dedicated picker endpoint is out of scope.)

## Error handling

- Backend: bad int params are ignored (default), same as `/admin/audit`; query
  errors → `fail(c, 500, …)`. No 400 needed for these optional filters.
- Frontend: 401 swallowed (global interceptor); other errors → `ProDataTable
  error=`. A failed refetch keeps the prior page visible (don't blank the table).

## Testing

- **Backend (Go, testcontainers via SP2 harness):**
  - Pure unit tests for `buildDeviceWhere` / `buildProxyWhere` (filter combos →
    correct WHERE + args + `$N` numbering), mirroring SP2's `TestBuildAuditWhere`.
  - DB round-trip: seed N devices/proxies across statuses; assert `limit`/`offset`
    paginate, `total` is the filtered count, each filter (`q`, `ban_status`,
    `online`, `alive`, `proxy_type`) returns the right subset, and `stats` reflect
    the unfiltered breakdown.
- **Frontend:** no JS runner — `npm run build` + `eslint` (no new error type
  beyond the accepted `react-hooks/set-state-in-effect`) + manual smoke of paging
  / search / filter on both pages.

## File-change summary

| File | Change |
|---|---|
| `frontend/components/admin/pro-data-table.tsx` | optional `server` mode (debounced search, controlled paging) |
| `internal/api/resources_query.go` | **new** — `deviceFilter`/`proxyFilter` + pure `buildDeviceWhere`/`buildProxyWhere` |
| `internal/api/admin_api.go` | devices + proxies handlers → `{rows,total,stats}` with filters; remove `LIMIT 500` |
| `internal/api/resources_query_test.go` | **new** — pure builder tests |
| `internal/api/resources_db_test.go` | **new** — DB round-trip pagination/filter tests |
| `frontend/components/admin-proxies.tsx` | server-mode controlled paging/filter; stats cards |
| `frontend/components/admin-devices.tsx` | server-mode controlled paging/filter; stats cards; proxy-picker reads `.rows` |

No engine-package files change.
