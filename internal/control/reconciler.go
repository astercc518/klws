// internal/control/reconciler.go
package control

import (
	"context"
	"log"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// ReconcileDeps 注入回调，保持 control 零引擎依赖。
type ReconcileDeps struct {
	ListActive func(ctx context.Context) ([]string, error) // 跨租户活跃账号
	CountryOf  func(ctx context.Context, jid string) string
	NextDue    func(cc string) int64 // 新调度项的 due 时间（含抖动，由 main 注入）
}

type Reconciler struct {
	rdb   *goredis.Client
	sched *Scheduler
	ccs   []string
	deps  ReconcileDeps
}

func NewReconciler(rdb *goredis.Client, sched *Scheduler, ccs []string, deps ReconcileDeps) *Reconciler {
	return &Reconciler{rdb: rdb, sched: sched, ccs: ccs, deps: deps}
}

// Tick 把"既非温驻留、又不在任一 due 队列"的活跃账号补入 due-ZSet。
// 返回本轮补入的账号数。
func (r *Reconciler) Tick(ctx context.Context, nowMs int64) (int, error) {
	active, err := r.deps.ListActive(ctx)
	if err != nil {
		return 0, err
	}

	// 一次性把 resident 与各国 due 成员拉进内存 set，避免每账号往返 Redis。
	resident := map[string]struct{}{}
	for _, jid := range r.rdb.SMembers(ctx, residentKey).Val() {
		resident[jid] = struct{}{}
	}
	scheduled := map[string]struct{}{}
	ccSet := make(map[string]struct{}, len(r.ccs))
	for _, cc := range r.ccs {
		ccSet[cc] = struct{}{}
		for _, jid := range r.rdb.ZRange(ctx, dueKey(cc), 0, -1).Val() {
			scheduled[jid] = struct{}{}
		}
	}

	added := 0
	for _, jid := range active {
		if _, warm := resident[jid]; warm {
			continue // 已温
		}
		if _, queued := scheduled[jid]; queued {
			continue // 已排队
		}
		cc := r.deps.CountryOf(ctx, jid)
		if _, active := ccSet[cc]; !active {
			continue // cc 不在活跃国家集合内（含未绑定 cc==""）：走被动 warm:req，不主动调度
		}
		if err := r.sched.Enqueue(ctx, jid, cc, r.deps.NextDue(cc)); err != nil {
			log.Printf("control: reconciler enqueue %s: %v", jid, err)
			continue
		}
		added++
	}
	return added, nil
}

// Run 每 interval 调 Tick；interval<=0 表示禁用。Tick 错误记录并继续。
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if n, err := r.Tick(ctx, time.Now().UnixMilli()); err != nil {
				log.Printf("control: reconciler tick error: %v", err)
			} else if n > 0 {
				log.Printf("control: reconciler re-enqueued %d unscheduled accounts", n)
			}
		}
	}
}
