-- migrations/0013_device_tags.sql
-- Account tags for anti-association grouping (e.g. 'US-Marketing', 'Tier1').
-- Purely additive metadata: no change to the RLS policy, proxy-binding
-- transactions, or any internal/store business logic.
ALTER TABLE account_devices
    ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}';

-- GIN index so "filter by tag" / tag containment stays fast as the pool grows.
CREATE INDEX IF NOT EXISTS idx_acc_tags ON account_devices USING GIN (tags);
