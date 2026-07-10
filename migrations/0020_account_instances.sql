-- 0020: Evolution 换栈路由表。Go 侧只存 instance↔jid↔node 路由与状态，
-- 不存 Signal 会话（会话在 Evolution 侧 Redis）。replay-all 幂等。
CREATE TABLE IF NOT EXISTS account_instances (
    instance_name text PRIMARY KEY,
    jid           text,
    tenant_id     bigint NOT NULL,
    evo_node      text   NOT NULL DEFAULT 'default',
    proxy_id      bigint,
    state         text   NOT NULL DEFAULT 'created',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- 一个 jid 最多绑一个 instance（防双绑；NULL jid 不受约束，允许多未配对实例）。
CREATE UNIQUE INDEX IF NOT EXISTS uq_account_instances_jid
    ON account_instances (jid) WHERE jid IS NOT NULL;

CREATE INDEX IF NOT EXISTS ix_account_instances_tenant
    ON account_instances (tenant_id);

ALTER TABLE account_instances ENABLE ROW LEVEL SECURITY;
ALTER TABLE account_instances FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON account_instances;
CREATE POLICY tenant_isolation ON account_instances
    USING (tenant_id = current_setting('app.current_tenant_id', true)::bigint)
    WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::bigint);
