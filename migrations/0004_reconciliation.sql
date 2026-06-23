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
