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
CREATE INDEX IF NOT EXISTS idx_acc_owner_node ON account_devices (owner_node) WHERE owner_node IS NOT NULL;
