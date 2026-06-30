-- migrations/0015_risk_breaker_opts.sql
-- Control-plane knobs for the (future) ban-rate circuit breaker. See
-- docs/RISK-CIRCUIT-BREAKER-DESIGN-zh.md. Purely additive columns on the
-- existing singleton config row — no engine logic, no red-line package touched.
--
-- Defaults are "safe by default": the breaker ships DISABLED, and when enabled
-- it starts in DRY-RUN (observe-only) so an operator can calibrate the
-- threshold/window/sample before it ever pauses a real campaign.
ALTER TABLE system_risk_config
    ADD COLUMN IF NOT EXISTS circuit_breaker_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE system_risk_config
    ADD COLUMN IF NOT EXISTS circuit_breaker_dry_run BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE system_risk_config
    ADD COLUMN IF NOT EXISTS min_sample INT NOT NULL DEFAULT 20;
ALTER TABLE system_risk_config
    ADD COLUMN IF NOT EXISTS window_seconds INT NOT NULL DEFAULT 900;
ALTER TABLE system_risk_config
    ADD COLUMN IF NOT EXISTS eval_interval_seconds INT NOT NULL DEFAULT 20;

-- Sanity bounds (backstop for the api-layer validation).
DO $$ BEGIN
    ALTER TABLE system_risk_config
        ADD CONSTRAINT chk_risk_breaker_bounds
        CHECK (min_sample >= 1 AND window_seconds >= 1 AND eval_interval_seconds >= 1);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
