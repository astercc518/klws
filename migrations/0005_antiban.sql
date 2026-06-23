-- migrations/0005_antiban.sql
ALTER TABLE account_devices ADD COLUMN IF NOT EXISTS registered_at     TIMESTAMPTZ;
ALTER TABLE account_devices ADD COLUMN IF NOT EXISTS health_score      INT NOT NULL DEFAULT 100;
ALTER TABLE account_devices ADD COLUMN IF NOT EXISTS quarantined_until TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_acc_quarantine ON account_devices (quarantined_until)
    WHERE quarantined_until IS NOT NULL;

-- effective_quota: 号龄基础配额 × 健康度折扣。与 Go warmupQuota/EffectiveQuota 同曲线,须同步维护。
CREATE OR REPLACE FUNCTION effective_quota(reg TIMESTAMPTZ, health INT) RETURNS INT AS $$
DECLARE d NUMERIC; base INT;
BEGIN
    IF reg IS NULL THEN d := 0; ELSE d := EXTRACT(EPOCH FROM (now() - reg)) / 86400; END IF;
    base := CASE
        WHEN d < 2  THEN 20  WHEN d < 4  THEN 50
        WHEN d < 8  THEN 100 WHEN d < 15 THEN 250
        ELSE 1000 END;
    RETURN GREATEST(1, (base * health) / 100);
END $$ LANGUAGE plpgsql STABLE;
