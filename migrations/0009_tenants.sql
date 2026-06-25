-- migrations/0009_tenants.sql — tenant registry for the web platform.
-- Tenants are NOT RLS-scoped to themselves; they are platform metadata
-- accessed by staff via the BYPASSRLS app_system pool.
CREATE TABLE IF NOT EXISTS tenants (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT        NOT NULL,
    status         TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended')),
    sales_owner_id BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

DROP TRIGGER IF EXISTS trg_tenants_touch ON tenants;
CREATE TRIGGER trg_tenants_touch BEFORE UPDATE ON tenants
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
