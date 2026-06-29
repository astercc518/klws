// Package receipt records WhatsApp delivery/read receipts against campaign
// recipients. It is intentionally decoupled from the whatsmeow event types and
// the red-line engine packages: the cluster layer translates an *events.Receipt
// into a plain receipt.Event and hands it here, and this package does nothing
// but a small idempotent SQL UPDATE keyed by the real message id. See
// docs/RECEIPT-INGESTION-DESIGN-zh.md.
package receipt

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Kind is the receipt milestone. read implies delivered implies sent.
type Kind string

const (
	Delivered Kind = "delivered"
	Read      Kind = "read"
)

// Event is a provider-agnostic receipt, translated from whatsmeow's
// events.Receipt by the cluster layer.
type Event struct {
	MessageIDs []string  // real WA message ids (match campaign_recipients.message_id)
	SenderJID  string    // the sending account JID (== campaign_recipients.assigned_jid)
	Kind       Kind      // Delivered | Read
	At         time.Time // receipt timestamp
}

// Recorder writes receipt milestones. Use a BYPASSRLS pool (SystemPool): a
// receipt carries no tenant context and is matched purely by message id + the
// owning account JID.
type Recorder struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Recorder { return &Recorder{pool: pool} }

// Record applies one receipt. Idempotent (COALESCE keeps the earliest time) and
// monotonic (a read also back-fills delivered_at). Matching on assigned_jid
// disambiguates the non-unique message_id. A receipt for an unknown message id
// simply updates zero rows.
func (r *Recorder) Record(ctx context.Context, ev Event) error {
	if len(ev.MessageIDs) == 0 || ev.SenderJID == "" {
		return nil
	}
	at := ev.At
	if at.IsZero() {
		at = time.Now()
	}
	var sql string
	switch ev.Kind {
	case Delivered:
		sql = `UPDATE campaign_recipients
		          SET delivered_at = COALESCE(delivered_at, $3)
		        WHERE message_id = ANY($1::text[]) AND assigned_jid = $2`
	case Read:
		sql = `UPDATE campaign_recipients
		          SET read_at      = COALESCE(read_at, $3),
		              delivered_at = COALESCE(delivered_at, $3)
		        WHERE message_id = ANY($1::text[]) AND assigned_jid = $2`
	default:
		return fmt.Errorf("receipt: unknown kind %q", ev.Kind)
	}
	if _, err := r.pool.Exec(ctx, sql, ev.MessageIDs, ev.SenderJID, at); err != nil {
		return fmt.Errorf("receipt record (%s): %w", ev.Kind, err)
	}
	return nil
}
