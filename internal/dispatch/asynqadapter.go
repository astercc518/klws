// internal/dispatch/asynqadapter.go
package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
)

const TypeSend = "dispatch:send"

// AsynqEnqueuer implements Enqueuer over an asynq client.
type AsynqEnqueuer struct {
	client *asynq.Client
	queue  string
	retry  int
}

func NewAsynqEnqueuer(client *asynq.Client, queue string, maxRetry int) *AsynqEnqueuer {
	return &AsynqEnqueuer{client: client, queue: queue, retry: maxRetry}
}

func (a *AsynqEnqueuer) EnqueueSend(ctx context.Context, p SendPayload, delay time.Duration) error {
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	_, err = a.client.Enqueue(asynq.NewTask(TypeSend, b),
		asynq.Queue(a.queue), asynq.ProcessIn(delay), asynq.MaxRetry(a.retry))
	if err != nil {
		return fmt.Errorf("enqueue send: %w", err)
	}
	return nil
}

// RegisterSendHandler wires the asynq mux to ProcessSend. Thin glue; not unit-tested
// (requires a running asynq server). Exercised via integration/deploy.
func RegisterSendHandler(mux *asynq.ServeMux, w *SendWorker, resolve SendBodyResolver) {
	mux.HandleFunc(TypeSend, func(ctx context.Context, t *asynq.Task) error {
		var p SendPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
		}
		body, mediaSha, mime, raw, err := resolve(ctx, p.CampaignID)
		if err != nil {
			return fmt.Errorf("resolve body: %w", err)
		}
		return w.ProcessSend(ctx, p, body, mediaSha, mime, raw)
	})
}

var _ Enqueuer = (*AsynqEnqueuer)(nil)
