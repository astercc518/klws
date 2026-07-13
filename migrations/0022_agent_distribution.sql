-- 0022: 多级代理分销。console_users 扩层级+信用额度；成本价/划拨台账/月结单。replay-all 幂等。
ALTER TABLE console_users ADD COLUMN IF NOT EXISTS parent_id   BIGINT REFERENCES console_users(id);
ALTER TABLE console_users ADD COLUMN IF NOT EXISTS credit_limit BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_console_users_parent ON console_users (parent_id);

-- 平台成本价/国家（minor units）。
CREATE TABLE IF NOT EXISTS agent_cost_pricing (
    country_code CHAR(2) PRIMARY KEY,
    unit_cost    BIGINT  NOT NULL CHECK (unit_cost >= 0),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
DROP TRIGGER IF EXISTS trg_agent_cost_touch ON agent_cost_pricing;
CREATE TRIGGER trg_agent_cost_touch BEFORE UPDATE ON agent_cost_pricing
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- 划拨台账（append-only）：代理给客户 tenant_wallet 垫付的零售额度。
CREATE TABLE IF NOT EXISTS agent_allocations (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id   BIGINT NOT NULL REFERENCES console_users(id),
    tenant_id  BIGINT NOT NULL REFERENCES tenants(id),
    amount     BIGINT NOT NULL CHECK (amount > 0),
    actor_id   BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_alloc_agent ON agent_allocations (agent_id);

-- 月结单（每代理每月一行）。
CREATE TABLE IF NOT EXISTS agent_settlements (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id   BIGINT NOT NULL REFERENCES console_users(id),
    period     CHAR(7) NOT NULL,             -- 'YYYY-MM' (Asia/Shanghai)
    debt       BIGINT NOT NULL,             -- 成本口径应付平台
    margin     BIGINT NOT NULL,             -- 直属差价
    rebate     BIGINT NOT NULL,             -- 子树返佣
    net        BIGINT NOT NULL,             -- margin+rebate-debt
    status     TEXT   NOT NULL DEFAULT 'open' CHECK (status IN ('open','settled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (agent_id, period)
);

-- app_tenant 永不读这些员工/结算表（照 0010 对 console_users/tenants 的 REVOKE）。
REVOKE ALL ON agent_cost_pricing, agent_allocations, agent_settlements FROM app_tenant;
