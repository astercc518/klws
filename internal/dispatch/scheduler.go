// internal/dispatch/scheduler.go
package dispatch

import (
	"context"
	"fmt"
)

// DispatchRunning scans all running campaigns and dispatches one batch each,
// returning the total number of recipients enqueued. Intended to be driven by a
// supervised background loop. Per-campaign errors abort the scan (caller retries
// next tick); a campaign with no capacity simply contributes 0.
func (d *Dispatcher) DispatchRunning(ctx context.Context, batch int) (int, error) {
	rows, err := d.pool.Query(ctx, `SELECT id FROM campaigns WHERE state='running' ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("scan running campaigns: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan campaign id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate campaigns: %w", err)
	}

	total := 0
	for _, id := range ids {
		n, err := d.dispatchBatch(ctx, id, batch)
		if err != nil {
			return total, fmt.Errorf("dispatch campaign %d: %w", id, err)
		}
		total += n
	}
	return total, nil
}

// DispatchRunningBudget dispatches at most `budget` recipients IN TOTAL across
// all running campaigns (unlike DispatchRunning, which gives each campaign the
// full batch). Used by the pump filler so cumulative enqueues never exceed the
// free channel slots — preventing a mid-batch ErrPumpFull rollback from
// orphaning already-pushed payloads.
func (d *Dispatcher) DispatchRunningBudget(ctx context.Context, budget int) (int, error) {
	if budget <= 0 {
		return 0, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT id FROM campaigns WHERE state='running' ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("scan running campaigns: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan campaign id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate campaigns: %w", err)
	}

	total := 0
	for _, id := range ids {
		if budget <= 0 {
			break
		}
		n, err := d.dispatchBatch(ctx, id, budget) // batch = REMAINING budget
		if err != nil {
			return total, fmt.Errorf("dispatch campaign %d: %w", id, err)
		}
		total += n
		budget -= n
	}
	return total, nil
}
