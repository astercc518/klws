-- 0021: 联系人资源库（模块二切片一）。按 tenant 轴，FORCE RLS 复刻 0020。replay-all 幂等。
DO $$ BEGIN CREATE TYPE contact_status_t AS ENUM ('active','unsubscribed','invalid');
  EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE TABLE IF NOT EXISTS contacts (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id    BIGINT NOT NULL,
    phone        TEXT   NOT NULL,
    phone_bidx   BYTEA  NOT NULL,
    country_code CHAR(2),
    display_name TEXT,
    status       contact_status_t NOT NULL DEFAULT 'active',
    source       TEXT,
    vars         JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, phone_bidx)
);
CREATE INDEX IF NOT EXISTS idx_contacts_tenant_status ON contacts (tenant_id, status);

CREATE TABLE IF NOT EXISTS contact_tags (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS contact_tag_map (
    tenant_id  BIGINT NOT NULL,
    contact_id BIGINT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    tag_id     BIGINT NOT NULL REFERENCES contact_tags(id) ON DELETE CASCADE,
    PRIMARY KEY (contact_id, tag_id)
);

CREATE TABLE IF NOT EXISTS contact_segments (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    name TEXT NOT NULL,
    filter JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS contact_import_batches (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    filename TEXT,
    total INT NOT NULL DEFAULT 0,
    inserted INT NOT NULL DEFAULT 0,
    duplicates INT NOT NULL DEFAULT 0,
    invalid INT NOT NULL DEFAULT 0,
    actor_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

DROP TRIGGER IF EXISTS trg_contacts_touch ON contacts;
CREATE TRIGGER trg_contacts_touch BEFORE UPDATE ON contacts
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- FORCE RLS on every tenant-scoped table (policy verbatim from 0020).
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['contacts','contact_tags','contact_tag_map','contact_segments','contact_import_batches']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format($f$CREATE POLICY tenant_isolation ON %I
      USING (tenant_id = current_setting('app.current_tenant_id', true)::bigint)
      WITH CHECK (tenant_id = current_setting('app.current_tenant_id', true)::bigint)$f$, t);
  END LOOP;
END $$;
