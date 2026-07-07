-- Commission rates: per-sales default rate, per-tenant override rate.
-- Fraction stored as NUMERIC(5,4), e.g. 0.1000 = 10%.
ALTER TABLE console_users ADD COLUMN IF NOT EXISTS commission_rate NUMERIC(5,4);
ALTER TABLE tenants       ADD COLUMN IF NOT EXISTS commission_rate NUMERIC(5,4);
