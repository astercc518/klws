# Module A — 真实发信链路(text-first)+ Spintax Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make the dispatch pipeline actually deliver text WhatsApp messages — wiring Spintax文案混淆, a real campaign-body resolver, and a real whatsmeow text Send into the existing worker. This is the white-paper's Week-2 increment (text bulk-send).

**Architecture:** Three layers. (1) `internal/spintax` — a pure, unit-tested `{a|b}` expander wired into the worker's render step (deterministic per message so retries are stable). (2) a campaign-body resolver — a `store` read that loads the campaign's template body/media metadata by campaign id, replacing the `cmd/wadist` stub. (3) a real `waConn.Send` adapter (the ONLY new code touching whatsmeow), wired into the session factory so `RoutingSender` reaches a live client. Media upload is explicitly OUT OF SCOPE (no raw-blob store exists; deferred to a later module) — media campaigns fail loud.

**Tech Stack:** Go 1.26, pgx/v5, `go.mau.fi/whatsmeow v0.0.0-20260622185415-5f04eac6dbbb`, testcontainers (PG16). The send pipeline (`internal/dispatch`) and billing/anti-ban are already built and tested.

## Global Constraints

- Go module `github.com/acme/wadist`, Go 1.26.4. Toolchain at `/usr/local/go/bin`.
- Integration tests run with `TESTCONTAINERS_RYUK_DISABLED=true`. Never weaken `make gate`.
- **Media is out of scope** for Module A: the resolver returns media metadata if present, but the real `Send` adapter must REJECT a non-nil media handle with a clear "media not supported yet" error rather than silently dropping it. Text campaigns (`media_sha IS NULL`) are the supported path.
- The whatsmeow `Send` adapter CANNOT be unit-tested (needs a live WhatsApp device + 4G proxy) — this matches the existing precedent in `internal/cluster/conn_whatsmeow.go` ("No live-connection unit test exists"). It must compile against the real whatsmeow API and pass `go build ./...` + `go vet`; correctness is validated by the white-paper's Week-2 MANUAL acceptance (a real test phone receives the message). Do NOT fake a unit test that asserts nothing.
- Existing send-path behavior and tests must not regress. Do not change exported signatures gratuitously.
- Determinism: Spintax expansion must be stable for a given `MessageID` (so asynq at-least-once retries re-send identical text, not a new variant).

---

### Task 1: `internal/spintax` expander + wire into the worker render

**Files:**
- Create: `internal/spintax/spintax.go`
- Create: `internal/spintax/spintax_test.go`
- Modify: `internal/dispatch/worker.go` (apply spintax before `renderTemplate`)
- Modify: `internal/dispatch/template.go` (helper to combine spintax + var render, deterministic per seed)
- Create: `internal/dispatch/spintax_wiring_test.go`

**Interfaces:**
- Produces:
  - `func Expand(s string, r *rand.Rand) string` — expands nested `{a|b|c}` choosing one option per group using `r`; text outside braces is literal; an unmatched/empty group degrades gracefully (see tests).
  - In dispatch: `func renderBody(tmpl string, vars map[string]any, seed string) string` — expands spintax with a `rand.Rand` seeded deterministically from `seed`, then applies the existing `{{.var}}` templating. The worker calls `renderBody(body, pl.Vars, pl.MessageID)` instead of `renderTemplate(body, pl.Vars)`.

- [ ] **Step 1: Write the failing spintax test**

```go
// internal/spintax/spintax_test.go
package spintax

import (
	"math/rand"
	"strings"
	"testing"
)

func TestExpandPicksOneOption(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	got := Expand("{Hi|Hello|Hey} there", r)
	if !strings.HasSuffix(got, " there") {
		t.Fatalf("literal tail lost: %q", got)
	}
	first := strings.TrimSuffix(got, " there")
	if first != "Hi" && first != "Hello" && first != "Hey" {
		t.Fatalf("unexpected choice: %q", first)
	}
}

func TestExpandNested(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	got := Expand("{Hi {there|friend}|Hello}", r)
	ok := got == "Hello" || got == "Hi there" || got == "Hi friend"
	if !ok {
		t.Fatalf("nested expansion wrong: %q", got)
	}
}

func TestExpandNoBraces(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	if got := Expand("plain text, no spin", r); got != "plain text, no spin" {
		t.Fatalf("plain text altered: %q", got)
	}
}

func TestExpandDeterministicWithSameSeed(t *testing.T) {
	a := Expand("{a|b|c|d|e}", rand.New(rand.NewSource(42)))
	b := Expand("{a|b|c|d|e}", rand.New(rand.NewSource(42)))
	if a != b {
		t.Fatalf("same seed must yield same expansion: %q vs %q", a, b)
	}
}

func TestExpandEmptyOption(t *testing.T) {
	// "{|x}" means "" or "x" — must not panic, must be one of them.
	r := rand.New(rand.NewSource(4))
	got := Expand("a{|b}c", r)
	if got != "ac" && got != "abc" {
		t.Fatalf("empty-option expansion wrong: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `/usr/local/go/bin/go test ./internal/spintax/ -run TestExpand -v`
Expected: FAIL — `undefined: Expand`.

- [ ] **Step 3: Implement the expander**

```go
// internal/spintax/spintax.go
// Package spintax expands "spintax" templates: {a|b|c} picks one option at
// random. Groups may nest: {a {b|c}|d}. Used to vary bulk-send copy per
// recipient (anti-ban). Unmatched '{' or '}' are treated as literal text.
package spintax

import (
	"math/rand"
	"strings"
)

// Expand walks the string once, recursively resolving each {…|…} group with r.
func Expand(s string, r *rand.Rand) string {
	out, _ := expand(s, 0, r)
	return out
}

// expand parses from index i until end-of-string or an unmatched '}'.
// It returns the rendered text and the index just past where it stopped.
func expand(s string, i int, r *rand.Rand) (string, int) {
	var b strings.Builder
	for i < len(s) {
		switch s[i] {
		case '{':
			choice, next := expandGroup(s, i+1, r)
			b.WriteString(choice)
			i = next
		case '}':
			return b.String(), i + 1 // end of the enclosing group
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), i
}

// expandGroup parses options separated by '|' starting just after '{', picks
// one uniformly, and returns it plus the index just past the closing '}'.
func expandGroup(s string, i int, r *rand.Rand) (string, int) {
	var options []string
	var cur strings.Builder
	for i < len(s) {
		switch s[i] {
		case '{':
			sub, next := expandGroup(s, i+1, r)
			cur.WriteString(sub)
			i = next
		case '|':
			options = append(options, cur.String())
			cur.Reset()
			i++
		case '}':
			options = append(options, cur.String())
			i++
			if len(options) == 0 {
				return "", i
			}
			return options[r.IntN(len(options))], i
		default:
			cur.WriteByte(s[i])
			i++
		}
	}
	// Unmatched '{': treat the whole thing literally (best-effort).
	return "{" + cur.String(), i
}
```

> Implementer note: `rand.Rand` in `math/rand` uses `Intn`, while `math/rand/v2` uses `IntN`. Use the SAME import as the test (`math/rand`, method `Intn`). If you prefer `math/rand/v2`, update BOTH the test and impl consistently. Pick one and make `Expand(s string, r *rand.Rand)` compile with it. Verify nested recursion: `expandGroup` calling itself on `{` handles `{a {b|c}|d}`.

- [ ] **Step 4: Run test to verify it passes**

Run: `/usr/local/go/bin/go test ./internal/spintax/ -v`
Expected: PASS (all 5).

- [ ] **Step 5: Wire into the worker (failing wiring test first)**

```go
// internal/dispatch/spintax_wiring_test.go
package dispatch

import "testing"

func TestRenderBodyExpandsSpintaxThenVars(t *testing.T) {
	// deterministic seed → stable choice; vars still substituted.
	out := renderBody("{Hi|Hello} {{.name}}", map[string]any{"name": "Sam"}, "msg-123")
	if out != "Hi Sam" && out != "Hello Sam" {
		t.Fatalf("unexpected render: %q", out)
	}
	// same seed (same MessageID) → identical output across calls (retry-stable).
	again := renderBody("{Hi|Hello} {{.name}}", map[string]any{"name": "Sam"}, "msg-123")
	if out != again {
		t.Fatalf("not deterministic for same seed: %q vs %q", out, again)
	}
}
```

- [ ] **Step 6: Run it to confirm it fails** (`undefined: renderBody`).

Run: `/usr/local/go/bin/go test ./internal/dispatch/ -run TestRenderBodyExpandsSpintaxThenVars -v`

- [ ] **Step 7: Implement `renderBody`** in `internal/dispatch/template.go` (add imports `hash/fnv`, `math/rand`, `github.com/acme/wadist/internal/spintax`):

```go
// renderBody expands spintax (deterministically seeded by `seed`, so a retry of
// the same message re-renders identical copy) then substitutes {{.var}} values.
func renderBody(tmpl string, vars map[string]any, seed string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	r := rand.New(rand.NewSource(int64(h.Sum64()))) //nolint:gosec // not cryptographic; per-message copy variation
	spun := spintax.Expand(tmpl, r)
	return renderTemplate(spun, vars)
}
```

In `internal/dispatch/worker.go`, replace `rendered := renderTemplate(body, pl.Vars)` with:

```go
	rendered := renderBody(body, pl.Vars, pl.MessageID)
```

- [ ] **Step 8: Run tests + dispatch regression + commit**

Run: `/usr/local/go/bin/go test ./internal/spintax/ ./internal/dispatch/ -run 'TestExpand|TestRenderBody' -v`
Expected: PASS.

Run (regression): `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/dispatch/`
Expected: ok.

```bash
git add internal/spintax/ internal/dispatch/template.go internal/dispatch/worker.go internal/dispatch/spintax_wiring_test.go
git commit -m "feat(spintax): {a|b} expander wired into worker render (deterministic per message)"
```

---

### Task 2: Campaign-body resolver (replace the cmd/wadist stub)

**Files:**
- Create: `internal/store/campaign_send.go` (a read method)
- Create: `internal/store/campaign_send_test.go`
- Modify: `cmd/wadist/main.go` (replace the stub `SendBodyResolver`)

**Interfaces:**
- Produces: `func (m *Manager) CampaignSendable(ctx context.Context, campaignID int64) (body, mediaSha, mime string, err error)` — joins `campaigns → campaign_templates`, returning the template body and (nullable) media metadata. Returns a sentinel `ErrCampaignNotFound` if the campaign id is unknown.
- Consumed by: the `SendBodyResolver` closure in `cmd/wadist/main.go`, which adapts it to the dispatch signature `func(ctx, campaignID) (body, mediaSha, mime string, raw []byte, err error)` with `raw = nil` (media bytes out of scope).

- [ ] **Step 1: Read** `internal/store/manager.go` (Manager struct, `bizPool`/`Pool()`), and an existing store read + its test helper (e.g. `internal/store/node_test.go` / `testsupport_test.go`) to mirror the testcontainer + migration-apply pattern and confirm helper names.

- [ ] **Step 2: Write the failing test**

```go
// internal/store/campaign_send_test.go
package store

import (
	"context"
	"errors"
	"testing"
)

func TestCampaignSendable(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t) // adapt to the real helper in this package (see note)
	pool := mgr.Pool()

	// seed a template + campaign
	var tplID, campID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES (1, 'text', '{Hi|Hello} {{.name}}') RETURNING id`).
		Scan(&tplID); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state) VALUES (1, $1, 'running') RETURNING id`, tplID).
		Scan(&campID); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}

	body, mediaSha, mime, err := mgr.CampaignSendable(ctx, campID)
	if err != nil {
		t.Fatalf("CampaignSendable: %v", err)
	}
	if body != "{Hi|Hello} {{.name}}" || mediaSha != "" || mime != "" {
		t.Fatalf("unexpected: body=%q mediaSha=%q mime=%q", body, mediaSha, mime)
	}

	if _, _, _, err := mgr.CampaignSendable(ctx, 999999); !errors.Is(err, ErrCampaignNotFound) {
		t.Fatalf("unknown campaign: want ErrCampaignNotFound, got %v", err)
	}
}
```

> Implementer note: confirm this package's testcontainer helper name (it may be `newTestManager`, `testManager`, or a DSN+apply helper) by reading the existing `_test.go` files, and adapt the first two lines. Seed via `mgr.Pool()` / `SystemPool()` as those helpers do. Do NOT invent helpers.

- [ ] **Step 3: Run to confirm it fails** (`mgr.CampaignSendable undefined`).

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/store/ -run TestCampaignSendable -v`

- [ ] **Step 4: Implement the read method**

```go
// internal/store/campaign_send.go
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrCampaignNotFound is returned when a campaign id has no row.
var ErrCampaignNotFound = errors.New("store: campaign not found")

// CampaignSendable returns the template body and (nullable) media metadata for
// a campaign, joining campaigns → campaign_templates. mediaSha/mime are "" when
// the template is text-only.
func (m *Manager) CampaignSendable(ctx context.Context, campaignID int64) (body, mediaSha, mime string, err error) {
	var sha, mm *string
	err = m.bizPool.QueryRow(ctx, `
SELECT t.body, t.media_sha, t.media_mime
  FROM campaigns c JOIN campaign_templates t ON t.id = c.template_id
 WHERE c.id = $1`, campaignID).Scan(&body, &sha, &mm)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", ErrCampaignNotFound
	}
	if err != nil {
		return "", "", "", fmt.Errorf("load campaign sendable: %w", err)
	}
	if sha != nil {
		mediaSha = *sha
	}
	if mm != nil {
		mime = *mm
	}
	return body, mediaSha, mime, nil
}
```

> If the `Manager` field for the business pool is not named `bizPool`, use the accessor the package exposes (`m.Pool()` returns `*pgxpool.Pool`). Match what compiles.

- [ ] **Step 5: Run to confirm it passes.**

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test ./internal/store/ -run TestCampaignSendable -v`

- [ ] **Step 6: Replace the stub resolver in `cmd/wadist/main.go`**

Find the `RegisterSendHandler(mux, worker, func(...) { return "", "", "", nil, errors.New("resolver: not wired (pre-M-send)") })` call and replace the closure with:

```go
	dispatch.RegisterSendHandler(mux, worker, func(ctx context.Context, campaignID int64) (string, string, string, []byte, error) {
		body, mediaSha, mime, err := mgr.CampaignSendable(ctx, campaignID)
		if err != nil {
			return "", "", "", nil, err
		}
		// Media bytes are out of scope for Module A (text-first). mediaSha/mime
		// are surfaced so the Send adapter can fail loud on media campaigns.
		return body, mediaSha, mime, nil, nil
	})
```

- [ ] **Step 7: Build + vet + commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/store/ ./cmd/wadist/`
Expected: clean.

```bash
git add internal/store/campaign_send.go internal/store/campaign_send_test.go cmd/wadist/main.go
git commit -m "feat(store): CampaignSendable resolver; wire it into the send handler"
```

---

### Task 3: Real whatsmeow text Send adapter + session wiring

**Files:**
- Modify: `internal/cluster/conn_whatsmeow.go` (implement `Send` on `waConn`)
- Modify: `internal/cluster/sender.go` (export a `NewSessionWithSender` constructor)
- Modify: `cmd/wadist/main.go` (wire the conn as the session's sender)
- Create: `internal/cluster/conn_whatsmeow_send_test.go` (compile-only / guard test — see constraints)

**Interfaces:**
- Produces:
  - `func (c *waConn) Send(ctx context.Context, phone, body string, media *dispatch.MediaHandle) (string, error)` — sends a text message via the live whatsmeow client; returns the message id. If `media != nil`, returns an error `"cluster: media send not supported yet"` (Module A is text-only). This makes `*waConn` satisfy `cluster.SessionSender`.
  - `func NewSessionWithSender(jid string, conn Conn, lock DeviceLockHandle, sender SessionSender) *Session` — exported wrapper around the existing unexported `newSessionWithSender`, so the `cmd/wadist` factory can wire the conn as the sender.

- [ ] **Step 1: Learn the real whatsmeow API.** This adapter must compile against the pinned version. Inspect the actual API:

```bash
/usr/local/go/bin/go doc go.mau.fi/whatsmeow Client.SendMessage
/usr/local/go/bin/go doc go.mau.fi/whatsmeow Client.SendMessage 2>/dev/null; /usr/local/go/bin/go doc go.mau.fi/whatsmeow.SendResponse
/usr/local/go/bin/go doc go.mau.fi/whatsmeow/types.JID
/usr/local/go/bin/go doc go.mau.fi/whatsmeow/binary/proto.Message
/usr/local/go/bin/go doc go.mau.fi/whatsmeow/types ParseJID
```
Note the exact signatures: `SendMessage(ctx, to types.JID, message *waE2E.Message, ...) (SendResponse, error)` (the proto package and `SendResponse` field for the id, e.g. `.ID`, may differ by version — confirm). Confirm how to build a text message (a `*waE2E.Message{Conversation: proto.String(body)}` or `ExtendedTextMessage`) and how to construct the recipient `types.JID` from a phone number (`types.NewJID(phone, types.DefaultUserServer)` or `types.JID{User: phone, Server: types.DefaultUserServer}`). Record findings in your report.

- [ ] **Step 2: Implement `Send` on `waConn`** in `conn_whatsmeow.go`. Use the EXACT API names you confirmed in Step 1. Skeleton (adjust proto/types package paths + the SendResponse id field to the real API):

```go
// Send delivers a text message to `phone` via the live whatsmeow client and
// returns the server message id. Media is not yet supported (Module A is
// text-first); a non-nil media handle is rejected. This adapter has no unit
// test — it requires a live device + proxy (see package doc); correctness is
// validated by manual send to a real test phone.
func (c *waConn) Send(ctx context.Context, phone, body string, media *dispatch.MediaHandle) (string, error) {
	if media != nil {
		return "", errors.New("cluster: media send not supported yet")
	}
	jid := types.NewJID(phone, types.DefaultUserServer) // confirm constructor in Step 1
	msg := &waE2E.Message{Conversation: proto.String(body)} // confirm proto pkg in Step 1
	resp, err := c.client.SendMessage(ctx, jid, msg)
	if err != nil {
		return "", fmt.Errorf("whatsmeow send: %w", err)
	}
	return resp.ID, nil // confirm SendResponse id field name in Step 1
}
```

Add the necessary imports (`context` already present; add `errors`, `fmt`, the `dispatch` package, `types`, the proto/`waE2E` package). Add `var _ SessionSender = (*waConn)(nil)` to assert the interface at compile time. NOTE: this introduces an import of `internal/dispatch` into `conn_whatsmeow.go` — `internal/cluster/sender.go` already imports `dispatch` (for `dispatch.MediaHandle`/`dispatch.Sender`), so no new cycle is created; confirm `internal/dispatch` does not import `internal/cluster` (it must not).

- [ ] **Step 3: Export the session+sender constructor** in `sender.go`:

```go
// NewSessionWithSender builds a Session with an outbound sender wired (the real
// factory passes the whatsmeow conn as both Conn and SessionSender).
func NewSessionWithSender(jid string, conn Conn, lock DeviceLockHandle, sender SessionSender) *Session {
	return newSessionWithSender(jid, conn, lock, sender)
}
```

- [ ] **Step 4: Wire it in the factory** (`cmd/wadist/main.go`). The factory currently ends with `return cluster.NewSession(jid, conn, lock), nil`. The `conn` is a `*waConn` (from `cluster.NewWAConn`) which now implements `SessionSender`. Change to wire it as the sender:

```go
		return cluster.NewSessionWithSender(jid, conn, lock, conn), nil
```

(`conn` satisfies both `Conn` and `SessionSender`. If `NewWAConn`'s return type is unexported `*waConn` and the compiler complains passing it as `SessionSender`, that's fine — it's the same value; the assertion in Step 2 guarantees it satisfies the interface.)

- [ ] **Step 5: Add a compile/guard test** (the ONLY testable behavior without a live device: that media is rejected). This is honest coverage, not a vacuous assertion:

```go
// internal/cluster/conn_whatsmeow_send_test.go
package cluster

import (
	"context"
	"testing"

	"github.com/acme/wadist/internal/dispatch"
)

// The live text path needs a real device+proxy and is validated manually
// (white-paper Week-2). The one thing we CAN assert in-process is that media
// sends are rejected (Module A is text-only) — and that *waConn satisfies
// SessionSender at compile time (the var _ assertion in conn_whatsmeow.go).
func TestWAConnRejectsMedia(t *testing.T) {
	c := &waConn{} // nil client is fine: media check happens before any client use
	_, err := c.Send(context.Background(), "15551234567", "hi", &dispatch.MediaHandle{})
	if err == nil {
		t.Fatal("expected media send to be rejected in Module A")
	}
}
```

- [ ] **Step 6: Build, vet, focused test, dispatch+cluster regression, commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/cluster/ ./cmd/wadist/`
Expected: clean (this is the primary gate for the whatsmeow adapter).

Run: `TESTCONTAINERS_RYUK_DISABLED=true /usr/local/go/bin/go test -race ./internal/cluster/`
Expected: ok (existing cluster tests + the media-rejection guard).

```bash
git add internal/cluster/conn_whatsmeow.go internal/cluster/sender.go internal/cluster/conn_whatsmeow_send_test.go cmd/wadist/main.go
git commit -m "feat(cluster): real whatsmeow text Send adapter; wire session sender (media deferred)"
```

---

## Self-Review

**Spec coverage (vs white-paper Week-2, text-first):**
- Spintax文案混淆 → Task 1 (expander + worker wiring, deterministic per message). ✓
- Real send path replacing the resolver stub → Task 2. ✓
- Real whatsmeow delivery → Task 3 (text Send adapter + session sender wiring). ✓
- Media → explicitly deferred & failed-loud (Global Constraints + Task 3 guard). ✓
- "Limit/rate/anti-ban/billing/refund" → already in the existing worker (unchanged); this module only fills the resolver/sender/render gaps. ✓

**Placeholder scan:** Task 2 & the spintax test name helpers/`rand` variants with explicit implementer notes to confirm against the real package — flagged, not silent. Task 3's skeleton explicitly says "confirm the real API in Step 1" because the exact whatsmeow proto/types/SendResponse names are version-specific — this is a deliberate consult-the-API instruction, not a vague placeholder.

**Type consistency:** `spintax.Expand(string, *rand.Rand) string`; `renderBody(tmpl, vars, seed)`; `Manager.CampaignSendable(...) (body, mediaSha, mime string, err error)`; `waConn.Send` matches `cluster.SessionSender` (`Send(ctx, phone, body string, media *dispatch.MediaHandle) (string, error)`); `NewSessionWithSender` matches the unexported `newSessionWithSender` arg order.

**Known limitations (honest, for the record):**
- The whatsmeow text Send is compile-verified and wired, but end-to-end "a real phone receives it" is the white-paper's Week-2 MANUAL acceptance (needs a live WhatsApp account + 4G proxy not available in CI/sandbox).
- Media campaigns are rejected (no raw-blob store); media upload is a separate future module.

---

## Execution: subagent-driven, one task at a time; task review (spec+quality) after each; whole-branch review at the end.
