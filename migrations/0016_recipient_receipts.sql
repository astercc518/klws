-- migrations/0016_recipient_receipts.sql
-- Delivery / read receipt milestones for campaign recipients. Additive and
-- monotonic: a message is sent, then (later) delivered, then (later) read — so
-- these are timestamps, NOT mutually-exclusive states. The recipient_state_t
-- enum and the dispatch state machine are intentionally left untouched.
-- See docs/RECEIPT-INGESTION-DESIGN-zh.md. Populated by internal/receipt once
-- the node-side whatsmeow receipt hook (phases 2/3) is authorized & wired.
ALTER TABLE campaign_recipients ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;
ALTER TABLE campaign_recipients ADD COLUMN IF NOT EXISTS read_at      TIMESTAMPTZ;

-- Receipts correlate by the whatsmeow message id (campaign_recipients.message_id
-- holds the real WA id after markSent); index it for fast lookups.
CREATE INDEX IF NOT EXISTS idx_recip_msgid ON campaign_recipients (message_id);
