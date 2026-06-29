-- migrations/0014_risk_config.sql
-- Global risk / anti-ban policy, edited from the admin "风控策略中心".
--
-- CONTROL-PLANE ONLY: this row stores the admin's intended policy and is the
-- single source of truth the dispatch/sendgate engine MAY read in a future
-- integration. It does NOT modify any engine logic on its own — the running
-- dispatcher still uses its compiled defaults until that wiring lands. Kept out
-- of the red-line packages: only this additive table + an api-layer SystemPool
-- upsert touch it.
--
-- Singleton: a single row pinned at id = 1.
CREATE TABLE IF NOT EXISTS system_risk_config (
    id                       INT              PRIMARY KEY DEFAULT 1,
    min_delay_seconds        INT              NOT NULL DEFAULT 3,
    max_delay_seconds        INT              NOT NULL DEFAULT 8,
    daily_limit_per_device   INT              NOT NULL DEFAULT 1000,
    ban_rate_circuit_breaker DOUBLE PRECISION NOT NULL DEFAULT 0.15,
    updated_at               TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_by               BIGINT,
    CONSTRAINT system_risk_config_singleton CHECK (id = 1),
    CONSTRAINT chk_risk_delays   CHECK (min_delay_seconds >= 0 AND max_delay_seconds >= min_delay_seconds),
    CONSTRAINT chk_risk_daily    CHECK (daily_limit_per_device > 0),
    CONSTRAINT chk_risk_banrate  CHECK (ban_rate_circuit_breaker >= 0 AND ban_rate_circuit_breaker <= 1)
);

-- Seed the singleton with engine-aligned defaults (base gap ≈ 3s, ladder cap
-- 1000/day, 15% breaker per the spec). Idempotent.
INSERT INTO system_risk_config (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
