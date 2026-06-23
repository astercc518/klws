# M4: 对账 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 以 `wallet_ledger` 为唯一真相源,定时重算并校验每租户钱包是否漂移;漂移只告警/可选锁钱包,绝不自动改账。

**Architecture:** 两条独立不变式——①`balance=Σledger.delta_balance ∧ frozen=Σledger.delta_frozen`(记账侧),②`frozen=Σ charges.amount WHERE state∈{held,refund_pending}`(业务侧)。单租户对账用**一条 SQL**(单 MVCC 快照保证三聚合一致),全量用**集合查询**只返回漂移租户。前置:充值经可记账的 `Topup`,使不变式①成立。

**Tech Stack:** Go 1.22+,jackc/pgx/v5 (pgxpool),testcontainers-go。扩展既有 `internal/billing` 包。

## Global Constraints

- 见 `2026-06-23-wa-distribution-roadmap.md` 的 Global Constraints,逐条适用。
- 金额 int64 最小单位;`$1` 占位符;方言 "postgres";DB 调用带 ctx;错误 `%w` 包裹。
- 迁移自幂等(`IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS` / 偏索引 `IF NOT EXISTS`)。
- 对账**只读重算 + 告警**,绝不自动修账(金融修正须留人工)。
- 集成测试用 testcontainers;本地 `TESTCONTAINERS_RYUK_DISABLED=true`;`-race` 需 gcc(已装)。

## Dependencies(M3,已在 main)

- `billing.Repo`、`Hold/Settle/RequestRefund/Approve/RejectRefund`、`moveWallet`/`insertLedger`(`internal/billing/billing.go`)。
- 表 `tenant_wallets`/`billing_charges`/`wallet_ledger`/`refund_requests`;`ledger_kind_t` 含 `topup`/`adjust`;`wallet_ledger.charge_id` 可空。
- 测试助手 `testDSN`/`applyMigrations`/`seedWallet`(testsupport_test.go)、`newRepoWithSchema`/`walletState`/`hReq`(hold_test.go)、`chargeState`(settle_test.go)。

## VERIFIED API facts

- `pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(pgx.Tx) error) error`。
- `pgx.ErrNoRows` 判空行。`r.pool.Query/QueryRow/Exec`。
- 单条 SQL 语句在 READ COMMITTED 下走单一快照——`tenant_wallets`/`wallet_ledger`/`billing_charges` 三聚合在一条 SELECT 内一致求值。

## Scope 边界（显式）

- M4 交付:`Topup`(可记账充值)、`reconciliation_runs` 表、`Report`、`ReconcileTenant`、`ReconcileAll`、`DriftHandler`(含可选锁钱包)、`RunNightlyReconciliation`。
- 增量对账(checkpoint/xmin 水位)**不在 M4**——M4 用全量重算(正确优先);增量是后续优化。
- cron 调度器接线(实际定时触发)由部署侧 cron/asynq scheduler 负责;M4 提供可被调用的 `RunNightlyReconciliation`。

**Interfaces Produced:**
- `(*Repo).Topup(ctx, tenantID, amount int64, ref string) error`(幂等 via ref)
- `Report{ TenantID, WalletBalance, WalletFrozen, LedgerBalance, LedgerFrozen, ChargesFrozen, DriftBalance, DriftFrozen, DriftCharges int64 }`,`(Report).Healthy() bool`
- `(*Repo).ReconcileTenant(ctx, tenantID int64) (*Report, error)`(落审计)
- `(*Repo).ReconcileAll(ctx) ([]Report, error)`(只返回漂移租户)
- `ErrWalletLocked error`
- `DriftHandler`、`NewDriftHandler(repo *Repo, autoLock bool, notify func(context.Context, Report)) *DriftHandler`、`(*DriftHandler).HandleDrift(ctx, Report) error`
- `RunNightlyReconciliation(ctx, repo *Repo, h *DriftHandler) error`

## File Structure

- `internal/billing/billing.go` — 追加 `Topup`;Task 4 在 `Hold` 加 `locked` 守护 + `ErrWalletLocked`。
- `internal/billing/reconcile.go` — `Report`、`ReconcileTenant`、`ReconcileAll`、`DriftHandler`、`RunNightlyReconciliation`。
- `migrations/0004_reconciliation.sql` — `reconciliation_runs` 表 + `tenant_wallets.locked` 列。
- `internal/billing/*_test.go` — 各集成测试。

---

### Task 1: Topup（可记账、幂等充值）

**Files:**
- Modify: `internal/billing/billing.go`
- Test: `internal/billing/topup_test.go`

**Interfaces:**
- Produces: `(*Repo).Topup(ctx, tenantID, amount int64, ref string) error`。

- [ ] **Step 1: 写失败测试**

```go
// internal/billing/topup_test.go
package billing

import (
	"context"
	"testing"
)

func ledgerSum(t *testing.T, ctx context.Context, r *Repo, tenantID int64) (sumBal, sumFrz int64) {
	t.Helper()
	if err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(delta_balance),0), COALESCE(SUM(delta_frozen),0) FROM wallet_ledger WHERE tenant_id=$1`,
		tenantID).Scan(&sumBal, &sumFrz); err != nil {
		t.Fatalf("ledger sum: %v", err)
	}
	return
}

func TestTopup_CreditsAndLedgers(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := newRepoWithSchema(t)

	if err := r.Topup(ctx, 1, 1000, "pay-1"); err != nil {
		t.Fatalf("topup: %v", err)
	}
	bal, frz := walletState(t, ctx, pool, 1)
	if bal != 1000 || frz != 0 {
		t.Fatalf("after topup bal=%d frz=%d, want 1000/0", bal, frz)
	}
	// ledger reflects the credit so balance = Σdelta_balance holds
	sb, sf := ledgerSum(t, ctx, r, 1)
	if sb != 1000 || sf != 0 {
		t.Fatalf("ledger sum bal=%d frz=%d, want 1000/0", sb, sf)
	}
}

func TestTopup_IdempotentOnRef(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, pool := newRepoWithSchema(t)
	if err := r.Topup(ctx, 1, 1000, "pay-1"); err != nil {
		t.Fatalf("topup 1: %v", err)
	}
	if err := r.Topup(ctx, 1, 1000, "pay-1"); err != nil { // same ref
		t.Fatalf("topup 2: %v", err)
	}
	bal, _ := walletState(t, ctx, pool, 1)
	if bal != 1000 {
		t.Fatalf("after dup topup bal=%d, want 1000 (no double credit)", bal)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger WHERE idem_key='topup:pay-1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("topup ledger rows = %d, want 1", n)
	}
}

func TestTopup_RejectsNonPositive(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, _ := newRepoWithSchema(t)
	if err := r.Topup(ctx, 1, 0, "z"); err == nil {
		t.Fatal("expected error for non-positive topup")
	}
}
```

- [ ] **Step 2: 运行验证失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/billing/ -run TestTopup -v`
Expected: FAIL,`r.Topup undefined`。

- [ ] **Step 3: 实现 Topup（追加到 billing.go）**

```go
// Topup credits a tenant's balance and writes a 'topup' ledger row (charge_id
// NULL) so the reconciliation invariant balance=Σledger.delta_balance holds.
// Idempotent on ref (the payment reference): a replayed topup credits nothing.
func (r *Repo) Topup(ctx context.Context, tenantID, amount int64, ref string) error {
	if amount <= 0 {
		return fmt.Errorf("billing: topup amount must be positive, got %d", amount)
	}
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tenant_wallets (tenant_id) VALUES ($1) ON CONFLICT (tenant_id) DO NOTHING`,
			tenantID); err != nil {
			return fmt.Errorf("ensure wallet: %w", err)
		}
		var bal, frz int64
		if err := tx.QueryRow(ctx,
			`SELECT balance, frozen FROM tenant_wallets WHERE tenant_id=$1 FOR UPDATE`,
			tenantID).Scan(&bal, &frz); err != nil {
			return fmt.Errorf("lock wallet: %w", err)
		}
		// idempotency: this ref already applied?
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM wallet_ledger WHERE idem_key=$1)`,
			"topup:"+ref).Scan(&exists); err != nil {
			return fmt.Errorf("check topup idem: %w", err)
		}
		if exists {
			return nil // already credited
		}
		newBal := bal + amount
		if _, err := tx.Exec(ctx,
			`UPDATE tenant_wallets SET balance=$2, version=version+1 WHERE tenant_id=$1`,
			tenantID, newBal); err != nil {
			return fmt.Errorf("credit wallet: %w", err)
		}
		_, err := tx.Exec(ctx, `
INSERT INTO wallet_ledger
  (tenant_id, charge_id, kind, delta_balance, delta_frozen, balance_after, frozen_after, idem_key)
VALUES ($1, NULL, 'topup', $2, 0, $3, $4, $5)
ON CONFLICT (idem_key) DO NOTHING`,
			tenantID, amount, newBal, frz, "topup:"+ref)
		if err != nil {
			return fmt.Errorf("insert topup ledger: %w", err)
		}
		return nil
	})
}
```

- [ ] **Step 4: 运行验证通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/billing/ -run TestTopup -race -v`
Expected: PASS(充值+记账;同 ref 不双充;非正数报错)。

- [ ] **Step 5: 提交**

```bash
git add internal/billing/billing.go internal/billing/topup_test.go
git commit -m "feat(billing): ledgered idempotent Topup (enables reconciliation invariant)"
```

---

### Task 2: 迁移 0004 + Report + ReconcileTenant

**Files:**
- Create: `migrations/0004_reconciliation.sql`
- Create: `internal/billing/reconcile.go`
- Test: `internal/billing/reconcile_test.go`

**Interfaces:**
- Consumes: `Topup`(T1)、`Hold`(M3)。
- Produces: `Report` 结构 + `(Report).Healthy()`;`(*Repo).ReconcileTenant(ctx, tenantID int64) (*Report, error)`。

- [ ] **Step 1: 写迁移**

```sql
-- migrations/0004_reconciliation.sql
CREATE TABLE IF NOT EXISTS reconciliation_runs (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id      BIGINT NOT NULL,
    wallet_balance BIGINT NOT NULL,
    wallet_frozen  BIGINT NOT NULL,
    ledger_balance BIGINT NOT NULL,
    ledger_frozen  BIGINT NOT NULL,
    charges_frozen BIGINT NOT NULL,
    drift_balance  BIGINT NOT NULL,
    drift_frozen   BIGINT NOT NULL,
    drift_charges  BIGINT NOT NULL,
    healthy        BOOLEAN NOT NULL,
    checked_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_recon_drift ON reconciliation_runs (tenant_id, checked_at) WHERE healthy = FALSE;

-- 漂移时可锁钱包止损(Hold 侧据此拒新扣费)
ALTER TABLE tenant_wallets ADD COLUMN IF NOT EXISTS locked BOOLEAN NOT NULL DEFAULT FALSE;
```

- [ ] **Step 2: 写失败测试**

```go
// internal/billing/reconcile_test.go
package billing

import (
	"context"
	"testing"
)

// fundedRepo: fresh repo + a tenant funded via ledgered Topup(amount).
func fundedRepo(t *testing.T, tenantID, amount int64) (*Repo, context.Context) {
	t.Helper()
	r, ctx, _ := newRepoWithSchema(t)
	if err := r.Topup(ctx, tenantID, amount, "seed"); err != nil {
		t.Fatalf("seed topup: %v", err)
	}
	return r, ctx
}

func TestReconcileTenant_HealthyAfterHold(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx := fundedRepo(t, 1, 1000)
	if _, err := r.Hold(ctx, hReq(1, "m1", 300)); err != nil {
		t.Fatalf("hold: %v", err)
	}
	rep, err := r.ReconcileTenant(ctx, 1)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !rep.Healthy() {
		t.Fatalf("expected healthy, got drift bal=%d frz=%d charges=%d", rep.DriftBalance, rep.DriftFrozen, rep.DriftCharges)
	}
	// the run is audited
	var n int
	r.pool.QueryRow(ctx, `SELECT count(*) FROM reconciliation_runs WHERE tenant_id=1`).Scan(&n)
	if n != 1 {
		t.Fatalf("reconciliation_runs rows = %d, want 1", n)
	}
}

func TestReconcileTenant_DetectsBalanceDrift(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx := fundedRepo(t, 1, 1000)
	// inject drift: bump balance WITHOUT a ledger row
	if _, err := r.pool.Exec(ctx, `UPDATE tenant_wallets SET balance=balance+50 WHERE tenant_id=1`); err != nil {
		t.Fatalf("inject: %v", err)
	}
	rep, err := r.ReconcileTenant(ctx, 1)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.Healthy() {
		t.Fatal("expected drift, got healthy")
	}
	if rep.DriftBalance != 50 {
		t.Fatalf("drift_balance = %d, want 50", rep.DriftBalance)
	}
	var healthy bool
	r.pool.QueryRow(ctx, `SELECT healthy FROM reconciliation_runs WHERE tenant_id=1 ORDER BY id DESC LIMIT 1`).Scan(&healthy)
	if healthy {
		t.Fatal("audited run should be healthy=false")
	}
}

func TestReconcileTenant_DetectsFrozenVsChargesDrift(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx := fundedRepo(t, 1, 1000)
	r.Hold(ctx, hReq(1, "m1", 300)) // frozen=300, one held charge=300 → invariant② holds
	// corrupt invariant②: move the charge out of held WITHOUT touching frozen
	if _, err := r.pool.Exec(ctx, `UPDATE billing_charges SET state='settled' WHERE tenant_id=1 AND message_id='m1'`); err != nil {
		t.Fatalf("inject: %v", err)
	}
	rep, err := r.ReconcileTenant(ctx, 1)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// frozen(300) no longer equals Σ open-charge amounts(0)
	if rep.DriftCharges != 300 {
		t.Fatalf("drift_charges = %d, want 300", rep.DriftCharges)
	}
	if rep.Healthy() {
		t.Fatal("expected drift via invariant②")
	}
}
```

- [ ] **Step 3: 实现 reconcile.go**

```go
package billing

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Report is a single-tenant reconciliation result. Healthy iff all three drifts are zero.
type Report struct {
	TenantID      int64
	WalletBalance int64
	WalletFrozen  int64
	LedgerBalance int64 // Σ delta_balance
	LedgerFrozen  int64 // Σ delta_frozen
	ChargesFrozen int64 // Σ amount of charges in {held, refund_pending}
	DriftBalance  int64 // wallet - ledger
	DriftFrozen   int64
	DriftCharges  int64 // frozen - charges_frozen
}

func (r Report) Healthy() bool {
	return r.DriftBalance == 0 && r.DriftFrozen == 0 && r.DriftCharges == 0
}

// single statement → single MVCC snapshot → the three aggregates are consistent.
const reconcileOneSQL = `
SELECT
    w.balance, w.frozen,
    COALESCE(lb.sum_bal, 0), COALESCE(lb.sum_frz, 0),
    COALESCE(oc.frozen_charges, 0)
FROM tenant_wallets w
LEFT JOIN (
    SELECT tenant_id, SUM(delta_balance) AS sum_bal, SUM(delta_frozen) AS sum_frz
      FROM wallet_ledger WHERE tenant_id=$1 GROUP BY tenant_id
) lb ON lb.tenant_id = w.tenant_id
LEFT JOIN (
    SELECT tenant_id, SUM(amount) AS frozen_charges
      FROM billing_charges
     WHERE tenant_id=$1 AND state IN ('held','refund_pending') GROUP BY tenant_id
) oc ON oc.tenant_id = w.tenant_id
WHERE w.tenant_id=$1;`

// ReconcileTenant recomputes a tenant's wallet from the ledger + open charges,
// records an audit row, and returns the report. Read-only on money (never fixes).
func (r *Repo) ReconcileTenant(ctx context.Context, tenantID int64) (*Report, error) {
	rep := Report{TenantID: tenantID}
	err := r.pool.QueryRow(ctx, reconcileOneSQL, tenantID).Scan(
		&rep.WalletBalance, &rep.WalletFrozen,
		&rep.LedgerBalance, &rep.LedgerFrozen, &rep.ChargesFrozen)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("reconcile query: %w", err)
	}
	rep.DriftBalance = rep.WalletBalance - rep.LedgerBalance
	rep.DriftFrozen = rep.WalletFrozen - rep.LedgerFrozen
	rep.DriftCharges = rep.WalletFrozen - rep.ChargesFrozen

	if err := persistRun(ctx, r, rep); err != nil {
		return nil, err
	}
	return &rep, nil
}

func persistRun(ctx context.Context, r *Repo, rep Report) error {
	_, err := r.pool.Exec(ctx, `
INSERT INTO reconciliation_runs
  (tenant_id, wallet_balance, wallet_frozen, ledger_balance, ledger_frozen,
   charges_frozen, drift_balance, drift_frozen, drift_charges, healthy)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		rep.TenantID, rep.WalletBalance, rep.WalletFrozen, rep.LedgerBalance, rep.LedgerFrozen,
		rep.ChargesFrozen, rep.DriftBalance, rep.DriftFrozen, rep.DriftCharges, rep.Healthy())
	if err != nil {
		return fmt.Errorf("persist recon run: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: 运行验证通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/billing/ -run TestReconcileTenant -race -v`
Expected: PASS(健康;余额漂移=50;不变式②漂移=300;均落审计)。

- [ ] **Step 5: 提交**

```bash
git add migrations/0004_reconciliation.sql internal/billing/reconcile.go internal/billing/reconcile_test.go
git commit -m "feat(billing): ReconcileTenant single-snapshot drift detection + audit"
```

---

### Task 3: ReconcileAll（集合式，只返回漂移租户）

**Files:**
- Modify: `internal/billing/reconcile.go`
- Test: `internal/billing/reconcile_all_test.go`

**Interfaces:**
- Produces: `(*Repo).ReconcileAll(ctx) ([]Report, error)`。

- [ ] **Step 1: 写失败测试**

```go
// internal/billing/reconcile_all_test.go
package billing

import (
	"context"
	"testing"
)

func TestReconcileAll_ReturnsOnlyDrifting(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, _ := newRepoWithSchema(t)
	// tenant 1: healthy (funded + hold)
	r.Topup(ctx, 1, 1000, "s1")
	r.Hold(ctx, hReq(1, "m1", 300))
	// tenant 2: healthy
	r.Topup(ctx, 2, 500, "s2")
	// tenant 3: drifting (unledgered balance bump)
	r.Topup(ctx, 3, 800, "s3")
	if _, err := r.pool.Exec(ctx, `UPDATE tenant_wallets SET balance=balance+10 WHERE tenant_id=3`); err != nil {
		t.Fatalf("inject: %v", err)
	}

	drifts, err := r.ReconcileAll(ctx)
	if err != nil {
		t.Fatalf("reconcile all: %v", err)
	}
	if len(drifts) != 1 {
		t.Fatalf("drifting tenants = %d, want 1", len(drifts))
	}
	if drifts[0].TenantID != 3 || drifts[0].DriftBalance != 10 {
		t.Fatalf("drift = %+v, want tenant 3 drift_balance 10", drifts[0])
	}
}

func TestReconcileAll_AllHealthyReturnsEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, _ := newRepoWithSchema(t)
	r.Topup(ctx, 1, 1000, "s1")
	r.Hold(ctx, hReq(1, "m1", 300))
	r.Settle(ctx, 1, "m1")
	drifts, err := r.ReconcileAll(ctx)
	if err != nil {
		t.Fatalf("reconcile all: %v", err)
	}
	if len(drifts) != 0 {
		t.Fatalf("drifts = %d, want 0 (all healthy)", len(drifts))
	}
}
```

- [ ] **Step 2: 运行验证失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/billing/ -run TestReconcileAll -v`
Expected: FAIL,`r.ReconcileAll undefined`。

- [ ] **Step 3: 实现 ReconcileAll（追加到 reconcile.go）**

```go
const reconcileAllSQL = `
SELECT w.tenant_id, w.balance, w.frozen,
       COALESCE(lb.sum_bal,0), COALESCE(lb.sum_frz,0), COALESCE(oc.frozen_charges,0)
FROM tenant_wallets w
LEFT JOIN (
    SELECT tenant_id, SUM(delta_balance) sum_bal, SUM(delta_frozen) sum_frz
      FROM wallet_ledger GROUP BY tenant_id
) lb ON lb.tenant_id = w.tenant_id
LEFT JOIN (
    SELECT tenant_id, SUM(amount) frozen_charges
      FROM billing_charges WHERE state IN ('held','refund_pending') GROUP BY tenant_id
) oc ON oc.tenant_id = w.tenant_id
WHERE w.balance <> COALESCE(lb.sum_bal,0)
   OR w.frozen  <> COALESCE(lb.sum_frz,0)
   OR w.frozen  <> COALESCE(oc.frozen_charges,0);`

// ReconcileAll returns every drifting tenant in one pass. Empty slice = all healthy.
// Does NOT persist per-tenant audit rows (DriftHandler records the drifting ones).
func (r *Repo) ReconcileAll(ctx context.Context) ([]Report, error) {
	rows, err := r.pool.Query(ctx, reconcileAllSQL)
	if err != nil {
		return nil, fmt.Errorf("reconcile all: %w", err)
	}
	defer rows.Close()

	var drifts []Report
	for rows.Next() {
		var rep Report
		if err := rows.Scan(&rep.TenantID, &rep.WalletBalance, &rep.WalletFrozen,
			&rep.LedgerBalance, &rep.LedgerFrozen, &rep.ChargesFrozen); err != nil {
			return nil, fmt.Errorf("scan drift: %w", err)
		}
		rep.DriftBalance = rep.WalletBalance - rep.LedgerBalance
		rep.DriftFrozen = rep.WalletFrozen - rep.LedgerFrozen
		rep.DriftCharges = rep.WalletFrozen - rep.ChargesFrozen
		drifts = append(drifts, rep)
	}
	return drifts, rows.Err()
}
```

- [ ] **Step 4: 运行验证通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/billing/ -run TestReconcileAll -race -v`
Expected: PASS(3 租户仅 1 漂移被返回;全健康返回空)。

- [ ] **Step 5: 提交**

```bash
git add internal/billing/reconcile.go internal/billing/reconcile_all_test.go
git commit -m "feat(billing): set-based ReconcileAll returns only drifting tenants"
```

---

### Task 4: DriftHandler + 钱包锁（Hold 守护）

**Files:**
- Modify: `internal/billing/billing.go`(Hold 加 locked 守护 + `ErrWalletLocked`)
- Modify: `internal/billing/reconcile.go`(DriftHandler)
- Test: `internal/billing/drift_test.go`

**Interfaces:**
- Produces: `ErrWalletLocked`;`DriftHandler`、`NewDriftHandler`、`(*DriftHandler).HandleDrift`。

- [ ] **Step 1: 写失败测试**

```go
// internal/billing/drift_test.go
package billing

import (
	"context"
	"errors"
	"testing"
)

func TestHold_RejectsLockedWallet(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx := fundedRepo(t, 1, 1000)
	if _, err := r.pool.Exec(ctx, `UPDATE tenant_wallets SET locked=TRUE WHERE tenant_id=1`); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if _, err := r.Hold(ctx, hReq(1, "m1", 300)); !errors.Is(err, ErrWalletLocked) {
		t.Fatalf("hold err = %v, want ErrWalletLocked", err)
	}
}

func TestDriftHandler_AuditsLocksNotifies(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx := fundedRepo(t, 1, 1000)
	if _, err := r.pool.Exec(ctx, `UPDATE tenant_wallets SET balance=balance+10 WHERE tenant_id=1`); err != nil {
		t.Fatalf("inject: %v", err)
	}
	drifts, _ := r.ReconcileAll(ctx)
	if len(drifts) != 1 {
		t.Fatalf("expected 1 drift, got %d", len(drifts))
	}

	var notified int64
	h := NewDriftHandler(r, true /*autoLock*/, func(_ context.Context, rep Report) { notified = rep.TenantID })
	if err := h.HandleDrift(ctx, drifts[0]); err != nil {
		t.Fatalf("handle: %v", err)
	}
	// audited as unhealthy
	var n int
	r.pool.QueryRow(ctx, `SELECT count(*) FROM reconciliation_runs WHERE tenant_id=1 AND healthy=FALSE`).Scan(&n)
	if n != 1 {
		t.Fatalf("unhealthy audit rows = %d, want 1", n)
	}
	// wallet locked
	var locked bool
	r.pool.QueryRow(ctx, `SELECT locked FROM tenant_wallets WHERE tenant_id=1`).Scan(&locked)
	if !locked {
		t.Fatal("wallet should be locked after drift with autoLock")
	}
	// notified
	if notified != 1 {
		t.Fatalf("notify tenant = %d, want 1", notified)
	}
}
```

- [ ] **Step 2: 运行验证失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/billing/ -run 'TestHold_RejectsLockedWallet|TestDriftHandler' -v`
Expected: FAIL,`undefined: ErrWalletLocked` / `NewDriftHandler`。

- [ ] **Step 3a: Hold 加 locked 守护（修改 billing.go）**

在 `billing.go` 的错误哨兵处追加:

```go
var ErrWalletLocked = errors.New("billing: wallet is locked (reconciliation drift)")
```

在 `Hold` 内,把锁钱包那段从:

```go
		var bal, frz int64
		if err := tx.QueryRow(ctx,
			`SELECT balance, frozen FROM tenant_wallets WHERE tenant_id=$1 FOR UPDATE`,
			h.TenantID).Scan(&bal, &frz); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock wallet: %w", err)
		}
		if bal < h.Amount {
			return ErrInsufficientFunds // rollback also drops the charge insert
		}
```

改为(多读 `locked` 并在锁定时拒绝):

```go
		var bal, frz int64
		var locked bool
		if err := tx.QueryRow(ctx,
			`SELECT balance, frozen, locked FROM tenant_wallets WHERE tenant_id=$1 FOR UPDATE`,
			h.TenantID).Scan(&bal, &frz, &locked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock wallet: %w", err)
		}
		if locked {
			return ErrWalletLocked // drift-locked wallet rejects new deductions
		}
		if bal < h.Amount {
			return ErrInsufficientFunds // rollback also drops the charge insert
		}
```

> 仅 `Hold`(新扣费)受 `locked` 影响;`Settle`/退款(处理在途 charge)不应被锁阻断,故 `moveWallet` 不查 `locked`。

- [ ] **Step 3b: DriftHandler（追加到 reconcile.go）**

```go
// DriftHandler records drift, optionally locks the wallet to stop further
// deductions, and fires a notification. It never fixes the books.
type DriftHandler struct {
	repo     *Repo
	autoLock bool
	notify   func(context.Context, Report)
}

func NewDriftHandler(repo *Repo, autoLock bool, notify func(context.Context, Report)) *DriftHandler {
	return &DriftHandler{repo: repo, autoLock: autoLock, notify: notify}
}

// HandleDrift persists an unhealthy audit row, optionally locks the wallet, and
// notifies. Returns an error only on a DB failure of the audit/lock writes.
func (h *DriftHandler) HandleDrift(ctx context.Context, rep Report) error {
	if err := persistRun(ctx, h.repo, rep); err != nil {
		return err
	}
	if h.autoLock {
		if _, err := h.repo.pool.Exec(ctx,
			`UPDATE tenant_wallets SET locked=TRUE WHERE tenant_id=$1`, rep.TenantID); err != nil {
			return fmt.Errorf("lock wallet: %w", err)
		}
	}
	if h.notify != nil {
		h.notify(ctx, rep) // hand off to humans; never auto-correct
	}
	return nil
}
```

- [ ] **Step 4: 运行验证通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/billing/ -run 'TestHold_RejectsLockedWallet|TestDriftHandler' -race -v`
Expected: PASS(锁定钱包拒 Hold;漂移落审计+锁钱包+通知)。

- [ ] **Step 5: 提交**

```bash
git add internal/billing/billing.go internal/billing/reconcile.go internal/billing/drift_test.go
git commit -m "feat(billing): DriftHandler (audit/lock/notify) + Hold locked-wallet guard"
```

---

### Task 5: RunNightlyReconciliation（编排）

**Files:**
- Modify: `internal/billing/reconcile.go`
- Test: `internal/billing/nightly_test.go`

**Interfaces:**
- Consumes: `ReconcileAll`(T3)、`DriftHandler.HandleDrift`(T4)。
- Produces: `RunNightlyReconciliation(ctx, repo *Repo, h *DriftHandler) (drifted int, err error)`。

- [ ] **Step 1: 写失败测试**

```go
// internal/billing/nightly_test.go
package billing

import (
	"context"
	"testing"
)

func TestRunNightlyReconciliation_HandlesAllDrifts(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	r, ctx, _ := newRepoWithSchema(t)
	r.Topup(ctx, 1, 1000, "s1") // healthy
	r.Topup(ctx, 2, 500, "s2")
	r.pool.Exec(ctx, `UPDATE tenant_wallets SET balance=balance+7 WHERE tenant_id=2`) // drift
	r.Topup(ctx, 3, 200, "s3")
	r.pool.Exec(ctx, `UPDATE tenant_wallets SET balance=balance+9 WHERE tenant_id=3`) // drift

	var handled int
	h := NewDriftHandler(r, false, func(_ context.Context, _ Report) { handled++ })
	n, err := RunNightlyReconciliation(ctx, r, h)
	if err != nil {
		t.Fatalf("nightly: %v", err)
	}
	if n != 2 || handled != 2 {
		t.Fatalf("drifted=%d handled=%d, want 2/2", n, handled)
	}
}
```

- [ ] **Step 2: 运行验证失败**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/billing/ -run TestRunNightlyReconciliation -v`
Expected: FAIL,`undefined: RunNightlyReconciliation`。

- [ ] **Step 3: 实现（追加到 reconcile.go）**

```go
// RunNightlyReconciliation reconciles all tenants and handles each drift.
// A single tenant's handler error does not abort the rest; returns the count
// of drifting tenants and the first handler error encountered (if any).
func RunNightlyReconciliation(ctx context.Context, repo *Repo, h *DriftHandler) (int, error) {
	drifts, err := repo.ReconcileAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("nightly reconcile: %w", err)
	}
	var firstErr error
	for _, d := range drifts {
		if err := h.HandleDrift(ctx, d); err != nil && firstErr == nil {
			firstErr = err // record but keep going; one bad tenant must not block others
		}
	}
	return len(drifts), firstErr
}
```

- [ ] **Step 4: 运行验证通过**

Run: `TESTCONTAINERS_RYUK_DISABLED=true go test ./internal/billing/ -race -v`
Expected: PASS(2 漂移全部处置;全套绿)。

- [ ] **Step 5: 提交**

```bash
git add internal/billing/reconcile.go internal/billing/nightly_test.go
git commit -m "feat(billing): RunNightlyReconciliation orchestration"
```

---

## Self-Review

- **Spec 覆盖**:Topup(前置)✓(T1)、reconciliation_runs + ReconcileTenant 单快照 ✓(T2)、ReconcileAll 集合式 ✓(T3)、DriftHandler 告警+可选锁钱包 ✓(T4)、夜间编排 ✓(T5)。roadmap M4 全覆盖(增量对账显式排除)。
- **占位符扫描**:无 TBD/TODO,每步完整代码与命令。
- **不变式**:①`balance=Σdelta_balance ∧ frozen=Σdelta_frozen`(Topup 使开户余额入账,成立);②`frozen=Σ held/refund_pending charge`;两条独立交叉验证,测试分别注入漂移触发。
- **类型一致性**:`Report` 字段、`ReconcileTenant/ReconcileAll/Topup/HandleDrift/RunNightlyReconciliation` 签名跨任务一致;`persistRun` 在 T2 定义、T4 复用;`ErrWalletLocked` 在 T4 定义并被 Hold/测试引用。
- **只读重算**:对账绝不改钱包/分录;唯一的写是审计行 + 可选 `locked=TRUE`(止损,非改账)。
- **范围边界(显式)**:全量重算(非增量);cron 实际触发由部署侧负责;Topup 幂等靠 ref。
