-- migrations/0001_account_devices.sql
DO $$ BEGIN
    CREATE TYPE ban_status_t AS ENUM ('active','flagged','banned','logged_out','init');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS account_devices (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id         BIGINT      NOT NULL,
    account_jid       TEXT        NOT NULL UNIQUE,
    phone_number      TEXT        NOT NULL,
    push_name         TEXT,
    ban_status        ban_status_t NOT NULL DEFAULT 'init',
    ban_checked_at    TIMESTAMPTZ,
    owner_node        TEXT,
    last_connected_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_acc_tenant ON account_devices (tenant_id);

CREATE OR REPLACE FUNCTION touch_updated_at() RETURNS trigger AS $$
BEGIN NEW.updated_at = now(); RETURN NEW; END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_acc_touch ON account_devices;
CREATE TRIGGER trg_acc_touch BEFORE UPDATE ON account_devices
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
