DO $$ BEGIN CREATE TYPE charge_state_t AS ENUM ('held','settled','refund_pending','refunded','rejected'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE TYPE refund_state_t AS ENUM ('pending','approved','rejected'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE TYPE ledger_kind_t  AS ENUM ('hold','settle','refund','reject','topup','adjust'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE TABLE IF NOT EXISTS tenant_wallets (
    tenant_id  BIGINT PRIMARY KEY,
    balance    BIGINT      NOT NULL DEFAULT 0,
    frozen     BIGINT      NOT NULL DEFAULT 0,
    currency   CHAR(3)     NOT NULL DEFAULT 'USD',
    version    BIGINT      NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_wallet_nonneg CHECK (balance >= 0 AND frozen >= 0)
);

CREATE TABLE IF NOT EXISTS billing_charges (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id    BIGINT  NOT NULL,
    account_jid  TEXT    NOT NULL,
    message_id   TEXT    NOT NULL,
    country_code CHAR(2) NOT NULL,
    amount       BIGINT  NOT NULL CHECK (amount > 0),
    state        charge_state_t NOT NULL DEFAULT 'held',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_charge_state ON billing_charges (state);

CREATE TABLE IF NOT EXISTS wallet_ledger (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     BIGINT NOT NULL,
    charge_id     BIGINT REFERENCES billing_charges(id),
    kind          ledger_kind_t NOT NULL,
    delta_balance BIGINT NOT NULL,
    delta_frozen  BIGINT NOT NULL,
    balance_after BIGINT NOT NULL,
    frozen_after  BIGINT NOT NULL,
    idem_key      TEXT   NOT NULL UNIQUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS refund_requests (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    charge_id    BIGINT NOT NULL UNIQUE REFERENCES billing_charges(id),
    tenant_id    BIGINT NOT NULL,
    amount       BIGINT NOT NULL,
    reason       TEXT   NOT NULL,
    state        refund_state_t NOT NULL DEFAULT 'pending',
    reviewed_by  BIGINT,
    review_note  TEXT,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_refund_pending ON refund_requests (tenant_id, requested_at) WHERE state='pending';

DROP TRIGGER IF EXISTS trg_charge_touch ON billing_charges;
CREATE TRIGGER trg_charge_touch BEFORE UPDATE ON billing_charges
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
DROP TRIGGER IF EXISTS trg_wallet_touch ON tenant_wallets;
CREATE TRIGGER trg_wallet_touch BEFORE UPDATE ON tenant_wallets
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
