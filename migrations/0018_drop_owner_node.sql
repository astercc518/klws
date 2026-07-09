-- owner_node 展示镜像退役：所有权真相迁至 Redis owner:{jid}，
-- 管理后台归属列/筛选/计数改从 Redis 读。
DROP INDEX IF EXISTS idx_acc_owner_node;
ALTER TABLE account_devices DROP COLUMN IF EXISTS owner_node;
