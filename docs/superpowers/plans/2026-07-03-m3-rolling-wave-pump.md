# M3: Rolling-Wave 令牌桶滚筒波调度 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用一个内存令牌桶「滚筒波」Pump 替换 asynq 队列 + 1s×100 批处理脉冲，把发送并发平滑摊销到每秒稳定 τ 条，杜绝 CPU/内存/GC 尖峰；全程复用现有 dispatchBatch 选号/记账/幂等逻辑与 SendWorker.ProcessSend。

**Architecture:** 关键复用点——`dispatchBatch` 早已通过 `dispatch.Enqueuer` 接口（`EnqueueSend(ctx, SendPayload, delay)`）与 asynq 解耦。M3 在这个接缝处换传输：注入一个内存有界 channel 的 `PumpEnqueuer` 替代 `AsynqEnqueuer`，**dispatchBatch 的选号/assign/message_id/sent_today 逻辑一行不改**（全部不变式与测试保留）。新增 `Pump`：`x/time/rate` 令牌桶（速率 τ）+ S 个 worker goroutine 消费 channel，每发一条先取一个令牌，再调 `SendWorker.ProcessSend` inline（不经 asynq）。配置门控 `WADIST_DISPATCH_MODE`（默认 `asynq`=现状，`pump`=新路径），零爆炸半径。去 asynq 后崩溃恢复靠启动时 reclaim「孤儿 assignment」（`message_id` 确定性 → billing 幂等）。

**Tech Stack:** Go 1.26、`golang.org/x/time/rate`（令牌桶）、现有 `internal/dispatch`（SendWorker/ProcessSend/dispatchBatch/Enqueuer 接缝）、PostgreSQL（campaign_recipients 状态机为持久真相源）、testcontainers-go（PG 集成测试）。

## Global Constraints

- **零爆炸半径**：`WADIST_DISPATCH_MODE` 默认 `asynq`；未设时走现有 asynq + DispatchRunning(1s,100) 路径，行为逐字不变。`pump` 为 opt-in。
- **保留的不变式**（spec §5.3 + §8）：`dispatchBatch` 的 `message_id=campaignID:recipientID` 幂等 claim、`sent_today` 对称记账（assign +1 / 非发送退出 −1 / 真实发送保留）、`sendgate.Admit` 原子闸、`billing.Hold/Settle/RequestRefund`、`campaign_recipients` 状态机——M3 一律**复用**，不重写。`SendWorker.ProcessSend` 逐字复用。
- **平滑 τ**：令牌桶以 1/τ 间隔滴出，近 Poisson 到达，无「整秒一批」脉冲。稳态 τ 由 `WADIST_SEND_RATE`（默认沿用今值：约等于原 batch 吞吐；起始 160/s 上限、实际由号池/配额自然限流）。
- **去 asynq 的取舍（已在 spec §5.3 与用户确认接受）**：Pump 无 durable 队列；崩溃丢失内存中 in-flight payload，靠 PG `campaign_recipients` 持久态 + 启动 reclaim 恢复；`message_id` 确定性使重投幂等。
- **与 spec §5.2 的偏差（本计划显式决策）**：spec 草图是「账号驱动」（拉到期号→找 recipient）。本计划改为**复用 dispatchBatch 的 recipient 驱动 + Pump 令牌桶限速传输**——达成同一验收（平滑 τ、无 asynq 脉冲、无 GC 尖峰）且最大化保留 §5.3 的不变式清单，风险远低于账号驱动重写。控制面温驻留（control.Scheduler/WorkingSet）负责「哪些号在线」，与本 Pump（负责「以 τ 喂已 assign 的发送」）正交，均不改。
- 优雅停机：Supervisor 排空 channel + 等 in-flight ProcessSend（≤ ShutdownTimeout）。
- TDD；改 `internal/dispatch`（红线已解除）后跑 `make gate`。x/time 从 indirect 转 direct。

---

## 文件结构

- Modify `internal/config/config.go` — 读 `WADIST_DISPATCH_MODE`(默认"asynq")、`WADIST_SEND_RATE`(默认沿用，见 Task 1)、`WADIST_PUMP_BUFFER`(默认 512)、`WADIST_SEND_WORKERS`(默认=AsynqConcurrency)。加到 `Config` 结构。
- Create `internal/dispatch/pump.go` — `PumpEnqueuer`(实现 `Enqueuer`，内存有界 channel) + `Pump`(令牌桶 + worker 池 + Run/Shutdown)。
- Create `internal/dispatch/pump_test.go` — 单元：EnqueueSend 满则 ErrPumpFull；Pump 以 τ 限速消费并调 ProcessSend。
- Modify `internal/dispatch/batch.go` — **无需改选号逻辑**；仅确认 `EnqueueSend` 失败时该 payload 的 assignment 随 tx 回滚（已如此）。可能加：`EnqueueSend` 返回 `ErrPumpFull` 时该 recipient 留 pending（当前 tx 回滚已覆盖）。
- Create `internal/dispatch/reclaim.go` — `ReclaimOrphanedAssignments(ctx, pool) (int, error)`：启动时把 `state='pending' AND assigned_jid IS NOT NULL` 的孤儿 assignment 复位（清 assigned_jid/message_id + 对应 sent_today−1），供 pump 模式 boot 调用。
- Create `internal/dispatch/reclaim_test.go` — reclaim 复位 + sent_today 递减。
- Modify `cmd/wadist/main.go` — 按 `cfg.DispatchMode` 分派：`pump` → 构造 PumpEnqueuer(注入 dispatcher) + Pump，跳过 asynqSrv，boot reclaim，用 channel-余量驱动的 filler 循环替换 1s×100 tick，Supervisor 排空 Pump；`asynq` → 现路径不变。
- Create `internal/dispatch/pump_integration_test.go` — pump 模式端到端（recipients 经 pump 流过、τ 平滑、不变式保持）。

---

### Task 1: 配置（DISPATCH_MODE / SEND_RATE / PUMP_BUFFER / SEND_WORKERS）

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go` (or a store/config-style test if config has no test file — grep first)

**Interfaces:**
- Produces: `Config.DispatchMode string`(默认"asynq")、`Config.SendRate float64`(令牌/秒，默认 sentinel→见下)、`Config.PumpBuffer int`(默认 512)、`Config.SendWorkers int`(默认=AsynqConcurrency)。

- [ ] **Step 1: Write the failing test**

```go
// internal/config/config_test.go (add; grep existing test style first)
func TestConfig_DispatchDefaults(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x") // Load requires DSN; match existing test setup
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DispatchMode != "asynq" {
		t.Fatalf("DispatchMode default = %q; want asynq", cfg.DispatchMode)
	}
	if cfg.PumpBuffer != 512 {
		t.Fatalf("PumpBuffer default = %d; want 512", cfg.PumpBuffer)
	}
	if cfg.SendWorkers != cfg.AsynqConcurrency {
		t.Fatalf("SendWorkers default = %d; want AsynqConcurrency %d", cfg.SendWorkers, cfg.AsynqConcurrency)
	}
	if cfg.SendRate <= 0 {
		t.Fatalf("SendRate default = %v; want > 0", cfg.SendRate)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/config/ -run TestConfig_DispatchDefaults -v`
Expected: FAIL — fields undefined.

- [ ] **Step 3: Implement**

In `internal/config/config.go` add to `Config`:
```go
// DispatchMode selects the send driver: "asynq" (durable queue + 1s batch tick,
// default) or "pump" (in-memory token-bucket Rolling-Wave pump).
DispatchMode string
// SendRate is the global token-bucket rate (sends/sec) in pump mode.
SendRate float64
// PumpBuffer is the in-memory SendPayload channel capacity in pump mode.
PumpBuffer int
// SendWorkers is the number of concurrent send workers in pump mode.
SendWorkers int
```
In `Load()` (after AsynqConcurrency is resolved), add:
```go
cfg.DispatchMode = os.Getenv("WADIST_DISPATCH_MODE")
if cfg.DispatchMode == "" {
	cfg.DispatchMode = "asynq"
}
cfg.PumpBuffer = envInt("WADIST_PUMP_BUFFER", 512)          // reuse existing int-env helper; grep for one
cfg.SendWorkers = envInt("WADIST_SEND_WORKERS", cfg.AsynqConcurrency)
cfg.SendRate = envFloat("WADIST_SEND_RATE", 160.0)          // 160/s peak ceiling; steady rate self-limited by quotas
```
> Grep for existing env helpers first: `grep -nE "func env(Int|Float|Duration)" internal/config/config.go`. If `envInt`/`envFloat` don't exist, parse inline with `strconv.Atoi`/`ParseFloat`, defaulting on empty/error. The 160.0 default matches spec §9 peak-capability; steady throughput is naturally bounded by `sendgate` quotas + account pool, so this is a ceiling not a forced rate.

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/config/ -run TestConfig_DispatchDefaults -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): dispatch mode + pump rate/buffer/workers (default asynq)"
```

---

### Task 2: PumpEnqueuer（内存有界 channel，实现 dispatch.Enqueuer）

**Files:**
- Create: `internal/dispatch/pump.go`
- Test: `internal/dispatch/pump_test.go`

**Interfaces:**
- Consumes: existing `SendPayload`, `Enqueuer` interface (`internal/dispatch/types.go:44`).
- Produces:
  - `type PumpEnqueuer struct { ch chan SendPayload }`
  - `func NewPumpEnqueuer(buffer int) *PumpEnqueuer`
  - `func (p *PumpEnqueuer) EnqueueSend(ctx context.Context, pl SendPayload, delay time.Duration) error` — NON-blocking: on full channel returns `ErrPumpFull` (so dispatchBatch's tx rolls back that assignment, recipient stays pending). `delay` is ignored (pacing is the pump's job).
  - `func (p *PumpEnqueuer) C() <-chan SendPayload` — the pump workers' read side.
  - `func (p *PumpEnqueuer) Close()` — close the channel (drain signal).
  - `var ErrPumpFull = errors.New("dispatch: pump buffer full")`
- Compile assertion: `var _ Enqueuer = (*PumpEnqueuer)(nil)`.

- [ ] **Step 1: Write the failing test**

```go
package dispatch

import (
	"context"
	"errors"
	"testing"
)

func TestPumpEnqueuer_FullReturnsErr(t *testing.T) {
	p := NewPumpEnqueuer(1)
	ctx := context.Background()
	if err := p.EnqueueSend(ctx, SendPayload{RecipientID: 1}, 0); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	// buffer=1 now full → non-blocking, returns ErrPumpFull (does NOT block).
	if err := p.EnqueueSend(ctx, SendPayload{RecipientID: 2}, 0); !errors.Is(err, ErrPumpFull) {
		t.Fatalf("second enqueue err = %v; want ErrPumpFull", err)
	}
	got := <-p.C()
	if got.RecipientID != 1 {
		t.Fatalf("drained %d; want 1", got.RecipientID)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestPumpEnqueuer_FullReturnsErr -v`
Expected: FAIL — `NewPumpEnqueuer` undefined.

- [ ] **Step 3: Implement**

```go
// internal/dispatch/pump.go
package dispatch

import (
	"context"
	"errors"
	"time"
)

var ErrPumpFull = errors.New("dispatch: pump buffer full")

// PumpEnqueuer is an in-memory Enqueuer: dispatchBatch pushes SendPayloads into a
// bounded channel instead of asynq. It is non-blocking — a full buffer returns
// ErrPumpFull so the caller's transaction rolls back that assignment (the
// recipient stays pending and is retried on the next fill), providing natural
// backpressure without holding a DB tx open.
type PumpEnqueuer struct {
	ch chan SendPayload
}

func NewPumpEnqueuer(buffer int) *PumpEnqueuer {
	if buffer < 1 {
		buffer = 1
	}
	return &PumpEnqueuer{ch: make(chan SendPayload, buffer)}
}

var _ Enqueuer = (*PumpEnqueuer)(nil)

// EnqueueSend pushes non-blockingly; delay is ignored (the pump paces sends).
func (p *PumpEnqueuer) EnqueueSend(_ context.Context, pl SendPayload, _ time.Duration) error {
	select {
	case p.ch <- pl:
		return nil
	default:
		return ErrPumpFull
	}
}

// C is the read side consumed by pump workers.
func (p *PumpEnqueuer) C() <-chan SendPayload { return p.ch }

// Len reports current buffered payloads (used by the fill loop for backpressure).
func (p *PumpEnqueuer) Len() int { return len(p.ch) }

// Cap reports the buffer capacity.
func (p *PumpEnqueuer) Cap() int { return cap(p.ch) }

// Close closes the channel so pump workers drain and exit.
func (p *PumpEnqueuer) Close() { close(p.ch) }
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestPumpEnqueuer_FullReturnsErr -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/dispatch/pump.go internal/dispatch/pump_test.go
git commit -m "feat(dispatch): in-memory PumpEnqueuer (bounded channel, non-blocking backpressure)"
```

---

### Task 3: Pump（令牌桶 + worker 池 → ProcessSend）

**Files:**
- Modify: `internal/dispatch/pump.go`
- Test: `internal/dispatch/pump_test.go`

**Interfaces:**
- Consumes: Task 2 `PumpEnqueuer`; existing `SendWorker.ProcessSend`; existing `SendBodyResolver` (the body/media loader signature used by `RegisterSendHandler` — grep `internal/dispatch/asynqadapter.go` for the exact `SendBodyResolver` type).
- Produces:
  - `type Pump struct { ... }`
  - `func NewPump(pe *PumpEnqueuer, w *SendWorker, resolve SendBodyResolver, rate float64, workers int) *Pump`
  - `func (p *Pump) Run(ctx context.Context) error` — starts `workers` goroutines, each: read payload from `pe.C()` → `limiter.Wait(ctx)` (token) → load body via `resolve(ctx, pl.CampaignID)` → `w.ProcessSend(ctx, pl, body, mediaSha, mime, raw)`; per-send errors logged, loop continues. Returns when ctx is cancelled AND the channel is drained (graceful).

- [ ] **Step 1: Write the failing test** (pump paces via a fake sender + counts sends under a rate cap)

```go
func TestPump_RateLimitsAndProcesses(t *testing.T) {
	// A SendWorker with fake sender/gate/billing that records sends; grep the
	// existing worker_test.go helpers (fakeSender, newTestWorker) and reuse them.
	rec, w := newRecordingWorker(t) // returns a recorder + *SendWorker (reuse worker_test helpers)
	pe := NewPumpEnqueuer(8)
	resolve := func(ctx context.Context, campaignID int64) (string, string, string, []byte, error) {
		return "hi", "", "", nil, nil
	}
	p := NewPump(pe, w, resolve, 1000 /*rate*/, 2 /*workers*/)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	for i := 1; i <= 5; i++ {
		if err := pe.EnqueueSend(ctx, SendPayload{RecipientID: int64(i), CampaignID: 1, JID: "j"}, 0); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	// wait until all 5 processed (poll the recorder), then shut down.
	waitFor(t, func() bool { return rec.count() == 5 }, 2*time.Second)
	cancel()
	pe.Close()
	<-done
}
```
> `newRecordingWorker`/`waitFor`: build on the existing `worker_test.go` fakes (grep `internal/dispatch/worker_test.go` for the fake sender/gate/billing constructors). The test asserts all payloads flow through ProcessSend; a stricter rate-timing assertion is optional (rate correctness is `x/time/rate`'s own guarantee).

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestPump_RateLimitsAndProcesses -v`
Expected: FAIL — `NewPump` undefined.

- [ ] **Step 3: Implement**

```go
// append to internal/dispatch/pump.go
import (
	"log"
	"sync"

	"golang.org/x/time/rate"
)

type Pump struct {
	pe      *PumpEnqueuer
	w       *SendWorker
	resolve SendBodyResolver
	lim     *rate.Limiter
	workers int
}

func NewPump(pe *PumpEnqueuer, w *SendWorker, resolve SendBodyResolver, ratePerSec float64, workers int) *Pump {
	if workers < 1 {
		workers = 1
	}
	// burst = workers so all workers can grab a token at once at startup, then
	// the sustained rate is ratePerSec.
	return &Pump{pe: pe, w: w, resolve: resolve, lim: rate.NewLimiter(rate.Limit(ratePerSec), workers), workers: workers}
}

// Run starts the worker pool. Each worker consumes payloads, waits for a token
// (smooth τ pacing — no batch pulse), loads the body, and runs ProcessSend.
// Returns after ctx is cancelled and the channel is drained + workers exit.
func (p *Pump) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pl := range p.pe.C() {
				if err := p.lim.Wait(ctx); err != nil {
					// ctx cancelled: stop pacing but keep draining without a token so
					// buffered work isn't dropped (bounded by ShutdownTimeout upstream).
					if ctx.Err() != nil {
						p.process(context.Background(), pl)
						continue
					}
				}
				p.process(ctx, pl)
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

func (p *Pump) process(ctx context.Context, pl SendPayload) {
	body, mediaSha, mime, raw, err := p.resolve(ctx, pl.CampaignID)
	if err != nil {
		log.Printf("pump resolve campaign %d: %v", pl.CampaignID, err)
		return
	}
	if err := p.w.ProcessSend(ctx, pl, body, mediaSha, mime, raw); err != nil {
		log.Printf("pump ProcessSend recipient %d: %v", pl.RecipientID, err)
	}
}
```
> Confirm `SendBodyResolver`'s exact signature in `internal/dispatch/asynqadapter.go` and match it in `NewPump`/`resolve` (the test above assumes `(ctx, campaignID) -> (body, mediaSha, mime, raw, err)` — align to the real type). Add `golang.org/x/time/rate` (currently indirect) via `go mod tidy` after this compiles.

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestPump -race -v && go mod tidy`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/dispatch/pump.go internal/dispatch/pump_test.go go.mod go.sum
git commit -m "feat(dispatch): Rolling-Wave Pump (token bucket + worker pool → ProcessSend)"
```

---

### Task 4: 启动 reclaim 孤儿 assignment（去 asynq 的崩溃恢复）

**Files:**
- Create: `internal/dispatch/reclaim.go`
- Test: `internal/dispatch/reclaim_test.go`

**Interfaces:**
- Produces: `func ReclaimOrphanedAssignments(ctx context.Context, pool *pgxpool.Pool) (int, error)` — in ONE tx: find `campaign_recipients` with `state='pending' AND assigned_jid IS NOT NULL` (orphans from a pre-crash in-memory pump), decrement each assigned account's `sent_today` by its orphan count, then reset `assigned_jid=NULL, message_id=NULL, attempt=GREATEST(attempt-1,0)`. Returns count reclaimed. Safe because `message_id` is deterministic (`campaignID:recipientID`) so re-dispatch is billing-idempotent.

- [ ] **Step 1: Write the failing test**

```go
package dispatch

import (
	"context"
	"testing"
)

func TestReclaimOrphanedAssignments(t *testing.T) {
	ctx := context.Background()
	pool := newDispatchTestPool(t) // real PG + migrations; reuse existing dispatch test helper (grep)
	// seed: 1 account sent_today=1, 1 pending recipient assigned to it (orphan).
	mustExec(t, pool, `INSERT INTO account_devices (account_jid, tenant_id, sent_today) VALUES ('acc', 1, 1)`)
	mustExec(t, pool, `INSERT INTO campaigns (id, tenant_id, state) VALUES (7, 1, 'running')`)
	mustExec(t, pool, `INSERT INTO campaign_recipients (id, campaign_id, phone, country_code, state, assigned_jid, message_id, attempt)
		VALUES (55, 7, '+1', 'US', 'pending', 'acc', '7:55', 1)`)

	n, err := ReclaimOrphanedAssignments(ctx, pool)
	if err != nil || n != 1 {
		t.Fatalf("reclaim = %d,%v; want 1", n, err)
	}
	var jid *string
	var mid *string
	var sent int
	pool.QueryRow(ctx, `SELECT r.assigned_jid, r.message_id, a.sent_today
		FROM campaign_recipients r JOIN account_devices a ON a.account_jid='acc' WHERE r.id=55`).Scan(&jid, &mid, &sent)
	if jid != nil || mid != nil {
		t.Fatalf("orphan not reset: jid=%v mid=%v", jid, mid)
	}
	if sent != 0 {
		t.Fatalf("sent_today = %d; want 0 (decremented on reclaim)", sent)
	}
}
```
> `newDispatchTestPool`/`mustExec`: reuse the existing dispatch integration-test harness (grep `internal/dispatch/*_test.go` for how it spins PG + runs migrations; e.g. a `newTestPool`/`newManagerWithSchema` equivalent). Match real column names (grep migration 0006 for campaign_recipients columns: `attempt`, `assigned_jid`, `message_id`, `phone`, `country_code`, `vars`).

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestReclaimOrphanedAssignments -v`
Expected: FAIL — `ReclaimOrphanedAssignments` undefined.

- [ ] **Step 3: Implement**

```go
// internal/dispatch/reclaim.go
package dispatch

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReclaimOrphanedAssignments resets recipients that were assigned to an account
// (sent_today bumped, message_id claimed) but never reached a terminal state —
// orphans left when an in-memory pump lost buffered/in-flight payloads on crash.
// It decrements each account's sent_today by its orphan count and clears the
// assignment so dispatchBatch re-picks them. Deterministic message_id makes
// re-dispatch billing-idempotent. Call once at pump-mode boot.
func ReclaimOrphanedAssignments(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// decrement sent_today per account by its orphan count.
		if _, err := tx.Exec(ctx, `
UPDATE account_devices a
   SET sent_today = GREATEST(a.sent_today - o.cnt, 0)
  FROM (
    SELECT assigned_jid, COUNT(*) AS cnt
      FROM campaign_recipients
     WHERE state='pending' AND assigned_jid IS NOT NULL
     GROUP BY assigned_jid
  ) o
 WHERE a.account_jid = o.assigned_jid`); err != nil {
			return fmt.Errorf("reclaim sent_today: %w", err)
		}
		ct, err := tx.Exec(ctx, `
UPDATE campaign_recipients
   SET assigned_jid=NULL, message_id=NULL, attempt=GREATEST(attempt-1,0)
 WHERE state='pending' AND assigned_jid IS NOT NULL`)
		if err != nil {
			return fmt.Errorf("reclaim recipients: %w", err)
		}
		n = int(ct.RowsAffected())
		return nil
	})
	return n, err
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestReclaimOrphanedAssignments -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/dispatch/reclaim.go internal/dispatch/reclaim_test.go
git commit -m "feat(dispatch): boot reclaim of orphaned assignments (pump crash recovery)"
```

---

### Task 5: main.go 装配（pump 模式分派 + filler + 排空）

**Files:**
- Modify: `cmd/wadist/main.go`
- Test: `cmd/wadist/main_smoke_test.go` (extend if it exercises run(); else rely on Task 6 integration)

**Interfaces:**
- Consumes: Task 2 `PumpEnqueuer`, Task 3 `Pump`, Task 4 `ReclaimOrphanedAssignments`, existing `Dispatcher`/`SendWorker`/`SendBodyResolver`.

- [ ] **Step 1: Wire the mode switch** (this task is integration wiring; its test is Task 6's e2e — for TDD here, add a smoke assertion that run() boots in pump mode without asynq)

In `cmd/wadist/main.go run()`, branch on `cfg.DispatchMode`:

```go
// ... after building `worker` (SendWorker) and the sendBodyResolver closure ...
sendResolver := func(ctx context.Context, campaignID int64) (string, string, string, []byte, error) {
	body, mediaSha, mime, err := mgr.CampaignSendable(ctx, campaignID)
	if err != nil {
		return "", "", "", nil, err
	}
	return body, mediaSha, mime, nil, nil
}

var dispatcher *dispatch.Dispatcher
if cfg.DispatchMode == "pump" {
	// boot recovery: reclaim orphaned assignments from a prior crash.
	if n, err := dispatch.ReclaimOrphanedAssignments(ctx, pool); err != nil {
		log.Printf("warn: reclaim orphaned assignments: %v", err)
	} else if n > 0 {
		log.Printf("pump boot: reclaimed %d orphaned assignments", n)
	}
	pe := dispatch.NewPumpEnqueuer(cfg.PumpBuffer)
	dispatcher = dispatch.NewDispatcher(pool, billingRepo, pe, priceFor, 3*time.Second).WithMetrics(m)
	pump := dispatch.NewPump(pe, worker, sendResolver, cfg.SendRate, cfg.SendWorkers)
	sup.Go(func(lctx context.Context) error { return pump.Run(lctx) })
	// filler: top up the buffer to keep workers fed, backpressure-driven (no 1s pulse).
	sup.Go(func(lctx context.Context) error {
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				pe.Close() // stop pump workers after drain
				return lctx.Err()
			case <-t.C:
				room := pe.Cap() - pe.Len()
				if room <= 0 {
					continue
				}
				if _, err := dispatcher.DispatchRunning(lctx, room); err != nil {
					log.Printf("pump filler: %v", err)
				}
			}
		}
	})
} else {
	// existing asynq path (unchanged): asynqClient/enqueuer/dispatcher/asynqSrv +
	// the 1s×100 DispatchRunning loop + RegisterSendHandler + asynqSrv.Run(mux).
	dispatcher = dispatch.NewDispatcher(pool, billingRepo, enqueuer, priceFor, 3*time.Second).WithMetrics(m)
	// ... keep all existing asynq wiring exactly as today ...
}
```
Guard the asynq-specific wiring (asynqClient, asynqSrv, mux, RegisterSendHandler, `go asynqSrv.Run(mux)`, the 1s dispatch loop, StopIntake=asynqSrv.Shutdown) so it only builds/runs in the `else` (asynq) branch. In pump mode, `StopIntake` should be `func() { pe.Close() }` (stop feeding; Supervisor then drains). Keep takeover asynq (node.TakeoverEnqueuer/handler) as-is — it's a separate queue, still needed in both modes; if that requires an asynq client, keep a minimal asynq client + server for the `takeover` queue only, or gate takeover behind the same asynq availability (decide: simplest is to keep the asynq server for the `takeover` queue in BOTH modes, and only the `default` send queue moves to the pump). **Document this: pump mode still runs asynq for the `takeover` queue; only the send path moves to the pump.**

- [ ] **Step 2: Verify build + smoke**

Run: `cd /var/klwa && go build ./... && go vet ./cmd/wadist/`
Expected: builds. If `main_smoke_test.go` drives `run()`, add a variant with `WADIST_DISPATCH_MODE=pump` asserting it boots + `stop()` returns cleanly (no PG/redis needed if smoke uses ephemeral; else mark as integration in Task 6).

- [ ] **Step 3: Commit**

```bash
cd /var/klwa
git add cmd/wadist/main.go cmd/wadist/main_smoke_test.go
git commit -m "feat(wadist): wire pump dispatch mode (reclaim + filler + drain), asynq default"
```

---

### Task 6: 端到端集成（pump 模式 recipients 流过 + 不变式）+ make gate

**Files:**
- Create: `internal/dispatch/pump_integration_test.go`

**Interfaces:**
- Consumes: PumpEnqueuer + Pump + Dispatcher + SendWorker via a real PG.

- [ ] **Step 1: Write the integration test** (running campaign → dispatchBatch fills pump → pump ProcessSend marks recipients sent; assert sent_today symmetric + message_id idempotent)

```go
func TestPump_EndToEnd(t *testing.T) {
	ctx := context.Background()
	pool := newDispatchTestPool(t)
	rec, worker := newRecordingWorker(t) // fake sender records sends; real billing/gate against PG+redis as existing worker tests do — reuse their harness

	// seed: running campaign + template + 3 pending recipients + 1 healthy account.
	seedRunningCampaignWith3Recipients(t, pool) // build on existing dispatch batch_test seed helpers

	pe := dispatch.NewPumpEnqueuer(16)
	d := dispatch.NewDispatcher(pool, billingRepo(t), pe, func(string) int64 { return 1 }, time.Second)
	resolve := func(ctx context.Context, cid int64) (string, string, string, []byte, error) { return "hi", "", "", nil, nil }
	p := dispatch.NewPump(pe, worker, resolve, 1000, 2)

	pctx, cancel := context.WithCancel(ctx)
	go p.Run(pctx)

	// one fill pass enqueues the 3 recipients into the pump.
	if _, err := d.DispatchRunning(ctx, 16); err != nil {
		t.Fatalf("fill: %v", err)
	}
	waitFor(t, func() bool { return rec.count() == 3 }, 3*time.Second)
	cancel()
	pe.Close()

	// invariants: all 3 recipients terminal 'sent'; each account's sent_today
	// reflects real sends (symmetric accounting held); a second DispatchRunning
	// enqueues nothing (idempotent — no pending left).
	assertAllRecipientsSent(t, pool)
	n, _ := d.DispatchRunning(ctx, 16)
	if n != 0 {
		t.Fatalf("second fill enqueued %d; want 0 (idempotent)", n)
	}
}
```
> Reuse the existing dispatch test harness (grep `internal/dispatch/batch_test.go` / `worker_test.go` for `billingRepo`, the PG+redis spin-up, seed helpers, and the recording fake sender). Do NOT invent a parallel harness.

- [ ] **Step 2: Run to verify it passes**

Run: `cd /var/klwa && go test ./internal/dispatch/ -run TestPump_EndToEnd -race -v`
Expected: PASS.

- [ ] **Step 3: Full gate**

Run: `cd /var/klwa && go build ./... && go vet ./... && make gate`
Expected: `make gate` GREEN. Default `WADIST_DISPATCH_MODE` unset → asynq path → existing dispatch tests unchanged.

- [ ] **Step 4: Commit**

```bash
cd /var/klwa
git add internal/dispatch/pump_integration_test.go
git commit -m "test(dispatch): pump e2e (recipients flow, sent_today symmetric, idempotent) + gate green"
```

---

## Self-Review 结论（作者已核对）

- **Spec 覆盖**（§5）：§5.1 目标（每秒稳定 τ、无脉冲、无 GC 尖峰）→ Task 3 令牌桶 worker 池；§5.2 结构 → Task 2/3（channel + rate.Limiter + ProcessSend）；§5.3「替换 asynq/批处理脉冲，保留 sendgate/billing/message_id/sent_today/状态机」→ 复用 Enqueuer 接缝（dispatchBatch 不改）+ Task 5 mode 分派；「去 asynq 恢复」→ Task 4 boot reclaim。§5 的账号驱动草图偏差已在 Global Constraints 显式记录并给出理由。
- **占位符扫描**：无 TODO/“类似上文”。测试助手名（newRecordingWorker/newDispatchTestPool/mustExec/seed*）标注为「复用现有 dispatch 测试 harness，名字不符时 grep 用真实等价物」——对齐现有设施，非占位。env 助手同理（grep envInt/envFloat）。
- **类型一致性**：`PumpEnqueuer`(NewPumpEnqueuer/EnqueueSend/C/Len/Cap/Close/ErrPumpFull)、`Pump`(NewPump/Run)、`ReclaimOrphanedAssignments`、`Config.DispatchMode/SendRate/PumpBuffer/SendWorkers` 跨 Task 一致；复用 `Enqueuer`/`SendPayload`/`SendWorker.ProcessSend`/`SendBodyResolver` 现有签名（实现时按真实 `SendBodyResolver` 对齐）。
- **零爆炸半径**：`DispatchMode` 默认 asynq；Task 5 用 `else` 分支原样保留全部 asynq 接线；pump 分支 opt-in。

## 已知风险与后续（非阻塞，记入执行账本）

- **takeover 队列仍用 asynq**：pump 模式只把 `default` 发送队列换成 Pump；`takeover` 队列仍走 asynq（M9 接管依赖）。Task 5 需保留最小 asynq server 供 takeover。文档化。
- **filler 50ms tick**：backpressure 由 channel 余量驱动；50ms 是上限步长，实际由 worker 消费速度自然收敛。可后续做成事件驱动（worker 消费后唤醒 filler）。
- **reclaim 竞态**：boot reclaim 假设单实例（本机高密度设计）；多实例共享 PG 时 reclaim 会误伤另一实例的在途 assignment——与单机前提一致，多机需门控（记录）。
- **SendRate 是上限非强制**：稳态吞吐由 sendgate 日配额 + 号池自然限流；令牌桶只削峰。Risk Governor 动态调 τ 留作后续（spec §6 ④，未来 spec）。
- **filler 与 pump 的 metrics**：可复用现有 `wadist_dispatch_batch_assigned` + send_outcomes；新增 pump 缓冲深度 gauge 留作后续观测增强。
