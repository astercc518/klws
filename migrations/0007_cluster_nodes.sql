-- migrations/0007_cluster_nodes.sql
CREATE TABLE IF NOT EXISTS cluster_nodes (
    node_id           TEXT PRIMARY KEY,
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

DROP TRIGGER IF EXISTS trg_nodes_touch ON cluster_nodes;
CREATE TRIGGER trg_nodes_touch BEFORE UPDATE ON cluster_nodes
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE INDEX IF NOT EXISTS idx_nodes_heartbeat ON cluster_nodes (last_heartbeat_at);

-- owner_node was DROPPed by migrations/0018_drop_owner_node.sql. Migrations in
-- this repo are replayed idempotently on every deploy (no schema_migrations
-- tracking table — see scripts/migrate_twice.sh), so this file still runs
-- after 0018 on every subsequent redeploy; guard against the column being
-- gone so a fresh install (0007 before 0018 in file order) still gets the
-- index, while a replay against an already-0018'd DB is a safe no-op.
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
         WHERE table_name = 'account_devices' AND column_name = 'owner_node'
    ) THEN
        CREATE INDEX IF NOT EXISTS idx_acc_owner_node ON account_devices (owner_node) WHERE owner_node IS NOT NULL;
    END IF;
END $$;
