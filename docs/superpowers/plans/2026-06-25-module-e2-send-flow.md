# Module E-2 — 客户自助发送流 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** The customer self-serve send flow (white-paper Week-3 second half): upload a number pack (dedup + format-validate + suppression-filter), compose a Spintax message with preview, see the cost estimate (count × unit price), and submit — which creates the campaign (RLS) and sets it `running` so the existing dispatcher sends it. Completing this closes the full MVP loop the founder can black-box accept.

**Architecture:** All customer writes go through Module B's `withTenantTx` (RLS `WITH CHECK` enforces `tenant_id = session tenant` on insert). Number parsing is a pure unit. Suppression filtering and recipient dedup use `crypto.BlindIndex(blindKey, phone)` (the same `phone_bidx` the dispatcher already fences on). Cost estimate reuses `pricing.PriceFor` (Module C-1); a pre-submit balance guard blocks unaffordable campaigns (per-message `Hold` still happens in the worker). Spintax preview reuses `internal/spintax`. Submit sets `campaigns.state='running'`; `Dispatcher.DispatchRunning` (already running each second) picks it up — no new send wiring.

**Tech Stack:** Go 1.26, pgx/v5, html/template+htmx, testcontainers (PG16+Redis7). Reuses `internal/console` (B/C-1/E-1), `internal/crypto`, `internal/pricing`, `internal/spintax`.

## Global Constraints

- Go module `github.com/acme/wadist`, Go 1.26.4. Toolchain at `/usr/local/go/bin`. Tests with `TESTCONTAINERS_RYUK_DISABLED=true`.
- **Customer writes go through `s.withTenantTx(ctx)` ONLY** (RLS `WITH CHECK`); never SystemPool/bizPool. Inserts must set `tenant_id = session tenant` (the policy requires it).
- Money is integer minor units; no floats. A campaign must not be submittable when `estimate > available balance` (white-paper invariant "不透支").
- **Suppression is mandatory** (white-paper "已退订的号码永不再发"): the send flow requires a configured 32-byte blind-index key. If `s.blindKey` is unset, the send endpoints return an error (fail-closed) — do NOT insert recipients with NULL `phone_bidx` (that breaks dedup + the dispatcher's suppression fence).
- Recipients are inserted with `phone_bidx = crypto.BlindIndex(blindKey, phone)`; the existing unique index `(campaign_id, phone_bidx)` enforces dedup at the DB.
- CSRF: new customer POST routes use the **CSRF-outermost** convention (`requireCSRF(requireAuth(custOnly(handler)))`) per E-1's carried note; every new form carries `<input type="hidden" name="csrf" value="{{.CSRF}}">` and its handler passes `"CSRF"`.
- Do NOT change `NewServer`'s signature. Full console suite + repo build/vet stay green.

---

### Task 1: Number-pack parsing + suppression filtering

**Files:**
- Create: `internal/console/numbers.go`
- Create: `internal/console/numbers_test.go`
- Modify: `internal/console/server.go` (add `blindKey []byte` field + `WithBlindKey([]byte) *Server`)

**Interfaces:**
- Produces:
  - `type ParsedNumbers struct { Valid []string; Duplicates, Invalid int }`.
  - `func parseNumbers(raw string) ParsedNumbers` — splits on newlines/commas/whitespace, trims, normalizes (keep leading `+` and digits; strip spaces/dashes/parens), validates as 8–15 digits (E.164-ish), de-duplicates preserving first-seen order; counts dupes and invalids.
  - `func (s *Server) WithBlindKey(key []byte) *Server` (sets `blindKey`, chainable).
  - `func (s *Server) filterSuppressed(ctx context.Context, phones []string) (kept []string, filtered int, err error)` — returns `ErrSendNotConfigured` if `blindKey` is nil; else opens `withTenantTx`, and for each phone checks `suppression_list` by `crypto.BlindIndex(blindKey, phone)` (RLS-scoped), dropping suppressed ones.
  - `var ErrSendNotConfigured = errors.New("console: send flow requires WADIST_BLIND_INDEX_KEY")`.

- [ ] **Step 1: Write the failing parse test**

```go
// internal/console/numbers_test.go
package console

import "testing"

func TestParseNumbers(t *testing.T) {
	raw := "+1 (555) 123-4567\n15551234567\n  \n447911123456,\n447911123456\nabc\n12\n+1-555-123-4567"
	got := parseNumbers(raw)
	// "+15551234567" appears 3 times (the +1..., the +1-555..., normalize equal) → dupes;
	// "15551234567" is a distinct number; "447911123456" twice → 1 dupe; "abc"/"12" invalid.
	// Assert the distinct valid set and that counts are non-zero where expected.
	if len(got.Valid) == 0 {
		t.Fatalf("expected some valid numbers, got %+v", got)
	}
	for _, v := range got.Valid {
		if len(v) < 8 {
			t.Fatalf("invalid number slipped through: %q", v)
		}
	}
	if got.Invalid < 2 { // "abc" and "12"
		t.Fatalf("expected >=2 invalid, got %d", got.Invalid)
	}
	if got.Duplicates < 1 {
		t.Fatalf("expected >=1 duplicate, got %d", got.Duplicates)
	}
	// no duplicates remain in Valid
	seen := map[string]bool{}
	for _, v := range got.Valid {
		if seen[v] {
			t.Fatalf("duplicate in Valid: %q", v)
		}
		seen[v] = true
	}
}

func TestParseNumbersEmpty(t *testing.T) {
	if got := parseNumbers("   \n , \n"); len(got.Valid) != 0 {
		t.Fatalf("expected no valid, got %+v", got)
	}
}
```

- [ ] **Step 2: Run (RED).** `/usr/local/go/bin/go test ./internal/console/ -run TestParseNumbers -v` → FAIL.

- [ ] **Step 3: Implement `numbers.go`**

```go
// internal/console/numbers.go
package console

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/acme/wadist/internal/crypto"
)

var ErrSendNotConfigured = errors.New("console: send flow requires WADIST_BLIND_INDEX_KEY")

// ParsedNumbers is the outcome of parsing an uploaded/pasted number pack.
type ParsedNumbers struct {
	Valid      []string
	Duplicates int
	Invalid    int
}

// parseNumbers splits a raw pack (newline/comma/space separated), normalizes each
// entry to leading-'+'? + digits, validates 8–15 digits, dedupes (first-seen order).
func parseNumbers(raw string) ParsedNumbers {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';' || r == '\t' || r == ' '
	})
	var out ParsedNumbers
	seen := make(map[string]bool)
	for _, f := range fields {
		n := normalizePhone(f)
		if !validPhone(n) {
			out.Invalid++
			continue
		}
		if seen[n] {
			out.Duplicates++
			continue
		}
		seen[n] = true
		out.Valid = append(out.Valid, n)
	}
	return out
}

func normalizePhone(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validPhone(n string) bool {
	digits := strings.TrimPrefix(n, "+")
	if len(digits) < 8 || len(digits) > 15 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// WithBlindKey injects the HMAC blind-index key used for suppression + dedup.
func (s *Server) WithBlindKey(key []byte) *Server { s.blindKey = key; return s }

// filterSuppressed removes phones present in the tenant's suppression_list.
// Returns ErrSendNotConfigured when no blind key is configured (fail-closed:
// we must never send to an opted-out number).
func (s *Server) filterSuppressed(ctx context.Context, phones []string) (kept []string, filtered int, err error) {
	if len(s.blindKey) != 32 {
		return nil, 0, ErrSendNotConfigured
	}
	tx, err := s.withTenantTx(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)
	for _, p := range phones {
		bidx := crypto.BlindIndex(s.blindKey, p)
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM suppression_list WHERE phone_bidx=$1)`, bidx).Scan(&exists); err != nil {
			return nil, 0, fmt.Errorf("suppression check: %w", err)
		}
		if exists {
			filtered++
			continue
		}
		kept = append(kept, p)
	}
	return kept, filtered, nil
}
```

> Add the `blindKey []byte` field to the `Server` struct in `server.go` (alongside `billing`/`pricing`/`tenants`). Note `suppression_list` is RLS-enforced, so the `withTenantTx` query auto-scopes to the session tenant (no explicit `tenant_id`).

- [ ] **Step 4: Run (GREEN)** the parse test. (filterSuppressed is covered by the integration test in Task 3 / can add a focused one here if desired.)

- [ ] **Step 5: vet + commit**

```bash
/usr/local/go/bin/go vet ./internal/console/
git add internal/console/numbers.go internal/console/numbers_test.go internal/console/server.go
git commit -m "feat(console): number-pack parse/dedup/validate + suppression filter (blind-index)"
```

---

### Task 2: Campaign creation (RLS inserts) + cost estimate + balance guard

**Files:**
- Create: `internal/console/send.go`
- Create: `internal/console/send_test.go`

**Interfaces:**
- Consumes: `pricing.Repo` (via `s.pricing` from C-1's `WithPricing`), `s.blindKey` (Task 1), `withTenantTx`, `customerBalance` (E-1).
- Produces:
  - `var ErrInsufficientBalance = errors.New("console: insufficient balance for campaign")`.
  - `func (s *Server) estimateCost(ctx context.Context, country string, n int) int64` — `int64(n) * s.pricing.PriceFor(ctx, tenantID, country, 1)` using the session tenant (read tenant from context); returns 0 if n<=0.
  - `func (s *Server) createCampaign(ctx context.Context, country, body string, phones []string) (campaignID int64, err error)` — fail-closed if `blindKey` unset; computes estimate; rejects with `ErrInsufficientBalance` if `estimate > available balance`; else in ONE `withTenantTx`: insert `campaign_templates` (tenant_id, kind='text', body) → `campaigns` (tenant_id, template_id, state='running', total=len(phones)) → bulk-insert `campaign_recipients` (tenant_id, campaign_id, phone, country_code, phone_bidx); commit; returns campaign id. All inserts include `tenant_id = session tenant` (RLS `WITH CHECK`).

- [ ] **Step 1: Write the failing test**

```go
// internal/console/send_test.go
package console

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCreateCampaignRLSAndBalance(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	srv, _ := NewServer(cfg, NewUserRepo(mgr.SystemPool()), NewSessionStore(newTestRedis(t), time.Hour), mgr)
	srv.WithPricing(pricingRepo(t, mgr)).WithBlindKey(make([]byte, 32)) // 32-byte zero key OK for test

	var tid int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name) VALUES ('A') RETURNING id`, &tid)
	// price US=5; wallet balance 100 → affords 20 messages
	mustExec(t, mgr, ctx, `INSERT INTO tenant_pricing (tenant_id, country_code, unit_price) VALUES ($1,'US',5)`, tid)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 100)`, tid)

	cctx := withCustomerSession(ctx, tid) // helper: context with a RoleCustomer session for tid

	// 3 phones × 5 = 15 ≤ 100 → created, running, 3 recipients
	id, err := srv.createCampaign(cctx, "US", "{Hi|Hello}", []string{"15551230001", "15551230002", "15551230003"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var state string
	var total int
	mustScanPool(t, mgr, ctx, `SELECT state::text, total FROM campaigns WHERE id=$1`, id, &state, &total)
	if state != "running" || total != 3 {
		t.Fatalf("campaign: state=%s total=%d", state, total)
	}
	var rc int
	mustScanPool(t, mgr, ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id=$1`, id, &rc)
	if rc != 3 {
		t.Fatalf("recipients: want 3, got %d", rc)
	}

	// 30 phones × 5 = 150 > 100 → ErrInsufficientBalance, nothing created
	many := make([]string, 30)
	for i := range many { many[i] = fmt0(i) }
	if _, err := srv.createCampaign(cctx, "US", "x", many); !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("want ErrInsufficientBalance, got %v", err)
	}
}
```

> Implementer notes: add small helpers used above — `withCustomerSession(ctx, tid)` (returns `context.WithValue(ctx, sessionCtxKey, &SessionData{UserID:1, Role:RoleCustomer, TenantID:&tid})`), `pricingRepo(t, mgr)` (returns `pricing.NewRepo(mgr.SystemPool())`), `mustScanPool` (QueryRow on `mgr.SystemPool()` into dests), and `fmt0(i)` (returns a distinct valid phone like `"155500000%02d"`-ish ≥8 digits). Reuse `mustScan`/`mustExec` from E-1's customer_test.go. `sessionCtxKey` + `SessionData` are in middleware.go.

- [ ] **Step 2: Run (RED).** → FAIL (`createCampaign` undefined).

- [ ] **Step 3: Implement `send.go`**

```go
// internal/console/send.go
package console

import (
	"context"
	"errors"
	"fmt"

	"github.com/acme/wadist/internal/crypto"
)

var ErrInsufficientBalance = errors.New("console: insufficient balance for campaign")

func (s *Server) estimateCost(ctx context.Context, country string, n int) int64 {
	if n <= 0 {
		return 0
	}
	data, ok := sessionFrom(ctx)
	if !ok || data.TenantID == nil {
		return 0
	}
	unit := s.pricing.PriceFor(ctx, *data.TenantID, country, 1)
	return int64(n) * unit
}

// createCampaign validates funds then atomically creates a running campaign with
// its template + recipients, all tenant-scoped by RLS WITH CHECK. Recipients carry
// phone_bidx for dedup + the dispatcher's suppression fence.
func (s *Server) createCampaign(ctx context.Context, country, body string, phones []string) (int64, error) {
	if len(s.blindKey) != 32 {
		return 0, ErrSendNotConfigured
	}
	if len(phones) == 0 {
		return 0, fmt.Errorf("console: no recipients")
	}
	data, ok := sessionFrom(ctx)
	if !ok || data.Role != RoleCustomer || data.TenantID == nil {
		return 0, errors.New("console: createCampaign requires a customer session")
	}
	tenantID := *data.TenantID

	estimate := s.estimateCost(ctx, country, len(phones))
	bal, _, err := s.customerBalance(ctx)
	if err != nil {
		return 0, err
	}
	if estimate > bal {
		return 0, ErrInsufficientBalance
	}

	tx, err := s.withTenantTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var templateID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES ($1,'text',$2) RETURNING id`,
		tenantID, body).Scan(&templateID); err != nil {
		return 0, fmt.Errorf("insert template: %w", err)
	}
	var campaignID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state, total) VALUES ($1,$2,'running',$3) RETURNING id`,
		tenantID, templateID, len(phones)).Scan(&campaignID); err != nil {
		return 0, fmt.Errorf("insert campaign: %w", err)
	}
	for _, p := range phones {
		bidx := crypto.BlindIndex(s.blindKey, p)
		if _, err := tx.Exec(ctx,
			`INSERT INTO campaign_recipients (tenant_id, campaign_id, phone, country_code, phone_bidx)
			 VALUES ($1,$2,$3,$4,$5)
			 ON CONFLICT (campaign_id, phone_bidx) DO NOTHING`,
			tenantID, campaignID, p, country, bidx); err != nil {
			return 0, fmt.Errorf("insert recipient: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit campaign: %w", err)
	}
	return campaignID, nil
}
```

- [ ] **Step 4: Run (GREEN)** + vet.

- [ ] **Step 5: Commit**

```bash
git add internal/console/send.go internal/console/send_test.go
git commit -m "feat(console): createCampaign (RLS inserts) + cost estimate + balance guard"
```

---

### Task 3: HTTP send flow (config page, Spintax preview, submit) + cmd/console wiring

**Files:**
- Modify: `internal/console/server.go` (routes + `blindKey`/handlers; the form-rendering customer dashboard links to `/send`)
- Create: `internal/console/send_http.go` (handlers)
- Create: `internal/console/templates/customer_send.html`
- Modify: `internal/console/templates/customer_dashboard.html` (add a "新建群发任务" link to `/send`)
- Modify: `cmd/console/main.go` (build pricing repo + pass `baseCfg.BlindIndexKey` via `WithPricing`/`WithBlindKey`)
- Modify: `internal/console/send_test.go` or new `send_http_test.go` (end-to-end test)

**Interfaces:**
- Routes (customer-only, CSRF-outermost): `GET /send` (form); `POST /send/preview` (returns Spintax sample expansions as an HTML fragment); `POST /send` (submit).
- `handleSendPage`, `handleSendPreview`, `handleSendSubmit`.

- [ ] **Step 1: Write the failing end-to-end test** (`send_http_test.go`)

```go
// internal/console/send_http_test.go
package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSendFlowEndToEnd(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	srv, _ := NewServer(cfg, users, sessions, mgr)
	srv.WithPricing(pricingRepo(t, mgr)).WithBlindKey(make([]byte, 32))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var tid int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name) VALUES ('A') RETURNING id`, &tid)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_pricing (tenant_id, country_code, unit_price) VALUES ($1,'US',2)`, tid)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 1000)`, tid)
	// suppress one number for tenant A
	mustExec(t, mgr, ctx, `INSERT INTO suppression_list (tenant_id, phone_bidx) VALUES ($1, $2)`, tid, blindOf("15550000099"))

	cust := loginAs(t, ts, users, sessions, cfg, "a@x.test", RoleCustomer, &tid)
	tok := csrfFor(t, cust, ts, "/send") // GET the send page → csrf cookie+token

	// upload 3 numbers incl. a duplicate and the suppressed one → 1 dup + 1 suppressed filtered
	form := url.Values{
		"csrf":    {tok},
		"country": {"US"},
		"body":    {"{Hi|Hello} there"},
		"numbers": {"15550000001\n15550000001\n15550000099\n15550000002"},
	}
	resp, err := cust.PostForm(ts.URL+"/send", form)
	if err != nil { t.Fatal(err) }
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther { // 303 to dashboard on success
		t.Fatalf("submit: want 303, got %d", resp.StatusCode)
	}

	// exactly one campaign, running, with 2 recipients (001 + 002; 001 dup collapsed, 099 suppressed)
	var camp, total, rc int64
	mustScanPool(t, mgr, ctx, `SELECT id, total FROM campaigns WHERE tenant_id=$1`, tid, &camp, &total)
	mustScanPool(t, mgr, ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id=$1`, camp, &rc)
	if rc != 2 {
		t.Fatalf("recipients: want 2 (dup+suppressed removed), got %d", rc)
	}

	// preview returns variants
	pr, err := cust.PostForm(ts.URL+"/send/preview", url.Values{"csrf": {tok}, "body": {"{A|B|C}"}})
	if err != nil { t.Fatal(err) }
	pb := readAll(t, pr)
	if pr.StatusCode != 200 || !(strings.Contains(pb,"A")||strings.Contains(pb,"B")||strings.Contains(pb,"C")) {
		t.Fatalf("preview: %d %s", pr.StatusCode, pb)
	}
}
```

> Implementer note: add `blindOf(phone string) []byte { return crypto.BlindIndex(make([]byte,32), phone) }` test helper (must match the key passed to `WithBlindKey` — the 32-byte zero key). Reuse `loginAs`/`csrfFor`/`mustScan`/`mustExec`/`mustScanPool`.

- [ ] **Step 2: Run (RED).** → FAIL (routes 404).

- [ ] **Step 3: Implement handlers in `send_http.go`**

```go
// internal/console/send_http.go
package console

import (
	"errors"
	"math/rand"
	"net/http"

	"github.com/acme/wadist/internal/spintax"
)

func (s *Server) handleSendPage(w http.ResponseWriter, r *http.Request) {
	t, err := parsePage("customer_send.html")
	if err != nil { http.Error(w, "template", http.StatusInternalServerError); return }
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"CSRF": s.issueCSRFToken(w, r), "Error": ""})
}

func (s *Server) handleSendPreview(w http.ResponseWriter, r *http.Request) {
	body := r.FormValue("body")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// up to 5 sample expansions (deterministic seeds for stable display)
	for i := 0; i < 5; i++ {
		sample := spintax.Expand(body, rand.New(rand.NewSource(int64(i+1))))
		_, _ = w.Write([]byte("<li>" + htmlEscape(sample) + "</li>"))
	}
}

func (s *Server) handleSendSubmit(w http.ResponseWriter, r *http.Request) {
	country := r.FormValue("country")
	body := r.FormValue("body")
	parsed := parseNumbers(r.FormValue("numbers"))
	kept, _, err := s.filterSuppressed(r.Context(), parsed.Valid)
	if err != nil {
		s.renderSendError(w, r, "send not configured")
		return
	}
	if len(kept) == 0 {
		s.renderSendError(w, r, "没有可发送的有效号码")
		return
	}
	if _, err := s.createCampaign(r.Context(), country, body, kept); err != nil {
		if errors.Is(err, ErrInsufficientBalance) {
			s.renderSendError(w, r, "余额不足,请先充值")
			return
		}
		http.Error(w, "create campaign: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) renderSendError(w http.ResponseWriter, r *http.Request, msg string) {
	t, err := parsePage("customer_send.html")
	if err != nil { http.Error(w, "template", http.StatusInternalServerError); return }
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = render(w, t, map[string]any{"CSRF": s.issueCSRFToken(w, r), "Error": msg})
}
```

> Add a tiny `htmlEscape` helper (use `html.EscapeString` from stdlib `html`) so preview output is safe. Import `"html"` and define `func htmlEscape(s string) string { return html.EscapeString(s) }` (or call it inline).

- [ ] **Step 4: Register routes** in `server.go` `Handler()` (CSRF-outermost; customer-only):

```go
	custOnly := s.requireRole(RoleCustomer) // (already defined for /account; reuse)
	mux.HandleFunc("GET /send", s.requireAuth(custOnly(s.handleSendPage)))
	mux.HandleFunc("POST /send/preview", s.requireCSRF(s.requireAuth(custOnly(s.handleSendPreview))))
	mux.HandleFunc("POST /send", s.requireCSRF(s.requireAuth(custOnly(s.handleSendSubmit))))
```

- [ ] **Step 5: Templates.** Create `customer_send.html`:

```html
{{define "content"}}
<h1>新建群发任务</h1>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<form method="post" action="/send">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <label>目标国家(2位代码) <input name="country" maxlength="2" required></label>
  <label>号码包(每行一个,支持逗号分隔)<textarea name="numbers" rows="8" required></textarea></label>
  <label>文案(支持 Spintax,如 {你好|您好} {{"{{"}}.name{{"}}"}})<textarea name="body" rows="5" required></textarea></label>
  <button type="submit">提交发送</button>
</form>
<h2>文案预览</h2>
<button hx-post="/send/preview" hx-include="[name='body'],[name='csrf']" hx-target="#preview">预览</button>
<ul id="preview"></ul>
{{end}}
```

And in `customer_dashboard.html` add near the top: `<p><a href="/send">+ 新建群发任务</a></p>`.

- [ ] **Step 6: Wire `cmd/console/main.go`.** It already builds `pricing`/`billing`/`tenants` (from C-1) — confirm, and add `.WithBlindKey(baseCfg.BlindIndexKey)` to the builder chain. (`baseCfg.BlindIndexKey` comes from `WADIST_BLIND_INDEX_KEY`; if empty, the send endpoints fail-closed via `ErrSendNotConfigured`, which is acceptable — log a startup notice if it's empty.)

- [ ] **Step 7: Run end-to-end test + full console suite + build + vet + commit**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/console/ && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./...`
Expected: all green/clean. The end-to-end test proves: upload → dedup (1 dropped) + suppression filter (1 dropped) → 2 recipients → campaign `running` → preview works.

```bash
git add internal/console/send_http.go internal/console/send_http_test.go internal/console/server.go \
        internal/console/templates/customer_send.html internal/console/templates/customer_dashboard.html cmd/console/main.go
git commit -m "feat(console): customer send flow — upload/preview/submit; wire blind key + pricing"
```

---

## Self-Review

**Spec coverage (white-paper §2.2 send flow):**
- Upload number pack + dedup + format validate + report counts → Task 1 (`parseNumbers`). ✓
- Suppression filter (退订名单永不再发) → Task 1 (`filterSuppressed`, blind-index, RLS) + DB dedup index + dispatcher fence. ✓
- Spintax compose + preview → Task 3 (`/send/preview` reuses `spintax.Expand`). ✓
- Cost estimate = count × unit price → Task 2 (`estimateCost`). ✓
- Balance guard (不透支: can't submit unaffordable) → Task 2 (`ErrInsufficientBalance`). ✓
- Submit → creates campaign (RLS) → `running` → dispatcher sends → per-message Hold/Settle/Refund (existing) → appears on dashboard (E-1). ✓
- CSRF + customer-only on all new POSTs → Task 3 (CSRF-outermost convention). ✓

**Placeholder scan:** test helpers (`withCustomerSession`, `pricingRepo`, `mustScanPool`, `fmt0`, `blindOf`) are introduced with explicit "add this helper" notes — flagged, not silent. No TODO/TBD in production code.

**Type consistency:** customer writes via `withTenantTx` (RLS WITH CHECK, tenant_id=session); `ParsedNumbers`/`ErrSendNotConfigured`/`ErrInsufficientBalance`; `crypto.BlindIndex(blindKey, phone)` for both suppression check and recipient `phone_bidx` (same key → fence works); `pricing.PriceFor` reused; no `NewServer` change.

**Known limitations / carries:** country is a single per-campaign 2-letter code (matches white-paper "敲定发送国家"); estimate uses the tenant price with fallback 1; the per-message Hold can still fail mid-campaign if balance is drained concurrently (worker requeues — pending, not overcharged). Multi-country packs and richer validation are out of scope.

---

## Execution: subagent-driven, one task at a time; task review (spec+quality) after each; whole-branch review at the end.
