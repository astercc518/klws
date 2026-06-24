-- 0008_security.sql — encryption keys, PII columns, RLS, suppression, audit.

-- ── tenant_keys: KEK-wrapped per-tenant data keys (DEKs); delete = crypto-shred ──
CREATE TABLE IF NOT EXISTS tenant_keys (
    tenant_id  BIGINT NOT NULL,
    version    INT    NOT NULL,
    key_enc    BYTEA  NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, version)
);
