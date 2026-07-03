package dispatch

import (
	"fmt"
	"testing"
	"time"
)

// TestReclaimOrphanedAssignments seeds one account with sent_today=1 and one
// pending recipient orphaned mid-assignment (assigned_jid set, deterministic
// message_id claimed, attempt bumped to 1 — as dispatchBatch would leave it if
// the in-memory pump crashed before the send completed). Reclaim must, in one
// tx: decrement the account's sent_today by the orphan count, then clear the
// assignment so dispatchBatch's `assigned_jid IS NULL` filter re-picks it.
func TestReclaimOrphanedAssignments(t *testing.T) {
	pool, ctx := pgPool(t)

	seedAccount(t, ctx, pool, "acc", "US", time.Now(), 100, 1) // sent_today=1
	cid := seedCampaign(t, ctx, pool, "hello")
	rid := seedRecipient(t, ctx, pool, cid, "+15550001111", "US")

	msgID := fmt.Sprintf("%d:%d", cid, rid)
	if _, err := pool.Exec(ctx, `
UPDATE campaign_recipients SET assigned_jid=$1, message_id=$2, attempt=1 WHERE id=$3`,
		"acc", msgID, rid); err != nil {
		t.Fatalf("seed orphan assignment: %v", err)
	}

	n, err := ReclaimOrphanedAssignments(ctx, pool)
	if err != nil || n != 1 {
		t.Fatalf("reclaim = %d,%v; want 1,nil", n, err)
	}

	var jid, mid *string
	var attempt, sent int
	if err := pool.QueryRow(ctx, `
SELECT r.assigned_jid, r.message_id, r.attempt, a.sent_today
  FROM campaign_recipients r JOIN account_devices a ON a.account_jid='acc'
 WHERE r.id=$1`, rid).Scan(&jid, &mid, &attempt, &sent); err != nil {
		t.Fatalf("query result: %v", err)
	}
	if jid != nil || mid != nil {
		t.Fatalf("orphan not reset: assigned_jid=%v message_id=%v", jid, mid)
	}
	if attempt != 0 {
		t.Fatalf("attempt = %d; want 0 (GREATEST(1-1,0))", attempt)
	}
	if sent != 0 {
		t.Fatalf("sent_today = %d; want 0 (decremented on reclaim)", sent)
	}

	// Idempotent-ish: nothing left to reclaim on a second pass.
	n2, err := ReclaimOrphanedAssignments(ctx, pool)
	if err != nil || n2 != 0 {
		t.Fatalf("second reclaim = %d,%v; want 0,nil", n2, err)
	}
}
