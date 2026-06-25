-- migrations/0010_console_users.sql — console login accounts (admin/sales/customer).
CREATE TABLE IF NOT EXISTS console_users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL CHECK (role IN ('admin','sales','customer')),
    tenant_id     BIGINT      REFERENCES tenants(id),
    disabled      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- customers MUST map to a tenant; staff (admin/sales) MUST NOT.
    CONSTRAINT console_users_tenant_role CHECK (
        (role = 'customer' AND tenant_id IS NOT NULL) OR
        (role IN ('admin','sales') AND tenant_id IS NULL)
    )
);

DROP TRIGGER IF EXISTS trg_console_users_touch ON console_users;
CREATE TRIGGER trg_console_users_touch BEFORE UPDATE ON console_users
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- sales ownership FK (added after both tables exist).
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_sales_owner_fk;
ALTER TABLE tenants ADD CONSTRAINT tenants_sales_owner_fk
    FOREIGN KEY (sales_owner_id) REFERENCES console_users(id);

-- Defense in depth: the RLS tenant role must never read login/registry tables.
-- 0008's ALTER DEFAULT PRIVILEGES auto-grants new tables to app_tenant; revoke it.
-- (Roles app_tenant/app_system are created idempotently in 0008, which runs first.)
REVOKE ALL ON tenants, console_users FROM app_tenant;
