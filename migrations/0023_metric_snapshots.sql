-- 0023: metric_snapshots — 平台级运维时间序列(worker 定时采样落库,console 读趋势)。
-- 无 RLS:平台级数据,SystemPool(BYPASSRLS 的 app_system)专用,照 audit_log 的 GRANT/REVOKE 模式。
CREATE TABLE IF NOT EXISTS metric_snapshots (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 瞬时快照(全平台聚合;tenant_id NULL=平台级,非空=按租户切片留后续)。第一版只写 NULL。
    tenant_id            BIGINT,
    accounts_total       INT NOT NULL,
    accounts_active      INT NOT NULL,
    accounts_banned      INT NOT NULL,   -- banned+flagged
    accounts_quarantined INT NOT NULL,
    queue_backlog        INT NOT NULL,   -- campaign_recipients state=pending
    processed_1h         INT NOT NULL,   -- 近1h 处理量(state IN sent/failed/skipped, updated_at 锚点)
    delivered_1h         INT NOT NULL,   -- 近1h 到达(delivered_at 锚点,精确)
    failed_1h            INT NOT NULL,   -- 近1h 失败(state=failed, updated_at)
    avg_delivery_ms      INT             -- 近1h 平均到达延迟(delivered_at - created_at, ms, 可空)
);
CREATE INDEX IF NOT EXISTS ix_metric_snapshots_captured ON metric_snapshots (captured_at DESC);

-- 平台级运维表:app_tenant 永不读(照 0010/0011/0022 对平台专属表的 REVOKE);
-- app_customer 目前未在任何迁移中创建(仅 app_tenant/app_system 两角色,见 0008),这里预留同款
-- REVOKE 以便该角色日后出现时无需追加迁移。
REVOKE ALL ON metric_snapshots FROM app_tenant;
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_customer') THEN
    EXECUTE 'REVOKE ALL ON metric_snapshots FROM app_customer';
  END IF;
END $$;
