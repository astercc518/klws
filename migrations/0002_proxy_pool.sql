-- migrations/0002_proxy_pool.sql
DO $$ BEGIN
    CREATE TYPE proxy_type_t AS ENUM ('socks5','http','https');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS proxy_pool (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    proxy_url        TEXT        NOT NULL UNIQUE,
    proxy_type       proxy_type_t NOT NULL DEFAULT 'socks5',
    country_code     CHAR(2)     NOT NULL,
    is_alive         BOOLEAN     NOT NULL DEFAULT TRUE,
    latency_ms       INT         NOT NULL DEFAULT 0,
    usage_count      BIGINT      NOT NULL DEFAULT 0,
    failure_count    INT         NOT NULL DEFAULT 0,
    max_bindings     INT         NOT NULL DEFAULT 1,
    current_bindings INT         NOT NULL DEFAULT 0,
    last_check_at    TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_bindings CHECK (current_bindings >= 0 AND current_bindings <= max_bindings)
);

CREATE INDEX IF NOT EXISTS idx_proxy_available
    ON proxy_pool (country_code, usage_count)
    WHERE is_alive = TRUE AND current_bindings < max_bindings;

ALTER TABLE account_devices ADD COLUMN IF NOT EXISTS proxy_id BIGINT
    REFERENCES proxy_pool(id) ON DELETE SET NULL;
ALTER TABLE account_devices ADD COLUMN IF NOT EXISTS proxy_url_cache TEXT;
CREATE INDEX IF NOT EXISTS idx_acc_proxy ON account_devices (proxy_id);

DROP TRIGGER IF EXISTS trg_proxy_touch ON proxy_pool;
CREATE TRIGGER trg_proxy_touch BEFORE UPDATE ON proxy_pool
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
