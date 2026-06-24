-- migrations/0007_cluster_nodes.sql
CREATE TABLE IF NOT EXISTS cluster_nodes (
    node_id            TEXT        NOT NULL PRIMARY KEY,
    last_heartbeat_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    registered_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

DROP TRIGGER IF EXISTS trg_node_touch ON cluster_nodes;
CREATE TRIGGER trg_node_touch BEFORE UPDATE ON cluster_nodes
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
