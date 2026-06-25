-- migrations/0011_tenant_pricing.sql — per-tenant×country unit price (minor units).
-- Platform table: set by staff via SystemPool; never exposed to the RLS tenant role.
CREATE TABLE IF NOT EXISTS tenant_pricing (
    tenant_id    BIGINT      NOT NULL REFERENCES tenants(id),
    country_code CHAR(2)     NOT NULL,
    unit_price   BIGINT      NOT NULL CHECK (unit_price > 0),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, country_code)
);

DROP TRIGGER IF EXISTS trg_tenant_pricing_touch ON tenant_pricing;
CREATE TRIGGER trg_tenant_pricing_touch BEFORE UPDATE ON tenant_pricing
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

REVOKE ALL ON tenant_pricing FROM app_tenant;
