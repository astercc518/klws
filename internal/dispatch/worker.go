// internal/dispatch/worker.go
package dispatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/acme/wadist/internal/billing"
)

// ProcessSend runs one send end-to-end:
//
//	Admit (deny → requeue recipient, no charge) → Hold → render → (resolve media)
//	→ Sender.Send → success: Settle + mark sent ; failure: health signal (if ban) +
//	RequestRefund + mark failed, return the send error so asynq retries.
//
// mediaSha=="" means text-only.
func (w *SendWorker) ProcessSend(ctx context.Context, pl SendPayload, body, mediaSha, mime string, raw []byte) error {
	dec, err := w.gate.Admit(ctx, pl.JID, time.Now())
	if err != nil {
		return fmt.Errorf("admit: %w", err)
	}
	if !dec.Allow {
		return w.requeue(ctx, pl, dec.Reason) // back to pending, no charge
	}

	if _, err := w.billing.Hold(ctx, billing.HoldRequest{
		TenantID:    pl.TenantID,
		AccountJID:  pl.JID,
		MessageID:   pl.MessageID,
		CountryCode: pl.Country,
		Amount:      1, // unit price; real price injected at wiring layer
	}); err != nil {
		dec.Ticket.Release(ctx)
		return w.requeue(ctx, pl, "billing:"+err.Error())
	}

	rendered := renderTemplate(body, pl.Vars)
	var media *MediaHandle
	if mediaSha != "" {
		media, err = w.resolveMedia(ctx, pl.JID, mediaSha, mime, raw)
		if err != nil {
			_ = w.billing.RequestRefund(ctx, pl.TenantID, pl.MessageID, "media: "+err.Error())
			return w.markFailed(ctx, pl, err)
		}
	}

	waID, sendErr := w.sender.Send(ctx, pl.JID, pl.Phone, rendered, media)
	if sendErr != nil {
		if isBanSignal(sendErr) {
			_ = w.gate.ApplyHealthSignal(ctx, pl.JID, "wa_warning", 6*time.Hour)
		} else {
			_ = w.gate.ApplyHealthSignal(ctx, pl.JID, "undelivered", 0)
		}
		_ = w.billing.RequestRefund(ctx, pl.TenantID, pl.MessageID, sendErr.Error())
		_ = w.markFailed(ctx, pl, sendErr)
		return sendErr // surface for asynq retry
	}

	if err := w.billing.Settle(ctx, pl.TenantID, pl.MessageID); err != nil {
		return fmt.Errorf("settle: %w", err) // retry-safe (Settle idempotent)
	}
	_ = w.gate.ApplyHealthSignal(ctx, pl.JID, "delivered", 0)
	return w.markSent(ctx, pl, waID)
}

func isBanSignal(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "wa_warning") || strings.Contains(s, "banned") || strings.Contains(s, "403")
}

func (w *SendWorker) requeue(ctx context.Context, pl SendPayload, reason string) error {
	_, err := w.pool.Exec(ctx,
		`UPDATE campaign_recipients SET state='pending', assigned_jid=NULL, last_error=$2 WHERE id=$1`,
		pl.RecipientID, reason)
	if err != nil {
		return fmt.Errorf("requeue recipient: %w", err)
	}
	return nil
}

func (w *SendWorker) markFailed(ctx context.Context, pl SendPayload, cause error) error {
	if _, err := w.pool.Exec(ctx,
		`UPDATE campaign_recipients SET state='failed', last_error=$2 WHERE id=$1`,
		pl.RecipientID, cause.Error()); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	_, _ = w.pool.Exec(ctx, `UPDATE campaigns SET failed=failed+1 WHERE id=$1`, pl.CampaignID)
	return nil
}

func (w *SendWorker) markSent(ctx context.Context, pl SendPayload, waID string) error {
	if _, err := w.pool.Exec(ctx,
		`UPDATE campaign_recipients SET state='sent', message_id=$2 WHERE id=$1`,
		pl.RecipientID, waID); err != nil {
		return fmt.Errorf("mark sent: %w", err)
	}
	_, _ = w.pool.Exec(ctx, `UPDATE campaigns SET sent=sent+1 WHERE id=$1`, pl.CampaignID)
	return nil
}
