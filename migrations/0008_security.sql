-- 0008_security.sql — encryption keys, PII columns, RLS, suppression, audit.

-- ── tenant_keys: KEK-wrapped per-tenant data keys (DEKs); delete = crypto-shred ──
CREATE TABLE IF NOT EXISTS tenant_keys (
    tenant_id  BIGINT NOT NULL,
    version    INT    NOT NULL,
    key_enc    BYTEA  NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, version)
);

-- ── RLS roles (idempotent) ──
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='app_tenant') THEN
    CREATE ROLE app_tenant NOSUPERUSER NOINHERIT LOGIN PASSWORD 'app_tenant_pw';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='app_system') THEN
    CREATE ROLE app_system NOSUPERUSER NOINHERIT LOGIN PASSWORD 'app_system_pw' BYPASSRLS;
  END IF;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_tenant, app_system;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_tenant, app_system;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_tenant, app_system;
-- audit_log is append-only even for these roles (see Task 4):
-- (REVOKE UPDATE, DELETE ON audit_log added in Task 4 after the table exists)

-- ── enable RLS + tenant-isolation policy on the 9 tenant-scoped tables ──
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'account_devices','tenant_wallets','billing_charges','wallet_ledger',
    'refund_requests','reconciliation_runs','campaign_templates','campaigns','campaign_recipients'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format($f$CREATE POLICY tenant_isolation ON %I
      USING (tenant_id = current_setting('app.current_tenant_id', true)::bigint)
      WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::bigint)$f$, t);
  END LOOP;
END $$;
