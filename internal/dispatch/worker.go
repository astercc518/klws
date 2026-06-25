// internal/dispatch/worker.go
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/canary"
)

// ProcessSend runs one send end-to-end:
//
//	Admit (deny → requeue recipient, no charge) → Hold → render → (resolve media)
//	→ Sender.Send → success: Settle + mark sent ; failure: health signal (if ban) +
//	RequestRefund + mark failed, return the send error so asynq retries.
//
// mediaSha=="" means text-only.
func (w *SendWorker) ProcessSend(ctx context.Context, pl SendPayload, body, mediaSha, mime string, raw []byte) error {
	// Idempotency guard: asynq delivers at-least-once; skip if already terminal.
	var st string
	if err := w.pool.QueryRow(ctx, `SELECT state::text FROM campaign_recipients WHERE id=$1`, pl.RecipientID).Scan(&st); err != nil {
		return fmt.Errorf("load recipient state: %w", err)
	}
	if st == "sent" || st == "failed" {
		w.m.RecordSend("idempotent_skip")
		w.m.RecordCohortSend(canary.Cohort(pl.JID, w.canaryPct), "idempotent_skip")
		return nil // terminal — already processed; at-least-once retry is a no-op
	}

	dec, err := w.gate.Admit(ctx, pl.JID, time.Now())
	if err != nil {
		return fmt.Errorf("admit: %w", err)
	}
	if !dec.Allow {
		w.m.RecordGate(false, dec.Reason)
		w.m.RecordSend("gate_denied")
		w.m.RecordCohortSend(canary.Cohort(pl.JID, w.canaryPct), "gate_denied")
		return w.requeue(ctx, pl, dec.Reason) // back to pending, no charge
	}
	w.m.RecordGate(true, "ok")

	if _, err := w.billing.Hold(ctx, billing.HoldRequest{
		TenantID:    pl.TenantID,
		AccountJID:  pl.JID,
		MessageID:   pl.MessageID,
		CountryCode: pl.Country,
		Amount:      w.amountFor(ctx, pl.TenantID, pl.Country),
	}); err != nil {
		w.m.RecordBilling("hold", holdOutcome(err))
		w.m.RecordSend("hold_failed")
		w.m.RecordCohortSend(canary.Cohort(pl.JID, w.canaryPct), "hold_failed")
		dec.Ticket.Release(ctx)
		return w.requeue(ctx, pl, "billing:"+err.Error())
	}
	w.m.RecordBilling("hold", "held")

	rendered := renderTemplate(body, pl.Vars)
	var media *MediaHandle
	if mediaSha != "" {
		media, err = w.resolveMedia(ctx, pl.JID, mediaSha, mime, raw)
		if err != nil {
			dec.Ticket.Release(ctx)
			_ = w.billing.RequestRefund(ctx, pl.TenantID, pl.MessageID, "media: "+err.Error())
			w.m.RecordSend("media_failed")
			w.m.RecordCohortSend(canary.Cohort(pl.JID, w.canaryPct), "media_failed")
			return w.markFailed(ctx, pl, err)
		}
	}

	waID, sendErr := w.sender.Send(ctx, pl.JID, pl.Phone, rendered, media)
	if sendErr != nil {
		if isBanSignal(sendErr) {
			if hsErr := w.gate.ApplyHealthSignal(ctx, pl.JID, "wa_warning", 6*time.Hour); hsErr != nil {
				sendErr = fmt.Errorf("%w; health-signal: %v", sendErr, hsErr)
			}
			w.m.RecordHealthSignal("wa_warning")
		} else {
			_ = w.gate.ApplyHealthSignal(ctx, pl.JID, "undelivered", 0)
		}
		_ = w.billing.RequestRefund(ctx, pl.TenantID, pl.MessageID, sendErr.Error())
		_ = w.markFailed(ctx, pl, sendErr)
		w.m.RecordSend("send_failed")
		w.m.RecordCohortSend(canary.Cohort(pl.JID, w.canaryPct), "send_failed")
		return sendErr
	}

	if err := w.billing.Settle(ctx, pl.TenantID, pl.MessageID); err != nil {
		w.m.RecordBilling("settle", "error")
		w.m.RecordSend("settle_failed")
		w.m.RecordCohortSend(canary.Cohort(pl.JID, w.canaryPct), "settle_failed")
		return fmt.Errorf("settle: %w", err) // retry-safe (Settle idempotent)
	}
	w.m.RecordBilling("settle", "ok")
	w.m.RecordSend("sent")
	w.m.RecordCohortSend(canary.Cohort(pl.JID, w.canaryPct), "sent")
	_ = w.gate.ApplyHealthSignal(ctx, pl.JID, "delivered", 0)
	return w.markSent(ctx, pl, waID)
}

func holdOutcome(err error) string {
	switch {
	case errors.Is(err, billing.ErrInsufficientFunds):
		return "insufficient_funds"
	case errors.Is(err, billing.ErrWalletLocked):
		return "wallet_locked"
	case errors.Is(err, billing.ErrNotFound):
		return "not_found"
	default:
		return "error"
	}
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
	if _, err := w.pool.Exec(ctx,
		`UPDATE account_devices SET sent_today = GREATEST(0, sent_today - 1) WHERE account_jid = $1`,
		pl.JID); err != nil {
		return fmt.Errorf("decrement sent_today (requeue): %w", err)
	}
	return nil
}

func (w *SendWorker) markFailed(ctx context.Context, pl SendPayload, cause error) error {
	if _, err := w.pool.Exec(ctx,
		`UPDATE campaign_recipients SET state='failed', last_error=$2 WHERE id=$1`,
		pl.RecipientID, cause.Error()); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	if _, err := w.pool.Exec(ctx,
		`UPDATE account_devices SET sent_today = GREATEST(0, sent_today - 1) WHERE account_jid = $1`,
		pl.JID); err != nil {
		return fmt.Errorf("decrement sent_today (failed): %w", err)
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
