// internal/node/takeover.go
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
)

const TypeTakeover = "cluster:takeover"

type takeoverPayload struct {
	JID string `json:"jid"`
}

type TakeoverEnqueuer struct {
	client    *asynq.Client
	uniqueTTL time.Duration
}

func NewTakeoverEnqueuer(client *asynq.Client, uniqueTTL time.Duration) *TakeoverEnqueuer {
	if uniqueTTL < time.Second {
		uniqueTTL = time.Second
	}
	return &TakeoverEnqueuer{client: client, uniqueTTL: uniqueTTL}
}

// Enqueue submits a takeover task, deduplicated cluster-wide via asynq.Unique
// (key = type+payload+queue over shared Redis). A duplicate within the TTL is
// swallowed (returns nil) — another scan/node already queued it.
func (e *TakeoverEnqueuer) Enqueue(ctx context.Context, jid string) error {
	b, err := json.Marshal(takeoverPayload{JID: jid})
	if err != nil {
		return fmt.Errorf("marshal takeover: %w", err)
	}
	_, err = e.client.EnqueueContext(ctx, asynq.NewTask(TypeTakeover, b),
		asynq.Queue("takeover"), asynq.Unique(e.uniqueTTL))
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue takeover: %w", err)
	}
	return nil
}

// RegisterTakeoverHandler wires the takeover task to StartAccountWithLock.
func RegisterTakeoverHandler(mux *asynq.ServeMux, o *Orchestrator) {
	mux.HandleFunc(TypeTakeover, func(ctx context.Context, t *asynq.Task) error {
		var p takeoverPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("unmarshal takeover: %w", err)
		}
		_, err := o.StartAccountWithLock(ctx, p.JID)
		return err // nil if skipped (peer holds lock) — task done
	})
}

// RunHeartbeat is a managed loop body: refresh this node's heartbeat each tick.
func (o *Orchestrator) RunHeartbeat(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := o.mgr.UpsertNodeHeartbeat(ctx, o.nodeID); err != nil {
				o.logger.Errorf("heartbeat: %v", err)
			}
		}
	}
}

func (o *Orchestrator) scanOnce(ctx context.Context, staleness time.Duration, enq *TakeoverEnqueuer) (int, error) {
	stale, err := o.mgr.StaleOwnedAccounts(ctx, staleness)
	if err != nil {
		return 0, fmt.Errorf("scan stale: %w", err)
	}
	unowned, err := o.mgr.ListUnownedActiveAccounts(ctx)
	if err != nil {
		return 0, fmt.Errorf("scan unowned: %w", err)
	}

	// Merge both sets; asynq.Unique deduplicates across nodes cluster-wide.
	all := append(stale, unowned...)
	n := 0
	var lastErr error
	for _, jid := range all {
		if err := enq.Enqueue(ctx, jid); err != nil {
			o.logger.Errorf("enqueue takeover %s: %v", jid, err)
			lastErr = err
			continue
		}
		n++
	}
	return n, lastErr
}

// RunTakeoverScanner is a managed loop: each tick, enqueue takeover for accounts
// owned by stale nodes or with no owner (deduped via Unique).
func (o *Orchestrator) RunTakeoverScanner(ctx context.Context, scanInterval, staleness time.Duration, enq *TakeoverEnqueuer) error {
	t := time.NewTicker(scanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, err := o.scanOnce(ctx, staleness, enq); err != nil {
				o.logger.Errorf("takeover scan: %v", err)
			}
		}
	}
}
