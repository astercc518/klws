-- migrations/0006_dispatch.sql
ALTER TABLE account_devices ADD COLUMN IF NOT EXISTS sent_today INT NOT NULL DEFAULT 0;

DO $$ BEGIN CREATE TYPE campaign_state_t  AS ENUM ('draft','running','paused','completed','failed'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE TYPE recipient_state_t AS ENUM ('pending','sent','failed','skipped'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE TABLE IF NOT EXISTS campaign_templates (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id  BIGINT NOT NULL,
    kind       TEXT   NOT NULL,
    body       TEXT   NOT NULL,
    media_sha  CHAR(64),
    media_mime TEXT
);

CREATE TABLE IF NOT EXISTS campaigns (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   BIGINT NOT NULL,
    template_id BIGINT NOT NULL REFERENCES campaign_templates(id),
    state       campaign_state_t NOT NULL DEFAULT 'draft',
    total       INT NOT NULL DEFAULT 0,
    sent        INT NOT NULL DEFAULT 0,
    failed      INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS campaign_recipients (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    campaign_id  BIGINT NOT NULL REFERENCES campaigns(id),
    tenant_id    BIGINT NOT NULL,
    phone        TEXT   NOT NULL,
    country_code CHAR(2) NOT NULL,
    vars         JSONB  NOT NULL DEFAULT '{}',
    assigned_jid TEXT,
    state        recipient_state_t NOT NULL DEFAULT 'pending',
    message_id   TEXT,
    attempt      INT NOT NULL DEFAULT 0,
    last_error   TEXT,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (campaign_id, phone)
);
CREATE INDEX IF NOT EXISTS idx_recip_pending ON campaign_recipients (campaign_id) WHERE state = 'pending';

CREATE TABLE IF NOT EXISTS media_uploads (
    account_jid     TEXT   NOT NULL,
    media_sha       CHAR(64) NOT NULL,
    url             TEXT   NOT NULL,
    direct_path     TEXT   NOT NULL,
    media_key       BYTEA  NOT NULL,
    file_sha256     BYTEA  NOT NULL,
    file_enc_sha256 BYTEA  NOT NULL,
    file_length     BIGINT NOT NULL,
    uploaded_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_jid, media_sha)
);

DROP TRIGGER IF EXISTS trg_recip_touch ON campaign_recipients;
CREATE TRIGGER trg_recip_touch BEFORE UPDATE ON campaign_recipients
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
